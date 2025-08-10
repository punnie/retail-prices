package scraper

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"golang.org/x/time/rate"
	"price-scraper/internal/config"
	"price-scraper/internal/storage"
)

// ProductCallback is a function type for handling products as they are scraped
type ProductCallback func(*storage.Product) error

// Scraper interface defines the methods that each store scraper must implement
type Scraper interface {
	// GetName returns the name of the store
	GetName() string
	
	// ScrapeProducts scrapes products from the store using seed URLs/sitemaps
	// Deprecated: Use ScrapeProductsWithCallback for streaming storage
	ScrapeProducts() ([]*storage.Product, error)
	
	// ScrapeProductsWithCallback scrapes products from the store and calls the callback for each product
	// This allows for streaming storage and reduces memory usage
	ScrapeProductsWithCallback(callback ProductCallback) error
	
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

	// Create rate limiter
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

// ScrapeStore scrapes a specific store and saves the results
// Deprecated: Use ScrapeStoreStreaming for better memory usage and crash resilience
func (m *Manager) ScrapeStore(storeName string) error {
	scraper, exists := m.scrapers[storeName]
	if !exists {
		return fmt.Errorf("scraper for store '%s' not found", storeName)
	}

	fmt.Printf("Starting to scrape %s...\n", scraper.GetName())
	
	products, err := scraper.ScrapeProducts()
	if err != nil {
		return fmt.Errorf("failed to scrape %s: %w", scraper.GetName(), err)
	}

	fmt.Printf("Found %d products from %s\n", len(products), scraper.GetName())

	// Save products to storage
	for _, product := range products {
		if err := m.storage.SaveProduct(product); err != nil {
			fmt.Printf("Warning: failed to save product %s: %v\n", product.Name, err)
		}
	}

	return nil
}

// ScrapeStoreStreaming scrapes a specific store and saves results as they are scraped
func (m *Manager) ScrapeStoreStreaming(storeName string) error {
	scraper, exists := m.scrapers[storeName]
	if !exists {
		return fmt.Errorf("scraper for store '%s' not found", storeName)
	}

	fmt.Printf("Starting to scrape %s (streaming mode)...\n", scraper.GetName())
	
	productCount := 0
	callback := func(product *storage.Product) error {
		if err := m.storage.SaveProduct(product); err != nil {
			fmt.Printf("Warning: failed to save product %s: %v\n", product.Name, err)
			return err
		}
		productCount++
		if productCount%10 == 0 {
			fmt.Printf("Saved %d products from %s...\n", productCount, scraper.GetName())
		}
		return nil
	}

	err := scraper.ScrapeProductsWithCallback(callback)
	if err != nil {
		return fmt.Errorf("failed to scrape %s: %w", scraper.GetName(), err)
	}

	fmt.Printf("Successfully scraped and saved %d products from %s\n", productCount, scraper.GetName())
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

// ScrapeAllStores scrapes all registered stores
// Deprecated: Use ScrapeAllStoresStreaming for better memory usage and crash resilience
func (m *Manager) ScrapeAllStores() error {
	for storeName := range m.scrapers {
		if err := m.ScrapeStore(storeName); err != nil {
			fmt.Printf("Error scraping %s: %v\n", storeName, err)
			continue
		}
	}
	return nil
}

// ScrapeAllStoresStreaming scrapes all registered stores using streaming storage
func (m *Manager) ScrapeAllStoresStreaming() error {
	for storeName := range m.scrapers {
		if err := m.ScrapeStoreStreaming(storeName); err != nil {
			fmt.Printf("Error scraping %s: %v\n", storeName, err)
			continue
		}
	}
	return nil
}

// ScrapeStoreParallel scrapes a specific store using parallel workers
func (m *Manager) ScrapeStoreParallel(storeName string) error {
	scraper, exists := m.scrapers[storeName]
	if !exists {
		return fmt.Errorf("scraper for store '%s' not found", storeName)
	}

	// Check if scraper supports parallel scraping
	if parallelScraper, ok := scraper.(ParallelScraper); ok {
		fmt.Printf("Starting to scrape %s (parallel mode with %d workers)...\n", 
			scraper.GetName(), m.config.MaxConcurrentWorkers)
		
		return parallelScraper.ScrapeProductsParallel(m.config, m.storage, m.limiter)
	}

	// Fallback to streaming mode
	fmt.Printf("Scraper %s doesn't support parallel mode, falling back to streaming...\n", scraper.GetName())
	return m.ScrapeStoreStreaming(storeName)
}

// ParallelScraper interface for scrapers that support parallel processing
type ParallelScraper interface {
	Scraper
	ScrapeProductsParallel(cfg *config.Config, storage storage.Storage, limiter *rate.Limiter) error
}

// WorkerPool manages a pool of workers for parallel scraping
type WorkerPool struct {
	workers   int
	jobs      chan func() error
	results   chan error
	wg        sync.WaitGroup
	ctx       context.Context
	cancel    context.CancelFunc
	limiter   *rate.Limiter
}

// NewWorkerPool creates a new worker pool
func NewWorkerPool(workers int, limiter *rate.Limiter) *WorkerPool {
	ctx, cancel := context.WithCancel(context.Background())
	
	return &WorkerPool{
		workers: workers,
		jobs:    make(chan func() error, workers*2), // Buffer for jobs
		results: make(chan error, workers*2),
		ctx:     ctx,
		cancel:  cancel,
		limiter: limiter,
	}
}

// Start starts the worker pool
func (wp *WorkerPool) Start() {
	for i := 0; i < wp.workers; i++ {
		wp.wg.Add(1)
		go wp.worker()
	}
}

// worker is the worker goroutine
func (wp *WorkerPool) worker() {
	defer wp.wg.Done()
	
	for {
		select {
		case job, ok := <-wp.jobs:
			if !ok {
				return
			}
			
			// Rate limit the request
			if err := wp.limiter.Wait(wp.ctx); err != nil {
				wp.results <- err
				continue
			}
			
			// Execute the job
			err := job()
			wp.results <- err
			
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
	close(wp.results)
}

// Results returns the results channel
func (wp *WorkerPool) Results() <-chan error {
	return wp.results
}
