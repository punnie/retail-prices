package matching

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"retail-prices/internal/db"
	"retail-prices/internal/retailer"
)

// blockingReason records which signal produced a candidate, so a future
// second blocking strategy (e.g. pack-size + price, for products whose
// names diverge across retailers) can coexist without overwriting this one's
// rows.
const blockingReason = "name_trgm"

// batchSize bounds how many upserts are pipelined per round trip, per worker.
const batchSize = 1000

// progressEvery is how often (in pairs considered by one worker) that
// worker logs a progress line.
const progressEvery = 20000

// Run generates cross-retailer match candidates for human review: for every
// product, finds its nearest other-store neighbours by name_norm trigram
// distance (via the GiST index idx_retail_products_name_trgm_gist — see
// migration 006 for why GiST and not the GIN index also present on this
// column), scores each with Score, and upserts the ones above --min-score
// into match_candidates. Nothing here decides a match — every candidate
// still needs a human decision via match_decisions (see internal/web's
// /match review queue).
//
// The catalogue is sharded by product id across --workers concurrent
// connections, each running the same per-anchor query independently — a
// full run over ~180k products is a genuinely large, embarrassingly
// parallel batch job (see the package doc comment on why each anchor's
// query, however fast individually, adds up), so this isn't optional
// polish.
func Run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("match", flag.ContinueOnError)
	d := DefaultOptions
	topN := fs.Int("top-n", d.TopN, "candidate neighbours to consider per product")
	simFloor := fs.Float64("sim-floor", d.SimFloor, "name-similarity floor a candidate must clear to be scored at all (recall net, not the final score)")
	minScore := fs.Float64("min-score", d.MinScore, "minimum combined Score to persist as a candidate")
	workers := fs.Int("workers", d.Workers, "products processed concurrently, each on its own connection (shards the catalogue by id)")
	storeID := fs.String("store", "", "only look for matches of this store's products ("+strings.Join(retailer.IDs(), ", ")+"); default: every store")
	sinceStr := fs.String("since", "", "only look for matches of products first seen at or after this time (RFC 3339 or YYYY-MM-DD)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var sc Scope
	if *storeID != "" {
		r, err := retailer.ByID(*storeID)
		if err != nil {
			return err
		}
		sc.Store = r.Name
	}
	if *sinceStr != "" {
		t, err := time.Parse(time.RFC3339, *sinceStr)
		if err != nil {
			if t, err = time.Parse(time.DateOnly, *sinceStr); err != nil {
				return fmt.Errorf("--since must be RFC 3339 or YYYY-MM-DD: %w", err)
			}
		}
		sc.Since = t
	}
	return Generate(ctx, sc, Options{TopN: *topN, SimFloor: *simFloor, MinScore: *minScore, Workers: *workers})
}

// Scope narrows which products Generate looks for matches of. The zero
// Scope is the whole catalogue.
type Scope struct {
	Store string    // stored retailer name; "" for every store
	Since time.Time // only products first seen at or after this; zero for all
}

func (sc Scope) whole() bool { return sc.Store == "" && sc.Since.IsZero() }

// Options are Generate's tuning knobs; see the `retail match` flags.
type Options struct {
	TopN               int
	SimFloor, MinScore float64
	Workers            int
}

// DefaultOptions are the `retail match` flag defaults.
var DefaultOptions = Options{TopN: 8, SimFloor: 0.35, MinScore: 0.3, Workers: 8}

// Generate finds and scores match candidates for the products in scope.
func Generate(ctx context.Context, sc Scope, o Options) error {
	if o.SimFloor < 0 || o.SimFloor > 1 {
		return fmt.Errorf("--sim-floor must be in [0,1], got %v", o.SimFloor)
	}
	if o.Workers < 1 {
		return fmt.Errorf("--workers must be >= 1, got %d", o.Workers)
	}

	url, err := db.URL()
	if err != nil {
		return err
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return fmt.Errorf("parsing DATABASE_URL: %w", err)
	}
	// internal/db.Connect's default pool is sized for the occasional query
	// a CLI command makes, not --workers concurrent long-running ones —
	// and each worker holds two connections at once (read + write, see
	// runShard).
	cfg.MaxConns = int32(o.Workers)*2 + 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connecting to postgres: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("pinging postgres: %w", err)
	}

	scopeDesc := "each product's"
	if !sc.whole() {
		scopeDesc = "each"
		if sc.Store != "" {
			scopeDesc += " " + sc.Store
		}
		scopeDesc += " product"
		if !sc.Since.IsZero() {
			scopeDesc += " first seen since " + sc.Since.Format(time.RFC3339)
		}
		scopeDesc += "'s"
	}
	fmt.Fprintf(os.Stderr, "==> finding %s %d nearest other-store neighbours by name (%d workers)\n", scopeDesc, o.TopN, o.Workers)

	var considered, kept int64
	var wg sync.WaitGroup
	errs := make([]error, o.Workers)
	for shard := 0; shard < o.Workers; shard++ {
		wg.Add(1)
		go func(shard int) {
			defer wg.Done()
			errs[shard] = runShard(ctx, pool, sc, shard, o.Workers, o.TopN, o.SimFloor, o.MinScore, &considered, &kept)
		}(shard)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}

	fmt.Fprintf(os.Stderr, "done: considered %d nearest-neighbour pairs, kept %d candidates (score >= %v)\n",
		atomic.LoadInt64(&considered), atomic.LoadInt64(&kept), o.MinScore)
	return nil
}

// runShard handles every anchor product whose id falls in one shard
// (`id % totalShards == shard`) on its own connection, independently of
// every other shard.
//
// Over the whole catalogue each pair is only generated from its lower-id
// side (`a.id < c.id`), since the other side is an anchor too. A scoped run
// can't rely on that — its anchors are mostly new, high-id products whose
// neighbours are older — so it keeps pairs from either side and stores them
// as (lower id, higher id), the same orientation a full run uses.
func runShard(ctx context.Context, pool *pgxpool.Pool, sc Scope, shard, totalShards, topN int, simFloor, minScore float64, considered, kept *int64) error {
	// Two connections, not one: a pgx connection can't run a write (the
	// periodic upsert batch below) while a streaming Query's Rows on that
	// same connection is still open — see the identical comment in
	// internal/refresh/refresh.go, which this mirrors.
	readConn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer readConn.Release()

	writeConn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer writeConn.Release()

	// Ordered by the <-> trigram-distance operator, not similarity() DESC:
	// that's what lets Postgres walk the GiST index
	// (idx_retail_products_name_trgm_gist) directly in nearest-first order
	// and stop at LIMIT, instead of pulling every candidate out of the heap
	// and sorting it in memory. That alone isn't enough, though — a quality
	// filter (originally `dist < maxDist` here) inside the ordered scan
	// stops Postgres from using LIMIT to bound the walk at all: the index
	// has no way to know "no more rows will ever pass" just because
	// distance is increasing, so for any product with fewer than --top-n
	// good matches (most of the catalogue — plenty of items are genuinely
	// store-exclusive) it walked almost the *entire* table before giving
	// up — confirmed via EXPLAIN ANALYZE ("Rows Removed by Filter:
	// 234683" on one anchor, 558ms; this is what Pedro saw as a hang). So:
	// no quality filter in SQL. LIMIT alone always finds topN *something*
	// cheaply (any cross-store, non-phantom product exists in abundance),
	// and simFloor is applied in Go below instead, where a weak match is
	// simply discarded rather than making the query search harder for a
	// better one that may not exist.
	rows, err := readConn.Query(ctx, `
		SELECT a.id, a.brand_norm, a.pack_value, a.pack_unit, a.baseline_price,
		       c.id, c.brand_norm, c.pack_value, c.pack_unit, c.baseline_price, c.dist
		FROM retail_products a
		CROSS JOIN LATERAL (
		    SELECT b.id, b.brand_norm, b.pack_value, b.pack_unit, b.baseline_price,
		           b.name_norm <-> a.name_norm AS dist
		    FROM retail_products b
		    WHERE b.store <> a.store
		      AND NOT b.is_phantom
		      AND NOT EXISTS (
		          SELECT 1 FROM match_decisions d
		          WHERE (d.left_product_id = a.id AND d.right_product_id = b.id)
		             OR (d.left_product_id = b.id AND d.right_product_id = a.id)
		      )
		    ORDER BY b.name_norm <-> a.name_norm
		    LIMIT $1
		) c
		WHERE NOT a.is_phantom AND a.id % $2 = $3
		  AND ($4::bool OR a.id < c.id)
		  AND ($5 = '' OR a.store = $5)
		  AND ($6::timestamptz IS NULL OR a.first_seen >= $6)`,
		topN, totalShards, shard, !sc.whole(), sc.Store, sinceArg(sc.Since))
	if err != nil {
		return fmt.Errorf("shard %d: querying candidates: %w", shard, err)
	}
	defer rows.Close()

	batch := &pgx.Batch{}
	var localConsidered, localKept int64
	flush := func() error {
		if batch.Len() == 0 {
			return nil
		}
		br := writeConn.SendBatch(ctx, batch)
		for i := 0; i < batch.Len(); i++ {
			if _, err := br.Exec(); err != nil {
				br.Close()
				return fmt.Errorf("shard %d: upserting match_candidates: %w", shard, err)
			}
		}
		if err := br.Close(); err != nil {
			return err
		}
		batch = &pgx.Batch{}
		return nil
	}

	for rows.Next() {
		var leftID, rightID int64
		var lBrand, rBrand, lPackUnit, rPackUnit *string
		var lPackValue, rPackValue, lBaseline, rBaseline *float64
		var dist float64
		if err := rows.Scan(&leftID, &lBrand, &lPackValue, &lPackUnit, &lBaseline,
			&rightID, &rBrand, &rPackValue, &rPackUnit, &rBaseline, &dist); err != nil {
			return err
		}
		localConsidered++
		if localConsidered%progressEvery == 0 {
			fmt.Fprintf(os.Stderr, "  [shard %d] considered %d pairs, kept %d so far (last anchor id %d; total so far: %d)\n",
				shard, localConsidered, localKept, leftID, atomic.LoadInt64(considered)+localConsidered)
		}
		sim := 1 - dist
		if sim < simFloor {
			continue
		}

		left := featuresFrom(lBrand, lPackValue, lPackUnit, lBaseline)
		right := featuresFrom(rBrand, rPackValue, rPackUnit, rBaseline)
		score := Score(left, right, sim)
		if score < minScore {
			continue
		}
		localKept++
		if leftID > rightID {
			leftID, rightID = rightID, leftID
		}

		batch.Queue(`
			INSERT INTO match_candidates (left_product_id, right_product_id, score, blocking_reason)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (left_product_id, right_product_id)
			DO UPDATE SET score = EXCLUDED.score, blocking_reason = EXCLUDED.blocking_reason`,
			leftID, rightID, score, blockingReason)

		if batch.Len() >= batchSize {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}

	atomic.AddInt64(considered, localConsidered)
	atomic.AddInt64(kept, localKept)
	return nil
}

func sinceArg(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func featuresFrom(brandNorm *string, packValue *float64, packUnit *string, baseline *float64) Features {
	var f Features
	if brandNorm != nil {
		f.BrandNorm = *brandNorm
	}
	if packUnit != nil {
		f.PackUnit = *packUnit
	}
	if packValue != nil && *packValue > 0 {
		v := *packValue
		f.PackValue = &v
		if baseline != nil && *baseline > 0 {
			up := *baseline / v
			f.UnitPrice = &up
		}
	}
	return f
}
