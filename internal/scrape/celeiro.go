package scraper

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"price-scraper/internal/config"
	"price-scraper/internal/storage"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/time/rate"
)

// CeleiroScraper scrapes products from Celeiro online store using sitemap-based approach
type CeleiroScraper struct {
	*BaseScraper
	sitemapIndexURL string
}

// NewCeleiroScraper creates a new Celeiro scraper
func NewCeleiroScraper() *CeleiroScraper {
	userAgent := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	base := NewBaseScraper("Celeiro", "https://www.celeiro.pt", userAgent)

	return &CeleiroScraper{
		BaseScraper:     base,
		sitemapIndexURL: "https://www.celeiro.pt/sitemap.xml",
	}
}

// productURLPattern matches Celeiro product URLs which start with a numeric ID
// e.g. https://www.celeiro.pt/62-aloe-vera-pau-d-arco-1-litros-ltr-fharmonat
var productURLPattern = regexp.MustCompile(`^https://www\.celeiro\.pt/\d+-`)

// ScrapeProducts implements parallel scraping for Celeiro
func (c *CeleiroScraper) ScrapeProducts(cfg *config.Config, store storage.Storage, limiter *rate.Limiter) error {
	ctx := context.Background()

	fmt.Printf("Fetching Celeiro sitemap index from: %s\n", c.sitemapIndexURL)

	// Fetch and parse sitemap index
	xmlData, err := c.FetchXML(ctx, c.sitemapIndexURL, limiter)
	if err != nil {
		return fmt.Errorf("failed to fetch sitemap index: %w", err)
	}

	sitemapIndex, err := c.ParseSitemapIndex(xmlData)
	if err != nil {
		return fmt.Errorf("failed to parse sitemap index: %w", err)
	}

	// Celeiro sitemaps are not prefixed with "product" — all child sitemaps contain
	// a mix of product and non-product URLs, so we process all of them and filter
	// at the URL level instead.
	allSitemaps := sitemapIndex.Sitemaps
	fmt.Printf("Found %d sitemaps\n", len(allSitemaps))

	// Apply sitemap limit
	if cfg.MaxSitemaps > 0 && len(allSitemaps) > cfg.MaxSitemaps {
		allSitemaps = allSitemaps[:cfg.MaxSitemaps]
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
	for i, sitemap := range allSitemaps {
		if cfg.MaxProductsTotal > 0 && atomic.LoadInt64(&totalProducts) >= int64(cfg.MaxProductsTotal) {
			fmt.Printf("Reached total product limit of %d, stopping\n", cfg.MaxProductsTotal)
			break
		}

		wg.Add(1)
		go func(sitemapIndex int, sitemapURL string) {
			defer wg.Done()

			fmt.Printf("Processing sitemap %d/%d: %s\n", sitemapIndex+1, len(allSitemaps), sitemapURL)

			count, err := c.processSitemap(ctx, sitemapURL, cfg, store, workerPool, &totalProducts)
			if err != nil {
				fmt.Printf("Warning: failed to process sitemap %s: %v\n", sitemapURL, err)
				return
			}

			mu.Lock()
			processedSitemaps++
			fmt.Printf("Completed sitemap %d/%d, scraped %d products (total: %d)\n",
				int(processedSitemaps), len(allSitemaps), count, atomic.LoadInt64(&totalProducts))
			mu.Unlock()
		}(i, sitemap.Loc)
	}

	wg.Wait()

	fmt.Printf("Parallel scraping completed. Total products scraped: %d\n", atomic.LoadInt64(&totalProducts))
	return nil
}

// isProductURL returns true if the URL looks like a Celeiro product page.
// Product URLs follow the pattern: https://www.celeiro.pt/<numeric-id>-<slug>
// Non-product URLs are categories/lists like /produtos/alimentacao/...
func isProductURL(loc string) bool {
	return productURLPattern.MatchString(loc)
}

// processSitemap processes a single sitemap using parallel workers
func (c *CeleiroScraper) processSitemap(ctx context.Context, sitemapURL string, cfg *config.Config, store storage.Storage, workerPool *WorkerPool, totalProducts *int64) (int, error) {
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

	fmt.Printf("Found %d URLs in sitemap\n", len(urlSet.URLs))

	// Filter to product URLs only
	var productURLs []URL
	for _, u := range urlSet.URLs {
		if isProductURL(u.Loc) {
			productURLs = append(productURLs, u)
		}
	}
	fmt.Printf("Filtered to %d product URLs (excluded %d non-product URLs)\n", len(productURLs), len(urlSet.URLs)-len(productURLs))

	// Apply date filtering
	filteredURLs := c.FilterURLsBySinceDate(productURLs, cfg.SinceDate)
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
func (c *CeleiroScraper) scrapeProductPage(ctx context.Context, productURL string, limiter *rate.Limiter) (*storage.Product, error) {
	doc, err := c.FetchPage(ctx, productURL, limiter)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch product page: %w", err)
	}

	return c.extractProductFromPage(doc, productURL)
}

// extractProductFromPage extracts product information from a Celeiro product page.
// Celeiro is a Magento-based store. It does NOT emit JSON-LD Product data, but it
// does use microdata (itemprop) and Open Graph meta tags extensively.
func (c *CeleiroScraper) extractProductFromPage(doc *goquery.Document, productURL string) (*storage.Product, error) {
	name := c.findProductName(doc)
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

// findProductName tries to find the product name on the page.
// Celeiro uses Magento's standard page-title with itemprop="name".
func (c *CeleiroScraper) findProductName(doc *goquery.Document) string {
	// Primary: itemprop name inside the page title heading
	if name := doc.Find("h1.page-title span.base[itemprop='name']").First().Text(); name != "" {
		return c.CleanText(name)
	}

	// Fallback: any h1.page-title
	if name := doc.Find("h1.page-title").First().Text(); name != "" {
		return c.CleanText(name)
	}

	// Fallback: Open Graph title
	if content, exists := doc.Find("meta[property='og:title']").Attr("content"); exists && content != "" {
		return c.CleanText(content)
	}

	return ""
}

// findProductPrice tries to find the product price on the page.
// Celeiro embeds price in multiple places: product:price:amount meta, itemprop price meta,
// data-price-amount attribute, and the visible span.price text.
func (c *CeleiroScraper) findProductPrice(doc *goquery.Document) string {
	// Primary: Open Graph product price (most reliable, plain numeric value)
	if content, exists := doc.Find("meta[property='product:price:amount']").Attr("content"); exists && content != "" {
		return "€" + content
	}

	// Microdata itemprop price
	if content, exists := doc.Find("meta[itemprop='price']").Attr("content"); exists && content != "" {
		return "€" + content
	}

	// data-price-amount on the price wrapper
	if amount, exists := doc.Find("span.price-wrapper[data-price-amount]").Attr("data-price-amount"); exists && amount != "" {
		return "€" + amount
	}

	// Visible price text
	if price := doc.Find(".price-box span.price").First().Text(); price != "" {
		return price
	}

	return ""
}

// findProductImage tries to find the product image URL.
func (c *CeleiroScraper) findProductImage(doc *goquery.Document) string {
	// Open Graph image (high quality, always absolute URL)
	if content, exists := doc.Find("meta[property='og:image']").Attr("content"); exists && content != "" {
		return content
	}

	// Microdata image link
	if href, exists := doc.Find("link[itemprop='image']").Attr("href"); exists && href != "" {
		if strings.HasPrefix(href, "http") {
			return href
		}
		return c.baseURL + href
	}

	// Fotorama gallery image
	if src, exists := doc.Find(".fotorama__img").First().Attr("src"); exists && src != "" {
		if strings.HasPrefix(src, "http") {
			return src
		}
		return c.baseURL + src
	}

	return ""
}

// findProductBrand tries to find the product brand.
// Celeiro uses microdata: <span itemprop="brand"><meta itemprop="name" content="Fharmonat" /></span>
func (c *CeleiroScraper) findProductBrand(doc *goquery.Document) string {
	// Microdata brand name
	if content, exists := doc.Find("span[itemprop='brand'] meta[itemprop='name']").Attr("content"); exists && content != "" {
		return c.CleanText(content)
	}

	return ""
}

// findProductCategory extracts category tags from the product page.
// Celeiro shows category tags in a .categories-container list, e.g. "Desintoxicação", "Digestão".
func (c *CeleiroScraper) findProductCategory(doc *goquery.Document) string {
	var categories []string
	doc.Find(".categories-container .categories li a").Each(func(i int, s *goquery.Selection) {
		if cat := c.CleanText(s.Text()); cat != "" {
			categories = append(categories, cat)
		}
	})

	if len(categories) > 0 {
		return strings.Join(categories, ", ")
	}

	return ""
}

// findProductUnit extracts the unit/weight information.
// Celeiro shows this in a .price-per-unit div, e.g. "1 Litros • 32.84€ por Ltr."
func (c *CeleiroScraper) findProductUnit(doc *goquery.Document) string {
	if unit := doc.Find(".price-per-unit p").First().Text(); unit != "" {
		return c.CleanText(unit)
	}

	if unit := doc.Find(".price-per-unit").First().Text(); unit != "" {
		return c.CleanText(unit)
	}

	return ""
}

// findProductDescription tries to find the product description.
// Celeiro has a short description in .product-description and detailed attributes in .product-details.
func (c *CeleiroScraper) findProductDescription(doc *goquery.Document) string {
	// Short description in the product info area
	if desc := doc.Find(".product-description .more.overflow-text").First().Text(); desc != "" {
		return c.CleanText(desc)
	}

	// itemprop description value
	if desc := doc.Find("div.value[itemprop='description']").First().Text(); desc != "" {
		return c.CleanText(desc)
	}

	// Fallback: meta description
	if content, exists := doc.Find("meta[name='description']").Attr("content"); exists && content != "" {
		return c.CleanText(content)
	}

	return ""
}
