package scrape

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"retail-prices/internal/scrape/config"
	storage "retail-prices/internal/scrape/rawlog"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/time/rate"
)

// ContinenteScraper scrapes products from Continente online store using sitemap-based approach
type ContinenteScraper struct {
	*BaseScraper
	sitemapIndexURL string
}

// NewContinenteScraper creates a new Continente scraper
func NewContinenteScraper() *ContinenteScraper {
	userAgent := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	base := NewBaseScraper("Continente", "https://www.continente.pt", userAgent)

	return &ContinenteScraper{
		BaseScraper:     base,
		sitemapIndexURL: "https://www.continente.pt/sitemap_index.xml",
	}
}

// ScrapeProducts implements parallel scraping for Continente
func (c *ContinenteScraper) ScrapeProducts(cfg *config.Config, store storage.Storage, limiter *rate.Limiter) error {
	ctx := context.Background()

	fmt.Printf("Fetching Continente sitemap index from: %s\n", c.sitemapIndexURL)

	// Fetch and parse sitemap index
	xmlData, err := c.FetchXML(ctx, c.sitemapIndexURL, limiter)
	if err != nil {
		return fmt.Errorf("failed to fetch sitemap index: %w", err)
	}

	sitemapIndex, err := c.ParseSitemapIndex(xmlData)
	if err != nil {
		return fmt.Errorf("failed to parse sitemap index: %w", err)
	}

	// Filter product sitemaps
	productSitemaps := c.filterProductSitemaps(sitemapIndex.Sitemaps)
	fmt.Printf("Found %d product sitemaps\n", len(productSitemaps))

	// Apply sitemap limit
	if cfg.MaxSitemaps > 0 && len(productSitemaps) > cfg.MaxSitemaps {
		productSitemaps = productSitemaps[:cfg.MaxSitemaps]
		fmt.Printf("Limited to %d sitemaps as per configuration\n", cfg.MaxSitemaps)
	}

	// Create worker pool
	workerPool := NewWorkerPool(cfg.MaxConcurrentWorkers, limiter)
	workerPool.Start()
	defer workerPool.Close()

	// Counters for tracking progress
	var totalProducts int64
	var processedSitemaps int64
	var mu sync.Mutex

	// Process sitemaps in parallel
	var wg sync.WaitGroup
	for i, sitemap := range productSitemaps {
		if cfg.MaxProductsTotal > 0 && atomic.LoadInt64(&totalProducts) >= int64(cfg.MaxProductsTotal) {
			fmt.Printf("Reached total product limit of %d, stopping\n", cfg.MaxProductsTotal)
			break
		}

		wg.Add(1)
		go func(sitemapIndex int, sitemapURL string) {
			defer wg.Done()

			fmt.Printf("Processing sitemap %d/%d: %s\n", sitemapIndex+1, len(productSitemaps), sitemapURL)

			count, err := c.processSitemap(ctx, sitemapURL, cfg, store, workerPool, &totalProducts)
			if err != nil {
				fmt.Printf("Warning: failed to process sitemap %s: %v\n", sitemapURL, err)
				return
			}

			mu.Lock()
			processedSitemaps++
			fmt.Printf("Completed sitemap %d/%d, scraped %d products (total: %d)\n",
				int(processedSitemaps), len(productSitemaps), count, atomic.LoadInt64(&totalProducts))
			mu.Unlock()
		}(i, sitemap.Loc)
	}

	wg.Wait()

	fmt.Printf("Parallel scraping completed. Total products scraped: %d\n", atomic.LoadInt64(&totalProducts))
	return nil
}

// filterProductSitemaps filters sitemaps to only include product sitemaps
func (c *ContinenteScraper) filterProductSitemaps(sitemaps []Sitemap) []Sitemap {
	var productSitemaps []Sitemap

	for _, sitemap := range sitemaps {
		if strings.Contains(sitemap.Loc, "product") {
			productSitemaps = append(productSitemaps, sitemap)
		}
	}

	return productSitemaps
}

// processSitemap processes a single sitemap using parallel workers
func (c *ContinenteScraper) processSitemap(ctx context.Context, sitemapURL string, cfg *config.Config, store storage.Storage, workerPool *WorkerPool, totalProducts *int64) (int, error) {
	// Fetch sitemap XML with rate limiting
	xmlData, err := c.FetchXML(ctx, sitemapURL, workerPool.limiter)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch sitemap: %w", err)
	}

	// Parse sitemap
	urlSet, err := c.ParseSitemap(xmlData)
	if err != nil {
		return 0, fmt.Errorf("failed to parse sitemap: %w", err)
	}

	fmt.Printf("Found %d product URLs in sitemap\n", len(urlSet.URLs))

	// Apply date filtering
	filteredURLs := c.FilterURLsBySinceDate(urlSet.URLs, cfg.SinceDate)
	if cfg.SinceDate != nil {
		fmt.Printf("After date filtering (since %s): %d URLs remain\n", cfg.SinceDate.Format("2006-01-02"), len(filteredURLs))
	}

	// Apply per-sitemap limit
	maxProducts := len(filteredURLs)
	if cfg.MaxProductsPerSitemap > 0 && maxProducts > cfg.MaxProductsPerSitemap {
		maxProducts = cfg.MaxProductsPerSitemap
	}

	fmt.Printf("Processing %d product URLs from sitemap (limited from %d)\n", maxProducts, len(filteredURLs))

	var productCount int64
	var wg sync.WaitGroup

	// Process product URLs in parallel
	for i := 0; i < maxProducts; i++ {
		// Check global product limit
		if cfg.MaxProductsTotal > 0 && atomic.LoadInt64(totalProducts) >= int64(cfg.MaxProductsTotal) {
			break
		}

		productURL := filteredURLs[i].Loc
		wg.Add(1)

		workerPool.Submit(func() error {
			defer wg.Done()

			// Check limits again inside worker
			if cfg.MaxProductsTotal > 0 && atomic.LoadInt64(totalProducts) >= int64(cfg.MaxProductsTotal) {
				return nil
			}

			product, err := c.scrapeProductPage(ctx, productURL, workerPool.limiter)
			if err != nil {
				fmt.Printf("Warning: failed to scrape product %s: %v\n", productURL, err)
				return nil
			}

			if product != nil {
				if err := store.SaveProduct(product); err != nil {
					fmt.Printf("Warning: failed to save product %s: %v\n", product.Name, err)
					return nil
				}

				atomic.AddInt64(&productCount, 1)
				atomic.AddInt64(totalProducts, 1)

				if count := atomic.LoadInt64(&productCount); count%10 == 0 {
					fmt.Printf("Scraped %d products from current sitemap...\n", count)
				}
			}

			return nil
		})
	}

	wg.Wait()

	return int(atomic.LoadInt64(&productCount)), nil
}

// scrapeProductPage scrapes a single product page with rate limiting
func (c *ContinenteScraper) scrapeProductPage(ctx context.Context, productURL string, limiter *rate.Limiter) (*storage.Product, error) {
	doc, err := c.FetchPage(ctx, productURL, limiter)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch product page: %w", err)
	}

	return c.extractProductFromPage(doc, productURL)
}

// extractProductFromPage extracts product information from a product page
func (c *ContinenteScraper) extractProductFromPage(doc *goquery.Document, productURL string) (*storage.Product, error) {
	name := c.FindProductName(doc)
	if name == "" {
		return nil, fmt.Errorf("could not find product name")
	}

	priceStr := c.findProductPrice(doc)
	if priceStr == "" {
		return nil, fmt.Errorf("could not find product price")
	}

	product, err := c.CreateProduct(name, priceStr, productURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create product: %w", err)
	}

	if imageURL := c.findProductImage(doc); imageURL != "" {
		product.ImageURL = imageURL
	}

	if brand := c.findProductBrand(doc); brand != "" {
		product.Brand = brand
	}

	if category := c.findProductCategory(doc); category != "" {
		product.Category = category
	}

	if unit := c.findProductUnit(doc); unit != "" {
		if product.Description != "" {
			product.Description += " | " + unit
		} else {
			product.Description = unit
		}
	}

	if description := c.findProductDescription(doc); description != "" {
		if product.Description != "" {
			product.Description += " | " + description
		} else {
			product.Description = description
		}
	}

	return product, nil
}

// FindProductName tries to find the product name on the page (exported for testing)
func (c *ContinenteScraper) FindProductName(doc *goquery.Document) string {
	// First try JSON-LD structured data (most reliable)
	if name := c.ExtractFromJSONLD(doc, "name"); name != "" {
		return c.CleanText(name)
	}

	nameSelectors := []string{
		"h1.product-name",
		".ct-pdp--name h1.product-name",
		".product-name-details h1.product-name",
		"h1.pwc-h3.col-h3.product-name",
		"h1[data-testid='pdp-product-name']",
		".product-name h1",
		".product-title h1",
		"h1.title",
		"h1",
		".pdp-product-name",
		"[data-testid*='product-name']",
		"[data-testid*='product-title']",
	}

	for _, selector := range nameSelectors {
		if name := doc.Find(selector).First().Text(); name != "" {
			return c.CleanText(name)
		}
	}

	// Fallback: try meta tags
	if content, exists := doc.Find("meta[property='og:title']").Attr("content"); exists && content != "" {
		title := c.CleanText(content)
		if idx := strings.Index(title, " - Continente"); idx > 0 {
			title = title[:idx]
		}
		if idx := strings.Index(title, " | Continente"); idx > 0 {
			title = title[:idx]
		}
		return title
	}

	return ""
}

// findProductPrice tries to find the product price on the page
func (c *ContinenteScraper) findProductPrice(doc *goquery.Document) string {
	// First try JSON-LD structured data
	if price := c.ExtractPriceFromJSONLD(doc); price != "" {
		return price
	}

	priceSelectors := []string{
		".price .value",
		".sales .value",
		".js-product-price .value",
		".ct-price-formatted",
		".prices .value",
		".price-current",
		".current-price",
		"[data-testid*='price'] .value",
		"[data-testid*='current-price']",
		".pdp-price",
		".product-price",
		".price",
	}

	for _, selector := range priceSelectors {
		if price := doc.Find(selector).First().Text(); price != "" && strings.Contains(price, "€") {
			return price
		}
	}

	// Fallback: look for any element containing € symbol
	var foundPrice string
	doc.Find("*").Each(func(i int, s *goquery.Selection) {
		if foundPrice != "" {
			return
		}
		text := strings.TrimSpace(s.Text())
		if strings.Contains(text, "€") && len(text) < 50 && len(text) > 2 {
			if strings.Contains(text, ",") || strings.Contains(text, ".") {
				foundPrice = text
			}
		}
	})

	return foundPrice
}

// findProductImage tries to find the product image URL
func (c *ContinenteScraper) findProductImage(doc *goquery.Document) string {
	imageSelectors := []string{
		".ct-product-image",
		".pdp-carousel-item img",
		".product-images img",
		".pdp-img-container img",
		".pdp-product-image img",
		".product-image img",
		".product-photo img",
		"[data-testid*='product-image'] img",
		".main-image img",
	}

	for _, selector := range imageSelectors {
		if src, exists := doc.Find(selector).First().Attr("src"); exists && src != "" {
			if strings.HasPrefix(src, "http") {
				return src
			} else if strings.HasPrefix(src, "/") {
				return c.baseURL + src
			}
		}
	}

	if content, exists := doc.Find("meta[property='og:image']").Attr("content"); exists && content != "" {
		return content
	}

	return ""
}

// findProductBrand tries to find the product brand
func (c *ContinenteScraper) findProductBrand(doc *goquery.Document) string {
	brandSelectors := []string{
		".ct-pdp--brand",
		".col-pdp--brand",
		"a.ct-pdp--brand",
		".pdp-product-brand",
		".product-brand",
		".brand-name",
		"[data-testid*='brand']",
		".manufacturer",
	}

	for _, selector := range brandSelectors {
		if brand := doc.Find(selector).First().Text(); brand != "" {
			return c.CleanText(brand)
		}
	}

	return ""
}

// findProductUnit tries to find the product unit information
func (c *ContinenteScraper) findProductUnit(doc *goquery.Document) string {
	unitSelectors := []string{
		".ct-pdp--unit",
		".col-pdp--unit",
		".product-unit",
		".unit-info",
		".pwc-m-unit",
		"[data-testid*='unit']",
	}

	for _, selector := range unitSelectors {
		if unit := doc.Find(selector).First().Text(); unit != "" {
			return c.CleanText(unit)
		}
	}

	return ""
}

// findProductDescription tries to find the product short description
func (c *ContinenteScraper) findProductDescription(doc *goquery.Document) string {
	descriptionSelectors := []string{
		".ct-pdp--short-description",
		".col-pdp--short-description",
		".product-description",
		".short-description",
		".product-summary",
		"[data-testid*='description']",
		".product-details p",
	}

	for _, selector := range descriptionSelectors {
		if description := doc.Find(selector).First().Text(); description != "" {
			return c.CleanText(description)
		}
	}

	return ""
}

// findProductCategory tries to find the product category
func (c *ContinenteScraper) findProductCategory(doc *goquery.Document) string {
	categorySelectors := []string{
		".breadcrumb a:last-child",
		".breadcrumbs a:last-child",
		".category-name",
		"[data-testid*='category']",
	}

	for _, selector := range categorySelectors {
		if category := doc.Find(selector).First().Text(); category != "" {
			return c.CleanText(category)
		}
	}

	return ""
}
