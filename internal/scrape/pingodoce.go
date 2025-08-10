package scraper

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/time/rate"
	"price-scraper/internal/config"
	"price-scraper/internal/storage"
)

// PingoDoceScraper scrapes products from Pingo Doce online store
type PingoDoceScraper struct {
	*BaseScraper
}

// NewPingoDoceScraper creates a new Pingo Doce scraper
func NewPingoDoceScraper() *PingoDoceScraper {
	userAgent := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	base := NewBaseScraper("Pingo Doce", "https://www.pingodoce.pt", userAgent)
	
	// Set a longer delay to be more respectful to the server
	base.SetDelay(2 * time.Second)
	
	return &PingoDoceScraper{
		BaseScraper: base,
	}
}

// GTMInfo represents the GTM tracking data that contains product information
type GTMInfo struct {
	Currency string `json:"currency"`
	Value    float64 `json:"value"`
	Items    []struct {
		ItemID       string  `json:"item_id"`
		ItemName     string  `json:"item_name"`
		ItemBrand    string  `json:"item_brand"`
		ItemCategory string  `json:"item_category"`
		Price        float64 `json:"price"`
	} `json:"items"`
}

// ScrapeProducts scrapes products from Pingo Doce using the API endpoint
func (p *PingoDoceScraper) ScrapeProducts() ([]*storage.Product, error) {
	var allProducts []*storage.Product
	
	// Use the callback version and collect products
	callback := func(product *storage.Product) error {
		allProducts = append(allProducts, product)
		return nil
	}
	
	err := p.ScrapeProductsWithCallback(callback)
	if err != nil {
		return nil, err
	}
	
	return allProducts, nil
}

// ScrapeProductsWithCallback scrapes products from Pingo Doce using the API endpoint with streaming storage
func (p *PingoDoceScraper) ScrapeProductsWithCallback(callback ProductCallback) error {
	return p.ScrapeProductsWithCallbackAndConfig(callback, nil)
}

// ScrapeProductsWithCallbackAndConfig scrapes products with configuration support
func (p *PingoDoceScraper) ScrapeProductsWithCallbackAndConfig(callback ProductCallback, cfg *config.Config) error {
	// Use default limits if no config provided
	maxProductsTotal := -1 // No limit by default for Pingo Doce (API-based)
	pageSize := 20
	
	if cfg != nil {
		maxProductsTotal = cfg.MaxProductsTotal
		// For Pingo Doce, we can use MaxProductsPerSitemap as page size
		if cfg.MaxProductsPerSitemap > 0 && cfg.MaxProductsPerSitemap < 100 {
			pageSize = cfg.MaxProductsPerSitemap
		}
	}
	
	// Use the API endpoint to get all products with pagination
	start := 0
	totalProducts := 0
	
	fmt.Printf("Starting to scrape Pingo Doce products using API endpoint (streaming mode)\n")
	
	for {
		// Check total product limit
		if maxProductsTotal > 0 && totalProducts >= maxProductsTotal {
			fmt.Printf("Reached total product limit of %d, stopping\n", maxProductsTotal)
			break
		}
		
		apiURL := fmt.Sprintf("https://www.pingodoce.pt/on/demandware.store/Sites-pingo-doce-Site/default/Search-UpdateGrid?start=%d&sz=%d", start, pageSize)
		
		fmt.Printf("Fetching page: start=%d, size=%d\n", start, pageSize)
		
		doc, err := p.FetchPage(apiURL)
		if err != nil {
			return fmt.Errorf("failed to fetch API page: %w", err)
		}
		
		pageProductCount, err := p.extractProductsFromAPIResponseWithCallback(doc, callback)
		if err != nil {
			return fmt.Errorf("failed to extract products from page: %w", err)
		}
		
		if pageProductCount == 0 {
			fmt.Printf("No products found on this page, stopping pagination\n")
			break
		}
		
		totalProducts += pageProductCount
		fmt.Printf("Processed %d products from this page (total: %d)\n", pageProductCount, totalProducts)
		
		// If we got less than the page size, we've reached the end
		if pageProductCount < pageSize {
			fmt.Printf("Reached end of pagination (got %d < %d products)\n", pageProductCount, pageSize)
			break
		}
		
		start += pageSize
	}
	
	fmt.Printf("Finished scraping Pingo Doce. Total products: %d\n", totalProducts)
	return nil
}

// ExtractProductsFromAPIResponse extracts products from the API response HTML
func (p *PingoDoceScraper) ExtractProductsFromAPIResponse(doc *goquery.Document) []*storage.Product {
	var products []*storage.Product
	
	// Find all product containers
	doc.Find(".product[data-pid]").Each(func(i int, s *goquery.Selection) {
		product := p.extractProductFromElement(s)
		if product != nil {
			products = append(products, product)
		}
	})
	
	return products
}

// extractProductFromElement extracts product information from a product element
func (p *PingoDoceScraper) extractProductFromElement(s *goquery.Selection) *storage.Product {
	// Check if this is a valid product element
	if _, exists := s.Attr("data-pid"); !exists {
		return nil
	}
	
	// Extract name from the product name link
	name := p.CleanText(s.Find(".product-name-link a").Text())
	if name == "" {
		// Fallback to image alt text
		name = p.CleanText(s.Find(".product-tile-component-image").AttrOr("alt", ""))
	}
	if name == "" {
		return nil
	}
	
	// Extract price from the value content attribute or sales text
	var priceValue float64
	var err error
	
	// Try to get price from content attribute first
	if priceContent := s.Find(".value").AttrOr("content", ""); priceContent != "" {
		priceValue, err = strconv.ParseFloat(priceContent, 64)
		if err != nil {
			return nil
		}
	} else {
		// Fallback to parsing from sales text
		priceText := s.Find(".sales").Text()
		priceValue, err = p.ParsePrice(priceText)
		if err != nil {
			return nil
		}
	}
	
	// Extract brand
	brand := p.CleanText(s.Find(".product-brand-name").Text())
	
	// Extract unit information
	unit := p.CleanText(s.Find(".product-unit").Text())
	
	// Extract image URL
	imageURL := s.Find(".product-tile-component-image").AttrOr("src", "")
	if imageURL != "" && !strings.HasPrefix(imageURL, "http") {
		imageURL = p.baseURL + imageURL
	}
	
	// Extract product URL
	productURL := s.Find(".product-name-link a").AttrOr("href", "")
	if productURL != "" && !strings.HasPrefix(productURL, "http") {
		productURL = p.baseURL + productURL
	}
	
	// Extract additional information from GTM data
	var category string
	if gtmData, exists := s.Find(".product-tile-pd").Attr("data-gtm-info"); exists {
		if gtmInfo := p.parseGTMInfo(gtmData); gtmInfo != nil && len(gtmInfo.Items) > 0 {
			category = gtmInfo.Items[0].ItemCategory
			// Verify the price matches
			if gtmInfo.Items[0].Price > 0 {
				priceValue = gtmInfo.Items[0].Price
			}
			// Verify the brand matches
			if gtmInfo.Items[0].ItemBrand != "" && brand == "" {
				brand = gtmInfo.Items[0].ItemBrand
			}
		}
	}
	
	// Create the product
	product := &storage.Product{
		Store:       p.name,
		Name:        name,
		Price:       priceValue,
		Currency:    "EUR",
		URL:         productURL,
		ImageURL:    imageURL,
		Brand:       brand,
		Category:    category,
		Description: p.buildDescription(unit, category),
		ScrapedAt:   p.getCurrentTime(),
	}
	
	return product
}

// parseGTMInfo parses the GTM tracking data JSON
func (p *PingoDoceScraper) parseGTMInfo(gtmData string) *GTMInfo {
	// Decode HTML entities
	gtmData = strings.ReplaceAll(gtmData, "&quot;", "\"")
	gtmData = strings.ReplaceAll(gtmData, "&amp;", "&")
	
	var gtmInfo GTMInfo
	if err := json.Unmarshal([]byte(gtmData), &gtmInfo); err != nil {
		return nil
	}
	
	return &gtmInfo
}

// buildDescription creates a description from available information
func (p *PingoDoceScraper) buildDescription(unit, category string) string {
	var parts []string
	
	if unit != "" {
		parts = append(parts, unit)
	}
	
	if category != "" {
		parts = append(parts, "Categoria: "+category)
	}
	
	return strings.Join(parts, " | ")
}

// extractProductsFromAPIResponseWithCallback extracts products from the API response HTML with callback
func (p *PingoDoceScraper) extractProductsFromAPIResponseWithCallback(doc *goquery.Document, callback ProductCallback) (int, error) {
	productCount := 0
	
	// Find all product containers
	doc.Find(".product[data-pid]").Each(func(i int, s *goquery.Selection) {
		product := p.extractProductFromElement(s)
		if product == nil {
			return
		}
		
		if err := callback(product); err != nil {
			fmt.Printf("Warning: callback failed for product %s: %v\n", product.Name, err)
		} else {
			productCount++
		}
	})
	
	return productCount, nil
}

// getCurrentTime returns the current time (helper for testing)
func (p *PingoDoceScraper) getCurrentTime() time.Time {
	return time.Now()
}

// ScrapeProductsParallel implements parallel scraping for Pingo Doce
func (p *PingoDoceScraper) ScrapeProductsParallel(cfg *config.Config, storage storage.Storage, limiter *rate.Limiter) error {
	ctx := context.Background()
	
	fmt.Printf("Starting to scrape Pingo Doce products using API endpoint (parallel mode with %d workers)\n", cfg.MaxConcurrentWorkers)
	
	// First, determine how many pages we need to fetch
	pageSize := 20
	if cfg.MaxProductsPerSitemap > 0 && cfg.MaxProductsPerSitemap < 100 {
		pageSize = cfg.MaxProductsPerSitemap
	}
	
	// Create worker pool
	workerPool := NewWorkerPool(cfg.MaxConcurrentWorkers, limiter)
	workerPool.Start()
	defer workerPool.Close()
	
	// Counters for tracking progress
	var totalProducts int64
	var processedPages int64
	var mu sync.Mutex
	
	// We'll fetch pages in parallel, but we need to be careful about pagination
	// Start with sequential page discovery, then parallel processing
	start := 0
	pageJobs := make([]string, 0)
	
	// Discover all available pages first (sequential to understand pagination)
	fmt.Printf("Discovering available pages...\n")
	for {
		// Check total product limit
		if cfg.MaxProductsTotal > 0 && int(atomic.LoadInt64(&totalProducts)) >= cfg.MaxProductsTotal {
			break
		}
		
		apiURL := fmt.Sprintf("https://www.pingodoce.pt/on/demandware.store/Sites-pingo-doce-Site/default/Search-UpdateGrid?start=%d&sz=%d", start, pageSize)
		
		// Quick check to see if this page has products
		doc, err := p.FetchPageWithRateLimit(ctx, apiURL, limiter)
		if err != nil {
			fmt.Printf("Failed to fetch page at start=%d: %v\n", start, err)
			break
		}
		
		// Count products on this page
		productCount := 0
		doc.Find(".product[data-pid]").Each(func(i int, s *goquery.Selection) {
			productCount++
		})
		
		if productCount == 0 {
			fmt.Printf("No products found at start=%d, stopping page discovery\n", start)
			break
		}
		
		pageJobs = append(pageJobs, apiURL)
		fmt.Printf("Discovered page %d with %d products (start=%d)\n", len(pageJobs), productCount, start)
		
		// If we got less than the page size, we've reached the end
		if productCount < pageSize {
			fmt.Printf("Reached end of pagination (got %d < %d products)\n", productCount, pageSize)
			break
		}
		
		start += pageSize
		
		// Limit page discovery to prevent runaway
		if len(pageJobs) >= 100 {
			fmt.Printf("Reached page discovery limit of 100 pages\n")
			break
		}
	}
	
	fmt.Printf("Discovered %d pages, processing in parallel...\n", len(pageJobs))
	
	// Process pages in parallel
	var wg sync.WaitGroup
	for i, pageURL := range pageJobs {
		if cfg.MaxProductsTotal > 0 && atomic.LoadInt64(&totalProducts) >= int64(cfg.MaxProductsTotal) {
			fmt.Printf("Reached total product limit of %d, stopping\n", cfg.MaxProductsTotal)
			break
		}
		
		wg.Add(1)
		go func(pageIndex int, url string) {
			defer wg.Done()
			
			fmt.Printf("Processing page %d/%d\n", pageIndex+1, len(pageJobs))
			
			count, err := p.processPageParallel(ctx, url, cfg, storage, workerPool, &totalProducts)
			if err != nil {
				fmt.Printf("Warning: failed to process page %s: %v\n", url, err)
				return
			}
			
			mu.Lock()
			processedPages++
			fmt.Printf("Completed page %d/%d, scraped %d products (total: %d)\n", 
				int(processedPages), len(pageJobs), count, atomic.LoadInt64(&totalProducts))
			mu.Unlock()
		}(i, pageURL)
	}
	
	wg.Wait()
	
	fmt.Printf("Parallel scraping completed. Total products scraped: %d\n", atomic.LoadInt64(&totalProducts))
	return nil
}

// processPageParallel processes a single API page using parallel workers
func (p *PingoDoceScraper) processPageParallel(ctx context.Context, pageURL string, cfg *config.Config, storage storage.Storage, workerPool *WorkerPool, totalProducts *int64) (int, error) {
	// Fetch page with rate limiting
	doc, err := p.FetchPageWithRateLimit(ctx, pageURL, workerPool.limiter)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch page: %w", err)
	}
	
	var productCount int64
	var wg sync.WaitGroup
	
	// Process each product in parallel
	doc.Find(".product[data-pid]").Each(func(i int, s *goquery.Selection) {
		// Check global product limit
		if cfg.MaxProductsTotal > 0 && atomic.LoadInt64(totalProducts) >= int64(cfg.MaxProductsTotal) {
			return
		}
		
		wg.Add(1)
		
		// Submit job to worker pool
		workerPool.Submit(func() error {
			defer wg.Done()
			
			// Check limits again inside worker
			if cfg.MaxProductsTotal > 0 && atomic.LoadInt64(totalProducts) >= int64(cfg.MaxProductsTotal) {
				return nil
			}
			
			product := p.extractProductFromElement(s)
			if product == nil {
				return nil
			}
			
			if err := storage.SaveProduct(product); err != nil {
				fmt.Printf("Warning: failed to save product %s: %v\n", product.Name, err)
				return nil
			}
			
			atomic.AddInt64(&productCount, 1)
			atomic.AddInt64(totalProducts, 1)
			
			// Progress reporting
			if count := atomic.LoadInt64(&productCount); count%10 == 0 {
				fmt.Printf("Scraped %d products from current page...\n", count)
			}
			
			return nil
		})
	})
	
	wg.Wait()
	
	return int(atomic.LoadInt64(&productCount)), nil
}
