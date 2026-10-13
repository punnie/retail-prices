// Package rawlog writes scraped price observations to the append-only
// `products` table and counts how a scrape went, so the caller can tell a
// clean run from a partial or failed one.
package rawlog

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Product is one price observation of one product page.
type Product struct {
	Store       string
	Name        string
	Price       float64
	Currency    string
	URL         string
	ImageURL    string
	Category    string
	Brand       string
	Description string
	ScrapedAt   time.Time
}

// Sink is what a scraper reports into: every observation it saves, and
// every product or source (sitemap, listing page) it failed to read.
type Sink interface {
	SaveProduct(product *Product) error
	ProductFailed(url string, err error)
	SourceFailed(url string, err error)
}

// Writer is a Sink backed by the products table. It is safe for concurrent
// use by the scraper's workers.
type Writer struct {
	pool *pgxpool.Pool

	saved, productsFailed, sourcesFailed atomic.Int64
}

// NewWriter returns a Writer that inserts into pool's products table.
func NewWriter(pool *pgxpool.Pool) *Writer {
	return &Writer{pool: pool}
}

// SaveProduct records one observation. Duplicates of an existing
// (store, name, price, scraped_at) row are ignored, as they always were.
func (w *Writer) SaveProduct(p *Product) error {
	_, err := w.pool.Exec(context.Background(), `
		INSERT INTO products
			(store, name, price, currency, url, image_url, category, brand, description, scraped_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (store, name, price, scraped_at) DO NOTHING`,
		p.Store, p.Name, p.Price, p.Currency, p.URL, p.ImageURL, p.Category, p.Brand, p.Description, p.ScrapedAt)
	if err != nil {
		w.productsFailed.Add(1)
		return fmt.Errorf("saving %s: %w", p.Name, err)
	}
	w.saved.Add(1)
	return nil
}

// ProductFailed records a product page that could not be scraped.
func (w *Writer) ProductFailed(url string, err error) {
	w.productsFailed.Add(1)
	fmt.Printf("Warning: failed to scrape product %s: %v\n", url, err)
}

// SourceFailed records a sitemap or listing page that could not be read, so
// every product it would have listed was missed.
func (w *Writer) SourceFailed(url string, err error) {
	w.sourcesFailed.Add(1)
	fmt.Printf("Warning: failed to process %s: %v\n", url, err)
}

// Counts is a summary of what a Writer has seen so far.
type Counts struct {
	Saved, ProductsFailed, SourcesFailed int64
}

// Counts returns the totals so far.
func (w *Writer) Counts() Counts {
	return Counts{
		Saved:          w.saved.Load(),
		ProductsFailed: w.productsFailed.Load(),
		SourcesFailed:  w.sourcesFailed.Load(),
	}
}
