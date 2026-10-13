package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"retail-prices/internal/retailer"
	"retail-prices/internal/scrape/rawlog"
	"retail-prices/internal/testdb"
)

func TestJobStatus(t *testing.T) {
	cases := []struct {
		name string
		err  error
		c    rawlog.Counts
		want string
	}{
		{"clean", nil, rawlog.Counts{Saved: 100}, StatusOK},
		{"nothing changed", nil, rawlog.Counts{}, StatusOK},
		{"some dead links", nil, rawlog.Counts{Saved: 100, ProductsFailed: 10}, StatusOK},
		{"many dead links", nil, rawlog.Counts{Saved: 100, ProductsFailed: 30}, StatusPartial},
		{"a sitemap failed", nil, rawlog.Counts{Saved: 100, SourcesFailed: 1}, StatusPartial},
		{"everything failed", nil, rawlog.Counts{ProductsFailed: 5}, StatusFailed},
		{"index unreachable", errors.New("boom"), rawlog.Counts{}, StatusFailed},
	}
	for _, c := range cases {
		if got := jobStatus(c.err, c.c); got != c.want {
			t.Errorf("%s: jobStatus = %s, want %s", c.name, got, c.want)
		}
	}
}

func TestSinceDate(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	r, _ := retailer.ByID("celeiro")
	day := func(s string) time.Time {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	check := func(opts ScrapeOptions, want time.Time) {
		t.Helper()
		got, err := sinceDate(ctx, pool, r, opts)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Equal(want) {
			t.Errorf("sinceDate = %s, want %s", got.Format(time.DateOnly), want.Format(time.DateOnly))
		}
	}

	// No jobs, no legacy file: twenty years back.
	got, err := sinceDate(ctx, pool, r, ScrapeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if y := time.Now().UTC().Year() - got.Year(); y != firstRunLookback {
		t.Errorf("first run looks back %d years, want %d", y, firstRunLookback)
	}

	// Legacy file, missing or present.
	file := filepath.Join(t.TempDir(), "lastRun-celeiro")
	check(ScrapeOptions{LegacyLastRunFile: file}, got)
	if err := os.WriteFile(file, []byte("2026-10-01\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	check(ScrapeOptions{LegacyLastRunFile: file}, day("2026-10-01"))

	// Jobs win over the file; failed and running jobs don't count, partial ones do.
	for _, j := range []struct{ started, status string }{
		{"2026-10-05 06:30:00+00", StatusOK},
		{"2026-10-06 06:30:00+00", StatusPartial},
		{"2026-10-07 06:30:00+00", StatusFailed},
		{"2026-10-08 06:30:00+00", StatusRunning},
		{"2026-10-09 06:30:00+00", StatusOK}, // another store
	} {
		store := r.Name
		if j.started[:10] == "2026-10-09" {
			store = "Auchan"
		}
		if _, err := pool.Exec(ctx, `INSERT INTO scrape_jobs (store, started_at, status) VALUES ($1, $2, $3)`,
			store, j.started, j.status); err != nil {
			t.Fatal(err)
		}
	}
	check(ScrapeOptions{LegacyLastRunFile: file}, day("2026-10-06"))

	// An explicit date wins over everything.
	since := day("2026-01-01")
	check(ScrapeOptions{Since: &since}, since)
}

func TestWriterSavesAndCounts(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	w := rawlog.NewWriter(pool)
	at := time.Date(2026, 10, 13, 6, 0, 0, 0, time.UTC)
	p := &rawlog.Product{Store: "Celeiro", Name: "Salva 2500Mg", Price: 16.25, Currency: "EUR",
		URL: "https://www.celeiro.pt/587693-salva", ScrapedAt: at}
	for range 2 { // the duplicate is ignored, as before
		if err := w.SaveProduct(p); err != nil {
			t.Fatal(err)
		}
	}
	w.ProductFailed("https://www.celeiro.pt/1-x", errors.New("404"))
	w.SourceFailed("https://www.celeiro.pt/sitemap.xml", errors.New("timeout"))

	if got, want := w.Counts(), (rawlog.Counts{Saved: 2, ProductsFailed: 1, SourcesFailed: 1}); got != want {
		t.Errorf("Counts = %+v, want %+v", got, want)
	}
	var n int
	var price float64
	var scrapedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT count(*), max(price)::float8, max(scraped_at) FROM products`).Scan(&n, &price, &scrapedAt); err != nil {
		t.Fatal(err)
	}
	if n != 1 || price != 16.25 || !scrapedAt.Equal(at) {
		t.Errorf("products holds %d rows, price %v, scraped_at %s; want 1, 16.25, %s", n, price, scrapedAt, at)
	}
}
