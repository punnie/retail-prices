package scrape

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"retail-prices/internal/scrape/config"
	"retail-prices/internal/scrape/rawlog"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/time/rate"
)

// PingoDoceScraper scrapes products from Pingo Doce online store
type PingoDoceScraper struct {
	*BaseScraper
}

// NewPingoDoceScraper creates a new Pingo Doce scraper
func NewPingoDoceScraper() *PingoDoceScraper {
	userAgent := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	base := NewBaseScraper("Pingo Doce", "https://www.pingodoce.pt", userAgent)

	return &PingoDoceScraper{
		BaseScraper: base,
	}
}

// GTMInfo represents the GTM tracking data that contains product information
type GTMInfo struct {
	Currency string  `json:"currency"`
	Value    float64 `json:"value"`
	Items    []struct {
		ItemID       string  `json:"item_id"`
		ItemName     string  `json:"item_name"`
		ItemBrand    string  `json:"item_brand"`
		ItemCategory string  `json:"item_category"`
		Price        float64 `json:"price"`
	} `json:"items"`
}

// ScrapeProducts implements parallel scraping for Pingo Doce
func (p *PingoDoceScraper) ScrapeProducts(cfg *config.Config, store rawlog.Sink, limiter *rate.Limiter) error {
	ctx := context.Background()

	fmt.Printf("Starting to scrape Pingo Doce products using API endpoint (parallel mode with %d workers)\n", cfg.MaxConcurrentWorkers)

	// Determine page size
	pageSize := cfg.PageSize
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

	// Discover all available pages first (sequential to understand pagination)
	start := 0
	pageJobs := make([]string, 0)

	fmt.Printf("Discovering available pages...\n")
	for {
		if cfg.MaxProductsTotal > 0 && int(atomic.LoadInt64(&totalProducts)) >= cfg.MaxProductsTotal {
			break
		}

		apiURL := fmt.Sprintf("https://www.pingodoce.pt/on/demandware.store/Sites-pingo-doce-Site/default/Search-UpdateGrid?start=%d&sz=%d", start, pageSize)

		doc, err := p.FetchPage(ctx, apiURL, limiter)
		if err != nil {
			store.SourceFailed(apiURL, err)
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

		if productCount < pageSize {
			fmt.Printf("Reached end of pagination (got %d < %d products)\n", productCount, pageSize)
			break
		}

		start += pageSize

		if len(pageJobs) >= cfg.PageDiscoverySize {
			fmt.Printf("Reached page discovery limit of %d pages\n", cfg.PageDiscoverySize)
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

			count, err := p.processPageParallel(ctx, url, cfg, store, workerPool, &totalProducts)
			if err != nil {
				store.SourceFailed(url, err)
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
func (p *PingoDoceScraper) processPageParallel(ctx context.Context, pageURL string, cfg *config.Config, store rawlog.Sink, workerPool *WorkerPool, totalProducts *int64) (int, error) {
	// Fetch page with rate limiting
	doc, err := p.FetchPage(ctx, pageURL, workerPool.limiter)
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

		workerPool.Submit(func() error {
			defer wg.Done()

			if cfg.MaxProductsTotal > 0 && atomic.LoadInt64(totalProducts) >= int64(cfg.MaxProductsTotal) {
				return nil
			}

			product := p.extractProductFromElement(s)
			if product == nil {
				return nil
			}

			if err := store.SaveProduct(product); err != nil {
				fmt.Printf("Warning: failed to save product %s: %v\n", product.Name, err)
				return nil
			}

			atomic.AddInt64(&productCount, 1)
			atomic.AddInt64(totalProducts, 1)

			if count := atomic.LoadInt64(&productCount); count%10 == 0 {
				fmt.Printf("Scraped %d products from current page...\n", count)
			}

			return nil
		})
	})

	wg.Wait()

	return int(atomic.LoadInt64(&productCount)), nil
}

// extractProductFromElement extracts product information from a product element
func (p *PingoDoceScraper) extractProductFromElement(s *goquery.Selection) *rawlog.Product {
	if _, exists := s.Attr("data-pid"); !exists {
		return nil
	}

	name := p.CleanText(s.Find(".product-name-link a").Text())
	if name == "" {
		name = p.CleanText(s.Find(".product-tile-component-image").AttrOr("alt", ""))
	}
	if name == "" {
		return nil
	}

	var priceValue float64
	var err error

	if priceContent := s.Find(".value").AttrOr("content", ""); priceContent != "" {
		priceValue, err = strconv.ParseFloat(priceContent, 64)
		if err != nil {
			return nil
		}
	} else {
		priceText := s.Find(".sales").Text()
		priceValue, err = p.ParsePrice(priceText)
		if err != nil {
			return nil
		}
	}

	brand := p.CleanText(s.Find(".product-brand-name").Text())
	unit := p.CleanText(s.Find(".product-unit").Text())

	imageURL := s.Find(".product-tile-component-image").AttrOr("src", "")
	if imageURL != "" && !strings.HasPrefix(imageURL, "http") {
		imageURL = p.baseURL + imageURL
	}

	productURL := s.Find(".product-name-link a").AttrOr("href", "")
	if productURL != "" && !strings.HasPrefix(productURL, "http") {
		productURL = p.baseURL + productURL
	}

	var category string
	if gtmData, exists := s.Find(".product-tile-pd").Attr("data-gtm-info"); exists {
		if gtmInfo := p.parseGTMInfo(gtmData); gtmInfo != nil && len(gtmInfo.Items) > 0 {
			category = gtmInfo.Items[0].ItemCategory
			if gtmInfo.Items[0].Price > 0 {
				priceValue = gtmInfo.Items[0].Price
			}
			if gtmInfo.Items[0].ItemBrand != "" && brand == "" {
				brand = gtmInfo.Items[0].ItemBrand
			}
		}
	}

	product := &rawlog.Product{
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

// getCurrentTime returns the current time (helper for testing)
func (p *PingoDoceScraper) getCurrentTime() time.Time {
	return time.Now()
}
