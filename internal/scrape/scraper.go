package scrape

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"retail-prices/internal/scrape/config"
	storage "retail-prices/internal/scrape/rawlog"

	"golang.org/x/time/rate"
)

// Scraper interface defines the methods that each store scraper must implement
type Scraper interface {
	// GetName returns the name of the store
	GetName() string

	// ScrapeProducts scrapes products from the store using parallel workers,
	// saving results to storage as they are scraped.
	ScrapeProducts(cfg *config.Config, storage storage.Storage, limiter *rate.Limiter) error

	// SetHTTPClient allows setting a custom HTTP client
	SetHTTPClient(client *http.Client)
}

// Manager manages multiple scrapers and coordinates scraping operations
type Manager struct {
	scrapers map[string]Scraper
	storage  storage.Storage
	client   *http.Client
	config   *config.Config
	limiter  *rate.Limiter
}

// NewManager creates a new scraper manager
func NewManager(storage storage.Storage, cfg *config.Config) *Manager {
	client := &http.Client{
		Timeout: cfg.HTTPTimeout,
	}

	limiter := rate.NewLimiter(rate.Limit(cfg.RequestsPerSecond), 1)

	return &Manager{
		scrapers: make(map[string]Scraper),
		storage:  storage,
		client:   client,
		config:   cfg,
		limiter:  limiter,
	}
}

// RegisterScraper registers a new scraper with the manager
func (m *Manager) RegisterScraper(name string, scraper Scraper) {
	scraper.SetHTTPClient(m.client)
	m.scrapers[name] = scraper
}

// ScrapeStore scrapes a specific store using parallel workers
func (m *Manager) ScrapeStore(storeName string) error {
	scraper, exists := m.scrapers[storeName]
	if !exists {
		return fmt.Errorf("scraper for store '%s' not found", storeName)
	}

	fmt.Printf("Starting to scrape %s (parallel mode with %d workers)...\n",
		scraper.GetName(), m.config.MaxConcurrentWorkers)

	return scraper.ScrapeProducts(m.config, m.storage, m.limiter)
}

// ScrapeAllStores scrapes all registered stores
func (m *Manager) ScrapeAllStores() error {
	for storeName := range m.scrapers {
		if err := m.ScrapeStore(storeName); err != nil {
			fmt.Printf("Error scraping %s: %v\n", storeName, err)
			continue
		}
	}
	return nil
}

// GetAvailableStores returns a list of available store names
func (m *Manager) GetAvailableStores() []string {
	stores := make([]string, 0, len(m.scrapers))
	for name := range m.scrapers {
		stores = append(stores, name)
	}
	return stores
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
