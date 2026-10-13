// Package refresh rebuilds the derived layer (scrape_runs, retail_products,
// price_intervals, price_spells, regular_price_changes,
// product_pricing_snapshot) from the raw products log.
//
// Everything it derives is partitioned by store, so a refresh runs one store
// at a time, each inside a single transaction: readers (`retail serve`) keep
// seeing the previous catalogue for that store until the new one commits,
// and a refresh that fails part-way rolls back instead of leaving the store
// half-rebuilt. `retail refresh --store <id>` rebuilds one store — what
// `retail run` does after each scrape — and plain `retail refresh` rebuilds
// every store in turn.
//
// retail_products is upserted by (store, sku) so its id — the join key used
// by human-curated tables (match_decisions, product_group_members,
// basket_items) — never changes across refreshes. The rest have no such
// external references, so a store's rows are simply deleted and rebuilt in
// full each run. There is no incremental watermarking yet: every refresh
// rescans the store's whole raw history, which the profiler showed takes low
// minutes even for all 28M rows.
package refresh

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"retail-prices/internal/db"
	"retail-prices/internal/normalize"
	"retail-prices/internal/quality"
	"retail-prices/internal/retailer"
)

// A product is phantom if it was seen in a smaller share of the substantial
// scrape runs that happened during its own lifespan than this — the
// sitemap-phantom signal from profile step 4, generalized from "exactly one
// observation" to "vanishingly few". Measuring over the product's own
// lifespan (rather than the store's whole history) avoids branding genuinely
// new products as phantoms just because they did not exist a year ago.
const phantomMaxRunShare = 0.10

// ...and regardless of share, a product with fewer observations than this
// has no usable price history: there is not enough signal to tell a real
// price from a placeholder. The 3-observation Continente bread roll whose
// only prices were two placeholders (55.00, 39.99) and one real 0.89 is the
// motivating case.
const phantomMinObservations = 5

// flushEvery bounds memory: price_intervals/price_events rows accumulate in
// memory and are bulk-loaded via COPY every this many products.
const flushEvery = 5000

// lockKey serializes refreshes: each one sets work_mem to 512MB and scans a
// store's whole history, so two at once (say, two retailers' scrapes ending
// together) would only compete for the same memory and disk. The later one
// waits for the earlier to commit.
const lockKey = "retail-refresh"

func Run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("refresh", flag.ContinueOnError)
	pricingOnly := fs.Bool("pricing-only", false,
		"skip the raw-log rescan and only redo placeholder derivation + pricing analysis "+
			"(phases B and C); use when only pricing_params or the classifier changed")
	storeID := fs.String("store", "", "refresh only this store ("+strings.Join(retailer.IDs(), ", ")+"); default: every store")
	if err := fs.Parse(args); err != nil {
		return err
	}

	stores := retailer.All
	if *storeID != "" {
		r, err := retailer.ByID(*storeID)
		if err != nil {
			return err
		}
		stores = []*retailer.Retailer{r}
	}

	pool, err := db.Connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	for _, r := range stores {
		if err := Store(ctx, pool, r.Name, *pricingOnly); err != nil {
			return fmt.Errorf("refreshing %s: %w", r.Name, err)
		}
	}
	return nil
}

// Store rebuilds one store's derived rows in a single transaction. With
// pricingOnly it skips the raw-log rescan and only redoes placeholder
// derivation and pricing analysis (phases B and C).
func Store(ctx context.Context, pool *pgxpool.Pool, store string, pricingOnly bool) error {
	started := time.Now()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) // no-op after Commit

	logf("==> %s: waiting for any other refresh to finish", store)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, lockKey); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "SET LOCAL work_mem = '512MB'"); err != nil {
		return err
	}

	if pricingOnly {
		logf("==> %s: pricing-only: resetting derived pricing analysis", store)
		if err := deletePricing(ctx, tx, store); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE price_intervals SET is_suspect = false
			WHERE is_suspect AND retail_product_id IN (SELECT id FROM retail_products WHERE store = $1)`,
			store); err != nil {
			return err
		}
	} else {
		logf("==> %s: rebuilding scrape_runs", store)
		if err := refreshScrapeRuns(ctx, tx, store); err != nil {
			return fmt.Errorf("refreshing scrape_runs: %w", err)
		}

		runIdx, err := loadRunIndex(ctx, tx, store)
		if err != nil {
			return fmt.Errorf("loading run counts: %w", err)
		}

		logf("==> %s: clearing price_intervals, price_spells, regular_price_changes, product_pricing_snapshot", store)
		if err := deletePricing(ctx, tx, store); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM price_intervals
			WHERE retail_product_id IN (SELECT id FROM retail_products WHERE store = $1)`, store); err != nil {
			return err
		}

		// Phase A builds the product dimension and the raw price series.
		// Phase B then derives the store's placeholder prices, which needs
		// the baselines Phase A computed — a price is only identifiable as
		// filler by looking at whether the products wearing it normally cost
		// anything like it. Phase C re-reads the (much smaller) interval
		// series to flag outliers and run the spell decomposition with that
		// store-level knowledge in hand.
		logf("==> %s: phase A: raw products -> retail_products, price_intervals", store)
		if err := refreshProducts(ctx, pool, tx, store, runIdx); err != nil {
			return err
		}

		logf("==> %s: flagging generic-name non-products", store)
		if err := flagGenericNames(ctx, tx, store); err != nil {
			return fmt.Errorf("flagging generic-name products: %w", err)
		}
	}

	logf("==> %s: phase B: deriving store placeholder prices", store)
	if err := refreshPlaceholders(ctx, tx, store); err != nil {
		return fmt.Errorf("deriving placeholder prices: %w", err)
	}

	logf("==> %s: phase C: pricing analysis", store)
	if err := classifyStore(ctx, tx, store); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	logf("==> %s: refreshed in %s", store, time.Since(started).Round(time.Second))
	return nil
}

// deletePricing removes a store's phase C output.
func deletePricing(ctx context.Context, tx pgx.Tx, store string) error {
	for _, table := range []string{"price_spells", "regular_price_changes", "product_pricing_snapshot"} {
		if _, err := tx.Exec(ctx, `
			DELETE FROM `+table+`
			WHERE retail_product_id IN (SELECT id FROM retail_products WHERE store = $1)`, store); err != nil {
			return fmt.Errorf("clearing %s: %w", table, err)
		}
	}
	return nil
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// Placeholder-detection thresholds. A price must be used broadly enough to be
// a store-level convention rather than a coincidence, must mostly attach to
// products that do not normally cost anything near it, and — critically —
// must be a price products never come *back* to.
//
// That last condition is what keeps genuine promotions safe. Continente's
// filler prices include everyday amounts like 4.49, 3.99 and 8.99, which are
// also exactly where real promotions land. Near-baseline share alone cannot
// separate them (they sit at 18–55%, straddling any cutoff), but recurrence
// separates them cleanly with a wide margin: products revisit real promo
// prices (4.49 → 2.45 intervals per product, 3.99 → 1.86, 8.99 → 1.31) and
// never revisit filler (every true placeholder measured ≤ 1.09, including
// Auchan's 1.90 at 1.05 and Continente's 500.00 at 1.000). Dropping this
// condition would quarantine real Continente promotions.
const (
	placeholderMinProducts            = 200
	placeholderMaxNearBasePct         = 60.0
	placeholderMaxIntervalsPerProduct = 1.15
)

func refreshPlaceholders(ctx context.Context, tx pgx.Tx, store string) error {
	if _, err := tx.Exec(ctx, "DELETE FROM store_placeholder_prices WHERE store = $1", store); err != nil {
		return err
	}

	_, err := tx.Exec(ctx, `
		INSERT INTO store_placeholder_prices (store, price, products, near_baseline_pct)
		SELECT store, price, products, near_pct
		FROM (
			SELECT rp.store,
			       round(pi.price::numeric, 2) AS price,
			       count(DISTINCT rp.id) AS products,
			       100.0 * count(*) FILTER (
			           WHERE rp.baseline_price BETWEEN pi.price / 2 AND pi.price * 2
			       ) / count(*) AS near_pct,
			       count(*)::float8 / count(DISTINCT rp.id) AS intervals_per_product
			FROM price_intervals pi
			JOIN retail_products rp ON rp.id = pi.retail_product_id
			WHERE rp.store = $4 AND rp.baseline_price > 0 AND pi.price > 0
			GROUP BY rp.store, round(pi.price::numeric, 2)
		) stats
		WHERE products >= $1
		  AND near_pct < $2
		  AND intervals_per_product <= $3`,
		placeholderMinProducts, placeholderMaxNearBasePct, placeholderMaxIntervalsPerProduct, store)
	return err
}

func refreshScrapeRuns(ctx context.Context, tx pgx.Tx, store string) error {
	if _, err := tx.Exec(ctx, "DELETE FROM scrape_runs WHERE store = $1", store); err != nil {
		return err
	}

	// completeness_ratio is measured against the store's *median* run size,
	// not its largest. A single unusually large run (a backfill, or a rare
	// full crawl) would otherwise drag the threshold so high that almost no
	// run qualifies as complete — at 80% of max, Continente had 3 complete
	// runs out of 288, which made the phantom-share denominator meaningless
	// and let 3-observation products through as legitimately tracked.
	_, err := tx.Exec(ctx, `
		WITH per_run AS (
			SELECT store, run_id, count(*) AS n, min(scraped_at) AS run_started, max(scraped_at) AS run_ended
			FROM (
				SELECT store, scraped_at,
				       sum(CASE WHEN gap IS NULL OR gap > interval '2 hours' THEN 1 ELSE 0 END)
				         OVER (PARTITION BY store ORDER BY scraped_at) AS run_id
				FROM (
					SELECT store, scraped_at,
					       scraped_at - lag(scraped_at) OVER (PARTITION BY store ORDER BY scraped_at) AS gap
					FROM products
					WHERE store = $1
				) gapped
			) tagged
			GROUP BY store, run_id
		), store_median AS (
			SELECT store, percentile_cont(0.5) WITHIN GROUP (ORDER BY n) AS median_n
			FROM per_run GROUP BY store
		)
		INSERT INTO scrape_runs (store, run_started, run_ended, observation_count, completeness_ratio, is_complete)
		SELECT pr.store, pr.run_started, pr.run_ended, pr.n,
		       pr.n::float8 / sm.median_n AS completeness_ratio,
		       pr.n::float8 / sm.median_n >= 0.5 AS is_complete
		FROM per_run pr JOIN store_median sm ON sm.store = pr.store
		WHERE sm.median_n > 0`, store)
	return err
}

// runIndex holds, per store, the start times of every substantial (complete)
// scrape run, sorted ascending — small enough to keep in memory (about a
// thousand rows in total) and enough to answer "how many real chances did
// this product have to be seen while it existed?".
type runIndex map[string][]time.Time

// countBetween returns how many substantial runs for a store fall within
// [from, to] inclusive: the denominator for that product's coverage.
func (ri runIndex) countBetween(store string, from, to time.Time) int {
	starts := ri[store]
	lo := sort.Search(len(starts), func(i int) bool { return !starts[i].Before(from) })
	hi := sort.Search(len(starts), func(i int) bool { return starts[i].After(to) })
	return hi - lo
}

func loadRunIndex(ctx context.Context, tx pgx.Tx, store string) (runIndex, error) {
	rows, err := tx.Query(ctx,
		`SELECT store, run_started FROM scrape_runs WHERE is_complete AND store = $1 ORDER BY run_started`, store)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ri := runIndex{}
	for rows.Next() {
		var store string
		var startedAt time.Time
		if err := rows.Scan(&store, &startedAt); err != nil {
			return nil, err
		}
		ri[store] = append(ri[store], startedAt)
	}
	return ri, rows.Err()
}

type rawRow struct {
	url, name, brand, category, imageURL, description string
	price                                             float64
	scrapedAt                                         time.Time
}

// skuSQLExpr computes the same SKU that internal/normalize.ExtractSKU does,
// but in SQL, so the big stream below can be ordered by (store, sku,
// scraped_at) directly — grouping by raw url instead would fragment a
// product's history whenever a retailer regenerates its URL slug while
// keeping the product id (Continente does this constantly: the same SKU
// resurfaces under word-order permutations of its own slug). Both come from
// the patterns in internal/retailer.
var skuSQLExpr = retailer.SKUSQLExpr()

// refreshProducts streams one store's raw log ordered by (sku, scraped_at)
// on its own pooled connection while tx performs the upserts and COPY loads.
// These must be different connections: a pgx connection can't run a write
// while a Query's Rows on that same connection are still open. Reading
// outside the transaction is safe because refresh never writes products.
func refreshProducts(ctx context.Context, pool *pgxpool.Pool, tx pgx.Tx, store string, runIdx runIndex) error {
	readConn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer readConn.Release()
	if _, err := readConn.Exec(ctx, "SET work_mem = '512MB'"); err != nil {
		return err
	}
	writeConn := tx

	rows, err := readConn.Query(ctx, `
		SELECT store, sku, url, name,
		       coalesce(brand, ''), coalesce(category, ''), coalesce(image_url, ''), coalesce(description, ''),
		       price, scraped_at
		FROM (
			SELECT *, `+skuSQLExpr+` AS sku
			FROM products
			WHERE store = $1 AND price > 0
		) s
		WHERE sku IS NOT NULL
		ORDER BY store, sku, scraped_at`, store)
	if err != nil {
		return err
	}
	defer rows.Close()

	var curStore, curSKU string
	var buf []rawRow

	var intervalRows [][]any
	var productsProcessed, productsUpserted int

	flush := func() error {
		if len(intervalRows) == 0 {
			return nil
		}
		if _, err := writeConn.CopyFrom(ctx, pgx.Identifier{"price_intervals"},
			[]string{"retail_product_id", "price", "valid_from", "valid_to", "observation_count"},
			pgx.CopyFromRows(intervalRows)); err != nil {
			return fmt.Errorf("copying price_intervals: %w", err)
		}
		intervalRows = intervalRows[:0]
		return nil
	}

	processGroup := func(store, sku string, group []rawRow) error {
		obs := make([]quality.Observation, len(group))
		for i, r := range group {
			obs[i] = quality.Observation{Price: r.price, ScrapedAt: r.scrapedAt}
		}
		// Suspect flags and events are deferred to phase C, which needs the
		// store-level placeholder prices that can only be derived once every
		// product's baseline exists.
		intervals := quality.BuildIntervals(obs)
		baseline := quality.BaselinePrice(intervals)

		id, err := upsertRetailProduct(ctx, writeConn, store, sku, group, runIdx, baseline)
		if err != nil {
			return fmt.Errorf("upserting %s %s: %w", store, sku, err)
		}
		productsUpserted++

		for _, iv := range intervals {
			intervalRows = append(intervalRows, []any{id, iv.Price, iv.ValidFrom, iv.ValidTo, iv.ObservationCount})
		}
		return nil
	}

	for rows.Next() {
		var store, sku string
		var r rawRow
		if err := rows.Scan(&store, &sku, &r.url, &r.name, &r.brand, &r.category, &r.imageURL, &r.description, &r.price, &r.scrapedAt); err != nil {
			return err
		}

		if store != curStore || sku != curSKU {
			if len(buf) > 0 {
				if err := processGroup(curStore, curSKU, buf); err != nil {
					return err
				}
				productsProcessed++
				if productsProcessed%flushEvery == 0 {
					if err := flush(); err != nil {
						return err
					}
					fmt.Fprintf(os.Stderr, "  processed %d products (%d upserted)\n", productsProcessed, productsUpserted)
				}
			}
			curStore, curSKU = store, sku
			buf = buf[:0]
		}
		buf = append(buf, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(buf) > 0 {
		if err := processGroup(curStore, curSKU, buf); err != nil {
			return err
		}
		productsProcessed++
	}
	// The read cursor (readConn) must be fully drained before flush() issues
	// writes on writeConn — a different connection, so no conflict, but the
	// loop above only leaves the read loop once rows.Next() returns false.
	if err := flush(); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "done: %d products processed, %d upserted into retail_products\n", productsProcessed, productsUpserted)
	return nil
}

// upsertRetailProduct derives the retail_products row for one (store, sku)
// group — the full observation history across every URL slug variant that
// SKU has ever appeared under, already merged and ordered by scraped_at —
// and upserts it, returning the stable id. Rows with no extractable SKU (a
// non-product page that leaked into the scrape, see profile step 8) are
// already excluded upstream by the query's `WHERE sku IS NOT NULL`.
func upsertRetailProduct(ctx context.Context, conn pgx.Tx, store, sku string, group []rawRow, runIdx runIndex, baseline float64) (int64, error) {
	last := group[len(group)-1]
	firstSeen, lastSeen := group[0].scrapedAt, last.scrapedAt
	observationCount := len(group)

	pack, hasPack := normalize.ParsePackSize(store, last.name, last.description)

	// Phantom if there is too little history to trust, either absolutely or
	// relative to the substantial runs that happened while it existed.
	isPhantom := observationCount < phantomMinObservations
	if !isPhantom {
		if chances := runIdx.countBetween(store, firstSeen, lastSeen); chances > 0 {
			isPhantom = float64(observationCount)/float64(chances) < phantomMaxRunShare
		}
	}

	var packValue *float64
	var packUnit *string
	var unitCount *int
	if hasPack {
		v := pack.TotalValue
		u := string(pack.Unit)
		c := pack.UnitCount
		packValue, packUnit, unitCount = &v, &u, &c
	}

	var baselinePtr *float64
	if baseline > 0 {
		baselinePtr = &baseline
	}

	var id int64
	err := conn.QueryRow(ctx, `
		INSERT INTO retail_products
			(store, sku, url, name, brand, category, image_url, name_norm, brand_norm,
			 pack_value, pack_unit, unit_count, is_phantom, first_seen, last_seen, observation_count,
			 baseline_price)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT (store, sku) DO UPDATE SET
			url = EXCLUDED.url, name = EXCLUDED.name, brand = EXCLUDED.brand,
			category = EXCLUDED.category, image_url = EXCLUDED.image_url,
			name_norm = EXCLUDED.name_norm, brand_norm = EXCLUDED.brand_norm,
			pack_value = EXCLUDED.pack_value, pack_unit = EXCLUDED.pack_unit, unit_count = EXCLUDED.unit_count,
			is_phantom = EXCLUDED.is_phantom, first_seen = EXCLUDED.first_seen,
			last_seen = EXCLUDED.last_seen, observation_count = EXCLUDED.observation_count,
			baseline_price = EXCLUDED.baseline_price
		RETURNING id`,
		store, sku, last.url, last.name, nullIfEmpty(last.brand), nullIfEmpty(last.category), nullIfEmpty(last.imageURL),
		normalize.NormalizeName(last.name), nullIfEmpty(normalize.NormalizeBrand(last.brand)),
		packValue, packUnit, unitCount, isPhantom, firstSeen, lastSeen, observationCount,
		baselinePtr,
	).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
