package scraper

import (
	"fmt"
	"net/http"
	"time"

	"price-scraper/internal/storage"
)

// Scraper interface defines the methods that each store scraper must implement
type Scraper interface {
	// GetName returns the name of the store
	GetName() string
	
	// ScrapeProducts scrapes products from the store
	// searchTerm can be empty to scrape all products or a specific term to search for
	ScrapeProducts(searchTerm string) ([]*storage.Product, error)
	
	// SetHTTPClient allows setting a custom HTTP client
	SetHTTPClient(client *http.Client)
}

// Manager manages multiple scrapers and coordinates scraping operations
type Manager struct {
	scrapers map[string]Scraper
	storage  storage.Storage
	client   *http.Client
}

// NewManager creates a new scraper manager
func NewManager(storage storage.Storage) *Manager {
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	return &Manager{
		scrapers: make(map[string]Scraper),
		storage:  storage,
		client:   client,
	}
}

// RegisterScraper registers a new scraper with the manager
func (m *Manager) RegisterScraper(name string, scraper Scraper) {
	scraper.SetHTTPClient(m.client)
	m.scrapers[name] = scraper
}

// ScrapeStore scrapes a specific store and saves the results
func (m *Manager) ScrapeStore(storeName, searchTerm string) error {
	scraper, exists := m.scrapers[storeName]
	if !exists {
		return fmt.Errorf("scraper for store '%s' not found", storeName)
	}

	fmt.Printf("Starting to scrape %s...\n", scraper.GetName())
	
	products, err := scraper.ScrapeProducts(searchTerm)
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

// GetAvailableStores returns a list of available store names
func (m *Manager) GetAvailableStores() []string {
	stores := make([]string, 0, len(m.scrapers))
	for name := range m.scrapers {
		stores = append(stores, name)
	}
	return stores
}

// ScrapeAllStores scrapes all registered stores
func (m *Manager) ScrapeAllStores(searchTerm string) error {
	for storeName := range m.scrapers {
		if err := m.ScrapeStore(storeName, searchTerm); err != nil {
			fmt.Printf("Error scraping %s: %v\n", storeName, err)
			continue
		}
	}
	return nil
}
