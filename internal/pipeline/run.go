package pipeline

import (
	"context"
	"fmt"
	"os"

	"retail-prices/internal/db"
	"retail-prices/internal/matching"
	"retail-prices/internal/refresh"
)

// RunStore is `retail run <store>`: scrape one retailer, then — as soon as
// the scrape ends — rebuild that store's slice of the catalogue, then look
// for match candidates among the products this scrape saw for the first
// time.
//
// The refresh runs whenever the scrape saved anything, even if it was
// partial or failed part-way: the log is append-only and refresh already
// discounts thin runs, so the catalogue should reflect whatever was
// observed. Matching is best-effort; its failure is reported but doesn't
// fail the run. The command exits non-zero if the scrape or the refresh
// failed.
func RunStore(ctx context.Context, args []string) error {
	r, opts, err := parseStoreArgs("run", args)
	if err != nil {
		return err
	}
	pool, err := db.Connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	res, err := Scrape(ctx, pool, r, opts)
	if err != nil {
		return err
	}

	var scrapeErr error
	if res.Status == StatusFailed {
		scrapeErr = fmt.Errorf("scrape of %s failed (job %d)", r.Name, res.JobID)
	}
	if res.Counts.Saved == 0 {
		fmt.Fprintf(os.Stderr, "==> %s: nothing new was saved; the catalogue is unchanged\n", r.Name)
		return scrapeErr
	}

	if err := refresh.Store(ctx, pool, r.Name, false); err != nil {
		return fmt.Errorf("refreshing %s: %w", r.Name, err)
	}

	sc := matching.Scope{Store: r.Name, Since: res.StartedAt}
	if err := matching.Generate(ctx, sc, matching.DefaultOptions); err != nil {
		fmt.Fprintf(os.Stderr, "==> %s: matching new products failed (run `retail match --store %s` to retry): %v\n",
			r.Name, r.ID, err)
	}
	return scrapeErr
}
