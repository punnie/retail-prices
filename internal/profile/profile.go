// Package profile implements `retail profile`: a read-only pass over the raw
// products log that answers, from the data itself, the questions that decide
// how the derived schema and quality rules must be built. See Step 2 of the
// project plan. Nothing here writes to the database except a couple of
// indexes that every later step needs anyway.
package profile

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"retail-prices/internal/db"
	"retail-prices/internal/normalize"
)

func Run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("profile", flag.ContinueOnError)
	out := fs.String("out", "profile_report.md", "path to write the full report to (in addition to stdout progress)")
	samplePct := fs.Float64("sample-pct", 3.0, "approximate percentage of rows to sample for text-format coverage checks")
	if err := fs.Parse(args); err != nil {
		return err
	}

	pool, err := db.Connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquiring connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SET work_mem = '512MB'"); err != nil {
		return fmt.Errorf("setting work_mem: %w", err)
	}

	var report strings.Builder
	fmt.Fprintf(&report, "# Raw data profile\n\ngenerated %s\n\n", time.Now().UTC().Format(time.RFC3339))

	steps := []struct {
		title string
		fn    func(ctx context.Context, conn *pgxpool.Conn, w io.Writer) error
	}{
		{"1. Overview", overview},
		{"2. Ensure indexes needed for the queries below", ensureIndexes},
		{"3. Run structure (gap-clustering on scraped_at)", runStructure},
		{"4. Per-product observation cadence", observationCadence},
		{"5. Price-change distribution (log-ratio between consecutive observations)", priceChangeDistribution},
		{"6. Sentinel / junk price values", sentinelValues},
		{"7. Field hygiene (whitespace, empty category/brand)", fieldHygiene},
	}

	for _, s := range steps {
		fmt.Fprintf(os.Stderr, "==> %s\n", s.title)
		fmt.Fprintf(&report, "## %s\n\n", s.title)
		if err := s.fn(ctx, conn, &report); err != nil {
			return fmt.Errorf("%s: %w", s.title, err)
		}
		fmt.Fprintln(&report)
	}

	fmt.Fprintf(os.Stderr, "==> 8. Sampled text-format coverage (SKU extraction, pack-size parsing)\n")
	fmt.Fprintf(&report, "## 8. Sampled text-format coverage (SKU extraction, pack-size parsing)\n\n")
	if err := sampledCoverage(ctx, conn, &report, *samplePct); err != nil {
		return fmt.Errorf("sampled coverage: %w", err)
	}

	if err := os.WriteFile(*out, []byte(report.String()), 0o644); err != nil {
		return fmt.Errorf("writing report to %s: %w", *out, err)
	}

	fmt.Println(report.String())
	fmt.Fprintf(os.Stderr, "\nfull report written to %s\n", *out)
	return nil
}

func newTabWriter(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
}

// --- 1. Overview -----------------------------------------------------------

func overview(ctx context.Context, conn *pgxpool.Conn, w io.Writer) error {
	rows, err := conn.Query(ctx, `
		SELECT store, count(*) AS rows, count(DISTINCT url) AS distinct_urls,
		       min(scraped_at) AS first_seen, max(scraped_at) AS last_seen
		FROM products
		GROUP BY store
		ORDER BY store`)
	if err != nil {
		return err
	}
	defer rows.Close()

	tw := newTabWriter(w)
	fmt.Fprintln(tw, "store\trows\tdistinct urls\tfirst seen\tlast seen")
	for rows.Next() {
		var store string
		var n, distinct int64
		var first, last time.Time
		if err := rows.Scan(&store, &n, &distinct, &first, &last); err != nil {
			return err
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%s\t%s\n", store, n, distinct,
			first.Format("2006-01-02"), last.Format("2006-01-02"))
	}
	return finish(tw, rows.Err(), w)
}

// --- 2. Indexes --------------------------------------------------------------

// These are the two indexes every later step (this profiler included) needs
// for per-product time series work. The raw `products` table's rows are never
// touched; this only adds read-optimizing indexes.
func ensureIndexes(ctx context.Context, conn *pgxpool.Conn, w io.Writer) error {
	stmts := []string{
		`CREATE INDEX IF NOT EXISTS idx_products_store_url_scraped_at ON products (store, url, scraped_at)`,
	}
	for _, stmt := range stmts {
		start := time.Now()
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return err
		}
		fmt.Fprintf(w, "- `%s` (%s)\n", stmt, time.Since(start).Round(time.Millisecond))
	}
	return nil
}

// --- 3. Run structure --------------------------------------------------------

// A "run" is a burst of scraped_at timestamps for a store with no gap wider
// than runGapThreshold inside it. This tells us whether runs are regular,
// full-catalogue passes or ragged partial ones.
const runGapThreshold = 2 * time.Hour

func runStructure(ctx context.Context, conn *pgxpool.Conn, w io.Writer) error {
	rows, err := conn.Query(ctx, `
		WITH gapped AS (
			SELECT store, scraped_at,
			       scraped_at - lag(scraped_at) OVER (PARTITION BY store ORDER BY scraped_at) AS gap
			FROM products
		), tagged AS (
			SELECT store, scraped_at,
			       sum(CASE WHEN gap IS NULL OR gap > $1::interval THEN 1 ELSE 0 END)
			         OVER (PARTITION BY store ORDER BY scraped_at) AS run_id
			FROM gapped
		)
		SELECT store, count(*) AS num_runs,
		       min(n) AS min_run_size, max(n) AS max_run_size,
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY n) AS median_run_size
		FROM (
			SELECT store, run_id, count(*) AS n
			FROM tagged
			GROUP BY store, run_id
		) per_run
		GROUP BY store
		ORDER BY store`, runGapThreshold.String())
	if err != nil {
		return err
	}
	defer rows.Close()

	tw := newTabWriter(w)
	fmt.Fprintf(tw, "gap threshold: %s\n\n", runGapThreshold)
	fmt.Fprintln(tw, "store\t#runs\tmin run size\tmedian run size\tmax run size")
	for rows.Next() {
		var store string
		var numRuns, minSize, maxSize int64
		var medianSize float64
		if err := rows.Scan(&store, &numRuns, &minSize, &maxSize, &medianSize); err != nil {
			return err
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%.0f\t%d\n", store, numRuns, minSize, medianSize, maxSize)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	fmt.Fprintln(tw)
	fmt.Fprintln(tw, "A wide min/median/max spread means run size is not a reliable")
	fmt.Fprintln(tw, "'complete catalogue' signal on its own — some runs are partial")
	fmt.Fprintln(tw, "(--since deltas, aborted runs, or sitemap limits).")
	return tw.Flush()
}

// --- 4. Per-product observation cadence -------------------------------------

func observationCadence(ctx context.Context, conn *pgxpool.Conn, w io.Writer) error {
	rows, err := conn.Query(ctx, `
		WITH per_product AS (
			SELECT store, url, count(*) AS obs
			FROM products
			GROUP BY store, url
		)
		SELECT store,
		       count(*) AS distinct_products,
		       count(*) FILTER (WHERE obs = 1) AS single_observation,
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY obs) AS median_obs,
		       max(obs) AS max_obs
		FROM per_product
		GROUP BY store
		ORDER BY store`)
	if err != nil {
		return err
	}
	defer rows.Close()

	tw := newTabWriter(w)
	fmt.Fprintln(tw, "store\tdistinct products\tsingle-observation (phantom candidates)\tmedian obs/product\tmax obs/product")
	for rows.Next() {
		var store string
		var distinct, single, maxObs int64
		var medianObs float64
		if err := rows.Scan(&store, &distinct, &single, &medianObs, &maxObs); err != nil {
			return err
		}
		pct := 100 * float64(single) / float64(distinct)
		fmt.Fprintf(tw, "%s\t%d\t%d (%.1f%%)\t%.0f\t%d\n", store, distinct, single, pct, medianObs, maxObs)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	fmt.Fprintln(tw)
	fmt.Fprintln(tw, "A high single-observation share is consistent with sitemap phantoms")
	fmt.Fprintln(tw, "(scraped once, never republished) rather than genuine delistings.")
	return tw.Flush()
}

// --- 5. Price-change distribution --------------------------------------------

func priceChangeDistribution(ctx context.Context, conn *pgxpool.Conn, w io.Writer) error {
	rows, err := conn.Query(ctx, `
		WITH ordered AS (
			SELECT store, url, price, scraped_at,
			       lag(price) OVER (PARTITION BY store, url ORDER BY scraped_at) AS prev_price
			FROM products
		), transitions AS (
			SELECT store, ln(price / prev_price) AS log_ratio, price / prev_price AS ratio
			FROM ordered
			WHERE prev_price IS NOT NULL AND prev_price > 0 AND price > 0 AND price <> prev_price
		)
		SELECT store,
		       count(*) AS transitions,
		       percentile_cont(0.01) WITHIN GROUP (ORDER BY log_ratio) AS p01,
		       percentile_cont(0.50) WITHIN GROUP (ORDER BY log_ratio) AS p50,
		       percentile_cont(0.99) WITHIN GROUP (ORDER BY log_ratio) AS p99,
		       count(*) FILTER (WHERE ratio >= 10 OR ratio <= 0.1) AS extreme_10x,
		       count(*) FILTER (WHERE ratio >= 100 OR ratio <= 0.01) AS extreme_100x
		FROM transitions
		GROUP BY store
		ORDER BY store`)
	if err != nil {
		return err
	}
	defer rows.Close()

	tw := newTabWriter(w)
	fmt.Fprintln(tw, "store\ttransitions\tp1 log-ratio\tmedian log-ratio\tp99 log-ratio\t>=10x or <=0.1x\t>=100x or <=0.01x")
	for rows.Next() {
		var store string
		var n, extreme10, extreme100 int64
		var p01, p50, p99 float64
		if err := rows.Scan(&store, &n, &p01, &p50, &p99, &extreme10, &extreme100); err != nil {
			return err
		}
		fmt.Fprintf(tw, "%s\t%d\t%.3f (%.0f%%)\t%.3f (%.0f%%)\t%.3f (%.0f%%)\t%d\t%d\n",
			store, n,
			p01, 100*(math.Exp(p01)-1),
			p50, 100*(math.Exp(p50)-1),
			p99, 100*(math.Exp(p99)-1),
			extreme10, extreme100)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	fmt.Fprintln(tw)
	fmt.Fprintln(tw, "log-ratio shown with the equivalent %% price change in parens.")
	fmt.Fprintln(tw, "This sets the threshold for 'implausible' in the quality layer:")
	fmt.Fprintln(tw, "the p1/p99 band is real promo/increase noise; the >=10x/100x counts")
	fmt.Fprintln(tw, "below are the scale of the 0.99->1000.00-style glitches to filter.")
	return tw.Flush()
}

// --- 6. Sentinel / junk values -----------------------------------------------

func sentinelValues(ctx context.Context, conn *pgxpool.Conn, w io.Writer) error {
	tw := newTabWriter(w)

	fmt.Fprintln(tw, "candidate sentinel exact-value counts:")
	fmt.Fprintln(tw, "price\tcount")
	rows, err := conn.Query(ctx, `
		SELECT price, count(*) AS n
		FROM products
		WHERE price IN (0, 0.01, 1, 999.99, 1000, 9999, 9999.99)
		GROUP BY price
		ORDER BY price`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var price float64
		var n int64
		if err := rows.Scan(&price, &n); err != nil {
			rows.Close()
			return err
		}
		fmt.Fprintf(tw, "%.2f\t%d\n", price, n)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	fmt.Fprintln(tw)
	fmt.Fprintln(tw, "top 15 most frequent exact prices overall (eyeball for sentinels):")
	fmt.Fprintln(tw, "price\tcount")
	rows2, err := conn.Query(ctx, `
		SELECT price, count(*) AS n
		FROM products
		GROUP BY price
		ORDER BY n DESC
		LIMIT 15`)
	if err != nil {
		return err
	}
	defer rows2.Close()
	for rows2.Next() {
		var price float64
		var n int64
		if err := rows2.Scan(&price, &n); err != nil {
			return err
		}
		fmt.Fprintf(tw, "%.2f\t%d\n", price, n)
	}
	return finish(tw, rows2.Err(), w)
}

// --- 7. Field hygiene ---------------------------------------------------------

func fieldHygiene(ctx context.Context, conn *pgxpool.Conn, w io.Writer) error {
	rows, err := conn.Query(ctx, `
		SELECT store,
		       count(*) FILTER (WHERE name <> btrim(name)) AS name_whitespace,
		       count(*) FILTER (WHERE category IS NULL OR btrim(category) = '') AS empty_category,
		       count(*) FILTER (WHERE brand IS NULL OR btrim(brand) = '') AS empty_brand,
		       count(*) FILTER (WHERE description IS NULL OR btrim(description) = '') AS empty_description
		FROM products
		GROUP BY store
		ORDER BY store`)
	if err != nil {
		return err
	}
	defer rows.Close()

	tw := newTabWriter(w)
	fmt.Fprintln(tw, "store\tname has leading/trailing ws\tempty category\tempty brand\tempty description")
	for rows.Next() {
		var store string
		var nameWS, emptyCat, emptyBrand, emptyDesc int64
		if err := rows.Scan(&store, &nameWS, &emptyCat, &emptyBrand, &emptyDesc); err != nil {
			return err
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\n", store, nameWS, emptyCat, emptyBrand, emptyDesc)
	}
	return finish(tw, rows.Err(), w)
}

// --- 8. Sampled text-format coverage ------------------------------------------

// SKU extraction and pack-size parsing both live in internal/normalize —
// this just measures that real code's coverage over a fresh sample, so the
// number here always reflects what `retail refresh` will actually produce.

type coverageCounter struct {
	total       int
	skuMatched  int
	packMatched int
	skuMisses   []string
	packMisses  []string
}

func sampledCoverage(ctx context.Context, conn *pgxpool.Conn, w io.Writer, samplePct float64) error {
	rows, err := conn.Query(ctx,
		`SELECT store, url, name, description FROM products TABLESAMPLE SYSTEM ($1)`, samplePct)
	if err != nil {
		return err
	}
	defer rows.Close()

	counters := map[string]*coverageCounter{}
	for rows.Next() {
		var store, url, name string
		var description *string
		if err := rows.Scan(&store, &url, &name, &description); err != nil {
			return err
		}
		c, ok := counters[store]
		if !ok {
			c = &coverageCounter{}
			counters[store] = c
		}
		c.total++

		if _, ok := normalize.ExtractSKU(store, url); ok {
			c.skuMatched++
		} else if len(c.skuMisses) < 5 {
			c.skuMisses = append(c.skuMisses, url)
		}

		desc := ""
		if description != nil {
			desc = *description
		}
		if _, ok := normalize.ParsePackSize(store, name, desc); ok {
			c.packMatched++
		} else if len(c.packMisses) < 5 {
			c.packMisses = append(c.packMisses, desc)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	tw := newTabWriter(w)
	fmt.Fprintf(tw, "sample size requested: ~%.1f%% of rows via TABLESAMPLE SYSTEM\n\n", samplePct)
	fmt.Fprintln(tw, "store\tsampled rows\tSKU-in-URL matched\tpack-size-in-description matched")
	for _, store := range []string{"Auchan", "Continente", "Pingo Doce", "Celeiro"} {
		c, ok := counters[store]
		if !ok || c.total == 0 {
			fmt.Fprintf(tw, "%s\t0\t-\t-\n", store)
			continue
		}
		fmt.Fprintf(tw, "%s\t%d\t%d (%.1f%%)\t%d (%.1f%%)\n", store, c.total,
			c.skuMatched, 100*float64(c.skuMatched)/float64(c.total),
			c.packMatched, 100*float64(c.packMatched)/float64(c.total))
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	fmt.Fprintln(w)
	for _, store := range []string{"Auchan", "Continente", "Pingo Doce", "Celeiro"} {
		c, ok := counters[store]
		if !ok {
			continue
		}
		if len(c.skuMisses) > 0 {
			fmt.Fprintf(w, "- %s SKU-pattern misses (sample): %s\n", store, strings.Join(quoteAll(c.skuMisses), "; "))
		}
		if len(c.packMisses) > 0 {
			fmt.Fprintf(w, "- %s pack-pattern misses (sample): %s\n", store, strings.Join(quoteAll(c.packMisses), "; "))
		}
	}
	return nil
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = "`" + s + "`"
	}
	return out
}

func finish(tw *tabwriter.Writer, rowsErr error, w io.Writer) error {
	if rowsErr != nil {
		return rowsErr
	}
	return tw.Flush()
}
