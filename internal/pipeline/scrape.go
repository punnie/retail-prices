// Package pipeline runs a retailer end to end — scrape, then refresh that
// store's slice of the catalogue, then look for match candidates among its
// new products — and records each scrape in scrape_jobs.
package pipeline

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"retail-prices/internal/db"
	"retail-prices/internal/retailer"
	"retail-prices/internal/scrape"
	"retail-prices/internal/scrape/config"
	"retail-prices/internal/scrape/rawlog"
)

// Job statuses, as stored in scrape_jobs.status.
const (
	StatusRunning = "running"
	StatusOK      = "ok"
	StatusPartial = "partial"
	StatusFailed  = "failed"
)

// partialProductShare is the share of failed product pages above which a
// scrape counts as partial rather than ok. Some failures are normal: dead
// sitemap entries redirect away and are reported as failures.
const partialProductShare = 0.20

// firstRunLookback is how far back the first scrape of a store looks when
// there is no earlier job to start from (the old NixOS module's fallback).
const firstRunLookback = 20

// ScrapeResult is what one recorded scrape did.
type ScrapeResult struct {
	JobID     int64
	StartedAt time.Time
	Status    string
	Counts    rawlog.Counts
}

// ScrapeOptions tunes how a scrape picks its --since date.
type ScrapeOptions struct {
	// Since, when set, overrides the date derived from earlier jobs.
	Since *time.Time
	// LegacyLastRunFile is read when the store has no earlier job: the
	// lastRun-<store> file the old price-scraper NixOS module kept, holding
	// a YYYY-MM-DD date. A missing file is not an error.
	LegacyLastRunFile string
}

// RunScrape is `retail scrape <store>`.
func RunScrape(ctx context.Context, args []string) error {
	r, opts, err := parseStoreArgs("scrape", args)
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
	if res.Status == StatusFailed {
		return fmt.Errorf("scrape of %s failed (job %d)", r.Name, res.JobID)
	}
	return nil
}

// Scrape scrapes one retailer and records the run in scrape_jobs. It returns
// an error only when the job could not be recorded; a scrape that ran but
// went wrong is reported through the result's Status.
func Scrape(ctx context.Context, pool *pgxpool.Pool, r *retailer.Retailer, opts ScrapeOptions) (ScrapeResult, error) {
	since, err := sinceDate(ctx, pool, r, opts)
	if err != nil {
		return ScrapeResult{}, err
	}

	var res ScrapeResult
	if err := pool.QueryRow(ctx,
		`INSERT INTO scrape_jobs (store, since_date) VALUES ($1, $2) RETURNING id, started_at`,
		r.Name, since).Scan(&res.JobID, &res.StartedAt); err != nil {
		return res, fmt.Errorf("recording scrape job: %w", err)
	}

	cfg := config.Load()
	cfg.SinceDate = &since
	fmt.Fprintf(os.Stderr, "==> scraping %s since %s (job %d)\n", r.Name, since.Format("2006-01-02"), res.JobID)

	w := rawlog.NewWriter(pool)
	scrapeErr := scrape.Scrape(r, cfg, w)
	res.Counts = w.Counts()
	res.Status = jobStatus(scrapeErr, res.Counts)

	var errText *string
	if scrapeErr != nil {
		s := scrapeErr.Error()
		errText = &s
	}
	// The scrape may have run for hours; record its outcome even if the
	// caller's context was cancelled meanwhile.
	if _, err := pool.Exec(context.WithoutCancel(ctx), `
		UPDATE scrape_jobs
		SET finished_at = now(), status = $2, products_saved = $3, products_failed = $4,
		    sources_failed = $5, error = $6
		WHERE id = $1`,
		res.JobID, res.Status, res.Counts.Saved, res.Counts.ProductsFailed, res.Counts.SourcesFailed, errText); err != nil {
		return res, fmt.Errorf("recording scrape job %d outcome: %w", res.JobID, err)
	}

	fmt.Fprintf(os.Stderr, "==> %s scrape %s: %d saved, %d products failed, %d sources failed\n",
		r.Name, res.Status, res.Counts.Saved, res.Counts.ProductsFailed, res.Counts.SourcesFailed)
	if scrapeErr != nil {
		fmt.Fprintf(os.Stderr, "    error: %v\n", scrapeErr)
	}
	return res, nil
}

// jobStatus classifies a finished scrape. A run that saved nothing is only a
// failure if something actually went wrong: with --since, a quiet day can
// legitimately have no changed products.
func jobStatus(scrapeErr error, c rawlog.Counts) string {
	switch {
	case scrapeErr != nil:
		return StatusFailed
	case c.Saved == 0 && (c.ProductsFailed > 0 || c.SourcesFailed > 0):
		return StatusFailed
	case c.SourcesFailed > 0:
		return StatusPartial
	case float64(c.ProductsFailed) > partialProductShare*float64(c.Saved+c.ProductsFailed):
		return StatusPartial
	default:
		return StatusOK
	}
}

// sinceDate picks the date a scrape filters sitemap entries by: an explicit
// override, else the start date of the store's last scrape that did not
// fail, else the legacy lastRun file, else firstRunLookback years ago.
// Partial scrapes count, as they did when the old module only looked at the
// scraper's exit code.
func sinceDate(ctx context.Context, pool *pgxpool.Pool, r *retailer.Retailer, opts ScrapeOptions) (time.Time, error) {
	if opts.Since != nil {
		return *opts.Since, nil
	}

	var last time.Time
	err := pool.QueryRow(ctx, `
		SELECT (started_at AT TIME ZONE 'UTC')::date
		FROM scrape_jobs
		WHERE store = $1 AND status IN ('ok', 'partial')
		ORDER BY started_at DESC
		LIMIT 1`, r.Name).Scan(&last)
	switch {
	case err == nil:
		return last, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return time.Time{}, fmt.Errorf("finding %s's last scrape: %w", r.Name, err)
	}

	if opts.LegacyLastRunFile != "" {
		b, err := os.ReadFile(opts.LegacyLastRunFile)
		switch {
		case err == nil:
			d, err := time.Parse("2006-01-02", strings.TrimSpace(string(b)))
			if err != nil {
				return time.Time{}, fmt.Errorf("reading %s: %w", opts.LegacyLastRunFile, err)
			}
			fmt.Fprintf(os.Stderr, "==> no earlier %s scrape job; using %s from %s\n",
				r.Name, d.Format("2006-01-02"), opts.LegacyLastRunFile)
			return d, nil
		case !errors.Is(err, os.ErrNotExist):
			return time.Time{}, err
		}
	}

	y, m, d := time.Now().UTC().AddDate(-firstRunLookback, 0, 0).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC), nil
}

// parseStoreArgs parses `<store> [--since=YYYY-MM-DD] [--legacy-last-run-file=PATH]`
// plus any extra flags the caller registers on fs.
func parseStoreArgs(cmd string, args []string, extra ...func(*flag.FlagSet)) (*retailer.Retailer, ScrapeOptions, error) {
	var opts ScrapeOptions
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	since := fs.String("since", "", "only scrape products modified after this date (YYYY-MM-DD); default: the start date of the store's last successful scrape")
	fs.StringVar(&opts.LegacyLastRunFile, "legacy-last-run-file", "",
		"lastRun file from the old price-scraper module, used only when the store has no earlier scrape job")
	for _, f := range extra {
		f(fs)
	}
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: retail %s <store> [flags]\nstores: %s\n", cmd, strings.Join(retailer.IDs(), ", "))
		fs.PrintDefaults()
	}

	// Accept the store before or after the flags.
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		args = append(append([]string{}, args[1:]...), args[0])
	}
	if err := fs.Parse(args); err != nil {
		return nil, opts, err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return nil, opts, fmt.Errorf("expected exactly one store")
	}
	r, err := retailer.ByID(fs.Arg(0))
	if err != nil {
		return nil, opts, err
	}
	if *since != "" {
		d, err := time.Parse("2006-01-02", *since)
		if err != nil {
			return nil, opts, fmt.Errorf("--since must be YYYY-MM-DD: %w", err)
		}
		opts.Since = &d
	}
	return r, opts, nil
}
