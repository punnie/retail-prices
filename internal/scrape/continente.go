package scraper

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"price-scraper/internal/storage"
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

// ScrapeProducts scrapes products from Continente using sitemap-based approach
func (c *ContinenteScraper) ScrapeProducts() ([]*storage.Product, error) {
	var products []*storage.Product
	
	// Use the callback version and collect products
	callback := func(product *storage.Product) error {
		products = append(products, product)
		return nil
	}
	
	err := c.ScrapeProductsWithCallback(callback)
	if err != nil {
		return nil, err
	}
	
	return products, nil
}

// ScrapeProductsWithCallback scrapes products from Continente using sitemap-based approach with streaming storage
func (c *ContinenteScraper) ScrapeProductsWithCallback(callback ProductCallback) error {
	// Use sitemap-based approach for comprehensive scraping
	fmt.Printf("Fetching Continente sitemap index from: %s\n", c.sitemapIndexURL)
	
	// Fetch and parse sitemap index
	xmlData, err := c.FetchXML(c.sitemapIndexURL)
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
	
	// Process each product sitemap
	productCount := 0
	for i, sitemap := range productSitemaps {
		fmt.Printf("Processing sitemap %d/%d: %s\n", i+1, len(productSitemaps), sitemap.Loc)
		
		sitemapProductCount, err := c.processSitemapWithCallback(sitemap.Loc, callback)
		if err != nil {
			fmt.Printf("Warning: failed to process sitemap %s: %v\n", sitemap.Loc, err)
			continue
		}
		
		productCount += sitemapProductCount
		
		// Limit the number of products to avoid overwhelming the system
		if productCount >= 100 {
			fmt.Printf("Reached product limit of 100, stopping\n")
			break
		}
	}
	
	return nil
}

// filterProductSitemaps filters sitemaps to only include product sitemaps
func (c *ContinenteScraper) filterProductSitemaps(sitemaps []Sitemap) []Sitemap {
	var productSitemaps []Sitemap
	
	for _, sitemap := range sitemaps {
		// Look for sitemaps that contain "product" in their URL
		if strings.Contains(sitemap.Loc, "product") {
			productSitemaps = append(productSitemaps, sitemap)
		}
	}
	
	return productSitemaps
}

// processSitemap processes a single product sitemap and extracts product URLs
func (c *ContinenteScraper) processSitemap(sitemapURL string) ([]*storage.Product, error) {
	var products []*storage.Product
	
	// Fetch sitemap XML
	xmlData, err := c.FetchXML(sitemapURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch sitemap: %w", err)
	}
	
	// Parse sitemap
	urlSet, err := c.ParseSitemap(xmlData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse sitemap: %w", err)
	}
	
	fmt.Printf("Found %d product URLs in sitemap\n", len(urlSet.URLs))
	
	// Process each product URL (limit to avoid overwhelming)
	maxProducts := 20 // Limit per sitemap
	for i, urlEntry := range urlSet.URLs {
		if i >= maxProducts {
			fmt.Printf("Reached per-sitemap limit of %d products\n", maxProducts)
			break
		}
		
		product, err := c.scrapeProductPage(urlEntry.Loc)
		if err != nil {
			fmt.Printf("Warning: failed to scrape product %s: %v\n", urlEntry.Loc, err)
			continue
		}
		
		if product != nil {
			products = append(products, product)
		}
	}
	
	return products, nil
}

// scrapeProductPage scrapes a single product page
func (c *ContinenteScraper) scrapeProductPage(productURL string) (*storage.Product, error) {
	fmt.Printf("Scraping product: %s\n", productURL)
	
	doc, err := c.FetchPage(productURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch product page: %w", err)
	}
	
	// Extract product information from the page
	return c.extractProductFromPage(doc, productURL)
}

// extractProductFromPage extracts product information from a product page
func (c *ContinenteScraper) extractProductFromPage(doc *goquery.Document, productURL string) (*storage.Product, error) {
	// Try to find product name
	name := c.FindProductName(doc)
	if name == "" {
		return nil, fmt.Errorf("could not find product name")
	}
	
	// Try to find price
	priceStr := c.findProductPrice(doc)
	if priceStr == "" {
		return nil, fmt.Errorf("could not find product price")
	}
	
	// Create product
	product, err := c.CreateProduct(name, priceStr, productURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create product: %w", err)
	}
	
	// Try to find additional information
	if imageURL := c.findProductImage(doc); imageURL != "" {
		product.ImageURL = imageURL
	}
	
	if brand := c.findProductBrand(doc); brand != "" {
		product.Brand = brand
	}
	
	if category := c.findProductCategory(doc); category != "" {
		product.Category = category
	}
	
	// Extract unit information and add to description
	if unit := c.findProductUnit(doc); unit != "" {
		if product.Description != "" {
			product.Description += " | " + unit
		} else {
			product.Description = unit
		}
	}
	
	// Extract short description
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
	if name := c.extractFromJSONLD(doc, "name"); name != "" {
		return c.CleanText(name)
	}
	
	// Based on the HTML structure analysis, the product name is in h1.product-name
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
	
	// Fallback: try meta tags (but avoid title tag as it can change for cosmetic reasons)
	if content, exists := doc.Find("meta[property='og:title']").Attr("content"); exists && content != "" {
		// Clean up the title (remove " - Continente | Continente Online" suffix)
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
	if price := c.extractPriceFromJSONLD(doc); price != "" {
		return price
	}
	
	// Based on the HTML structure analysis, the price is in .price .value or .sales .value
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
			// Check if this looks like a price
			if strings.Contains(text, ",") || strings.Contains(text, ".") {
				foundPrice = text
			}
		}
	})
	
	return foundPrice
}

// extractFromJSONLD extracts a field from JSON-LD structured data
func (c *ContinenteScraper) extractFromJSONLD(doc *goquery.Document, field string) string {
	// Look for JSON-LD script tags
	var foundValue string
	doc.Find("script[type='application/ld+json']").Each(func(i int, s *goquery.Selection) {
		if foundValue != "" {
			return
		}
		
		jsonText := s.Text()
		if jsonText == "" {
			return
		}
		
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(jsonText), &data); err != nil {
			return
		}
		
		// Check if this is a Product schema
		if typeVal, ok := data["@type"]; ok && typeVal == "Product" {
			if fieldVal, ok := data[field]; ok {
				if str, ok := fieldVal.(string); ok && str != "" {
					foundValue = str
					return
				}
			}
		}
	})
	
	return foundValue
}

// extractPriceFromJSONLD extracts price from JSON-LD structured data
func (c *ContinenteScraper) extractPriceFromJSONLD(doc *goquery.Document) string {
	// Look for JSON-LD script tags
	var foundPrice string
	doc.Find("script[type='application/ld+json']").Each(func(i int, s *goquery.Selection) {
		if foundPrice != "" {
			return
		}
		
		jsonText := s.Text()
		if jsonText == "" {
			return
		}
		
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(jsonText), &data); err != nil {
			return
		}
		
		// Check if this is a Product schema
		if typeVal, ok := data["@type"]; ok && typeVal == "Product" {
			if offers, ok := data["offers"]; ok {
				if offersMap, ok := offers.(map[string]interface{}); ok {
					if price, ok := offersMap["price"]; ok {
						if priceStr, ok := price.(string); ok && priceStr != "" {
							foundPrice = "€" + priceStr
							return
						}
						if priceFloat, ok := price.(float64); ok {
							foundPrice = fmt.Sprintf("€%.2f", priceFloat)
							return
						}
					}
				}
			}
		}
	})
	
	return foundPrice
}

// findProductImage tries to find the product image URL
func (c *ContinenteScraper) findProductImage(doc *goquery.Document) string {
	// Based on the HTML structure analysis, the image is in .ct-product-image or .pdp-carousel-item img
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
	
	// Fallback: try meta tags
	if content, exists := doc.Find("meta[property='og:image']").Attr("content"); exists && content != "" {
		return content
	}
	
	return ""
}

// findProductBrand tries to find the product brand
func (c *ContinenteScraper) findProductBrand(doc *goquery.Document) string {
	// Based on the HTML structure analysis, the brand is in .ct-pdp--brand or a link with brand info
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

// findProductUnit tries to find the product unit information (liter, kilogram, etc)
func (c *ContinenteScraper) findProductUnit(doc *goquery.Document) string {
	// Based on the HTML structure analysis, the unit is in .ct-pdp--unit or similar
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
	// Based on the HTML structure analysis, the description is in .ct-pdp--short-description
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


// processSitemapWithCallback processes a single product sitemap and calls callback for each product
func (c *ContinenteScraper) processSitemapWithCallback(sitemapURL string, callback ProductCallback) (int, error) {
	// Fetch sitemap XML
	xmlData, err := c.FetchXML(sitemapURL)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch sitemap: %w", err)
	}
	
	// Parse sitemap
	urlSet, err := c.ParseSitemap(xmlData)
	if err != nil {
		return 0, fmt.Errorf("failed to parse sitemap: %w", err)
	}
	
	fmt.Printf("Found %d product URLs in sitemap\n", len(urlSet.URLs))
	
	// Process each product URL (limit to avoid overwhelming)
	maxProducts := 20 // Limit per sitemap
	productCount := 0
	for i, urlEntry := range urlSet.URLs {
		if i >= maxProducts {
			fmt.Printf("Reached per-sitemap limit of %d products\n", maxProducts)
			break
		}
		
		product, err := c.scrapeProductPage(urlEntry.Loc)
		if err != nil {
			fmt.Printf("Warning: failed to scrape product %s: %v\n", urlEntry.Loc, err)
			continue
		}
		
		if product != nil {
			if err := callback(product); err != nil {
				return productCount, fmt.Errorf("callback failed for product %s: %w", product.Name, err)
			}
			productCount++
		}
	}
	
	return productCount, nil
}
