package scrape

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"retail-prices/internal/retailer"
	"retail-prices/internal/scrape/config"
	"retail-prices/internal/scrape/rawlog"

	"golang.org/x/time/rate"
)

// Scraper interface defines the methods that each store scraper must implement
type Scraper interface {
	// GetName returns the name of the store
	GetName() string

	// ScrapeProducts scrapes products from the store using parallel workers,
	// saving results to storage as they are scraped.
	ScrapeProducts(cfg *config.Config, sink rawlog.Sink, limiter *rate.Limiter) error

	// SetHTTPClient allows setting a custom HTTP client
	SetHTTPClient(client *http.Client)
}

// constructors maps each retailer id (internal/retailer) to its scraper.
var constructors = map[string]func() Scraper{
	"continente": func() Scraper { return NewContinenteScraper() },
	"pingodoce":  func() Scraper { return NewPingoDoceScraper() },
	"auchan":     func() Scraper { return NewAuchanScraper() },
	"celeiro":    func() Scraper { return NewCeleiroScraper() },
}

// Scrape runs the scraper for retailer r once, reporting every observation
// and failure into sink. It returns an error only when the scrape could not
// get going at all (for example, the sitemap index was unreachable);
// individual failures are counted by the sink.
func Scrape(r *retailer.Retailer, cfg *config.Config, sink rawlog.Sink) error {
	newScraper, ok := constructors[r.ID]
	if !ok {
		return fmt.Errorf("no scraper registered for retailer %q", r.ID)
	}
	s := newScraper()
	s.SetHTTPClient(&http.Client{Timeout: cfg.HTTPTimeout})
	limiter := rate.NewLimiter(rate.Limit(cfg.RequestsPerSecond), 1)

	fmt.Printf("Starting to scrape %s (parallel mode with %d workers)...\n",
		s.GetName(), cfg.MaxConcurrentWorkers)

	return s.ScrapeProducts(cfg, sink, limiter)
}

// WorkerPool manages a pool of workers for parallel scraping
type WorkerPool struct {
	workers   int
	jobs      chan func() error
	wg        sync.WaitGroup
	ctx       context.Context
	cancel    context.CancelFunc
	limiter   *rate.Limiter
	errorChan chan error
	errorWg   sync.WaitGroup
}

// NewWorkerPool creates a new worker pool
func NewWorkerPool(workers int, limiter *rate.Limiter) *WorkerPool {
	ctx, cancel := context.WithCancel(context.Background())

	return &WorkerPool{
		workers:   workers,
		jobs:      make(chan func() error, workers*10),
		ctx:       ctx,
		cancel:    cancel,
		limiter:   limiter,
		errorChan: make(chan error, workers*10),
	}
}

// Start starts the worker pool
func (wp *WorkerPool) Start() {
	wp.errorWg.Add(1)
	go func() {
		defer wp.errorWg.Done()
		for err := range wp.errorChan {
			if err != nil {
				fmt.Printf("Worker error: %v\n", err)
			}
		}
	}()

	for i := 0; i < wp.workers; i++ {
		wp.wg.Add(1)
		go wp.worker(i)
	}
}

// worker is the worker goroutine
func (wp *WorkerPool) worker(id int) {
	defer wp.wg.Done()

	for {
		select {
		case job, ok := <-wp.jobs:
			if !ok {
				return
			}

			if err := job(); err != nil {
				select {
				case wp.errorChan <- err:
				default:
					fmt.Printf("Worker %d: error channel full, dropping error: %v\n", id, err)
				}
			}

		case <-wp.ctx.Done():
			return
		}
	}
}

// Submit submits a job to the worker pool
func (wp *WorkerPool) Submit(job func() error) {
	select {
	case wp.jobs <- job:
	case <-wp.ctx.Done():
	}
}

// Close closes the worker pool and waits for all workers to finish
func (wp *WorkerPool) Close() {
	close(wp.jobs)
	wp.wg.Wait()
	wp.cancel()
	close(wp.errorChan)
	wp.errorWg.Wait()
}
