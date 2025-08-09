package scraper

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"price-scraper/internal/storage"
)

// ContinenteScraper scrapes products from Continente online store
type ContinenteScraper struct {
	*BaseScraper
}

// NewContinenteScraper creates a new Continente scraper
func NewContinenteScraper() *ContinenteScraper {
	userAgent := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	base := NewBaseScraper("Continente", "https://www.continente.pt", userAgent)
	
	return &ContinenteScraper{
		BaseScraper: base,
	}
}

// ScrapeProducts scrapes products from Continente
func (c *ContinenteScraper) ScrapeProducts(searchTerm string) ([]*storage.Product, error) {
	var products []*storage.Product
	
	if searchTerm == "" {
		// If no search term, scrape some popular categories
		categories := []string{
			"frescos",
			"mercearia",
			"congelados",
			"bebidas",
			"limpeza-casa",
		}

		for _, category := range categories {
			categoryProducts, err := c.scrapeCategory(category)
			if err != nil {
				fmt.Printf("Warning: failed to scrape category %s: %v\n", category, err)
				continue
			}
			products = append(products, categoryProducts...)
		}
	} else {
		// Search for specific products
		searchProducts, err := c.searchProducts(searchTerm)
		if err != nil {
			return nil, err
		}
		products = append(products, searchProducts...)
	}
	
	return products, nil
}

// searchProducts searches for products using the search functionality
func (c *ContinenteScraper) searchProducts(searchTerm string) ([]*storage.Product, error) {
	// Encode the search term for URL
	encodedTerm := url.QueryEscape(searchTerm)
	searchURL := fmt.Sprintf("%s/pesquisa/?q=%s", c.baseURL, encodedTerm)
	
	fmt.Printf("Searching Continente for: %s\n", searchTerm)
	
	doc, err := c.FetchPage(searchURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch search page: %w", err)
	}
	
	return c.extractProductsFromPage(doc, searchURL)
}

// scrapeCategory scrapes products from a specific category
func (c *ContinenteScraper) scrapeCategory(category string) ([]*storage.Product, error) {
	categoryURL := fmt.Sprintf("%s/%s", c.baseURL, category)
	
	fmt.Printf("Scraping Continente category: %s\n", category)
	
	doc, err := c.FetchPage(categoryURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch category page: %w", err)
	}
	
	return c.extractProductsFromPage(doc, categoryURL)
}

// extractProductsFromPage extracts products from a page document
func (c *ContinenteScraper) extractProductsFromPage(doc *goquery.Document, pageURL string) ([]*storage.Product, error) {
	var products []*storage.Product
	
	// Common selectors for Continente product listings
	// Note: These selectors may need to be updated based on the actual website structure
	productSelectors := []string{
		".product-item",
		".product-card",
		".product",
		"[data-testid*='product']",
		".ct-product-tile",
	}
	
	var foundProducts bool
	
	for _, selector := range productSelectors {
		doc.Find(selector).Each(func(i int, s *goquery.Selection) {
			foundProducts = true
			product := c.extractProductFromElement(s, pageURL)
			if product != nil {
				products = append(products, product)
			}
		})
		
		if foundProducts {
			break // Found products with this selector, no need to try others
		}
	}
	
	if !foundProducts {
		// Fallback: try to find any element that might contain product information
		doc.Find("*").Each(func(i int, s *goquery.Selection) {
			// Look for elements that contain price information
			text := s.Text()
			if strings.Contains(text, "€") && len(strings.TrimSpace(text)) < 200 {
				// This might be a product, try to extract it
				product := c.extractProductFromElement(s, pageURL)
				if product != nil {
					products = append(products, product)
				}
			}
		})
	}
	
	return products, nil
}

// extractProductFromElement extracts product information from a DOM element
func (c *ContinenteScraper) extractProductFromElement(s *goquery.Selection, baseURL string) *storage.Product {
	// Try to find product name
	name := c.findProductName(s)
	if name == "" {
		return nil
	}
	
	// Try to find price
	priceStr := c.findProductPrice(s)
	if priceStr == "" {
		return nil
	}
	
	// Try to find product URL
	productURL := c.findProductURL(s, baseURL)
	
	// Try to find image URL
	imageURL := c.findProductImage(s)
	
	// Try to find brand
	brand := c.findProductBrand(s)
	
	// Create product
	product, err := c.CreateProduct(name, priceStr, productURL)
	if err != nil {
		return nil
	}
	
	// Add additional fields
	if imageURL != "" {
		product.ImageURL = imageURL
	}
	if brand != "" {
		product.Brand = brand
	}
	
	return product
}

// findProductName tries to find the product name in various ways
func (c *ContinenteScraper) findProductName(s *goquery.Selection) string {
	nameSelectors := []string{
		".product-name",
		".product-title",
		"h1", "h2", "h3", "h4",
		"[data-testid*='name']",
		"[data-testid*='title']",
		".title",
		".name",
	}
	
	for _, selector := range nameSelectors {
		if name := s.Find(selector).First().Text(); name != "" {
			return c.CleanText(name)
		}
	}
	
	// Fallback: use alt text from images
	if alt, exists := s.Find("img").Attr("alt"); exists && alt != "" {
		return c.CleanText(alt)
	}
	
	return ""
}

// findProductPrice tries to find the product price
func (c *ContinenteScraper) findProductPrice(s *goquery.Selection) string {
	priceSelectors := []string{
		".price",
		".product-price",
		"[data-testid*='price']",
		".value",
		".amount",
	}
	
	for _, selector := range priceSelectors {
		if price := s.Find(selector).First().Text(); price != "" && strings.Contains(price, "€") {
			return price
		}
	}
	
	// Fallback: look for any text containing €
	text := s.Text()
	if strings.Contains(text, "€") {
		// Extract price-like patterns
		lines := strings.Split(text, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.Contains(line, "€") && len(line) < 50 {
				return line
			}
		}
	}
	
	return ""
}

// findProductURL tries to find the product URL
func (c *ContinenteScraper) findProductURL(s *goquery.Selection, baseURL string) string {
	// Look for links
	if href, exists := s.Find("a").Attr("href"); exists {
		if strings.HasPrefix(href, "http") {
			return href
		} else if strings.HasPrefix(href, "/") {
			return c.baseURL + href
		}
	}
	
	// Check if the element itself is a link
	if href, exists := s.Attr("href"); exists {
		if strings.HasPrefix(href, "http") {
			return href
		} else if strings.HasPrefix(href, "/") {
			return c.baseURL + href
		}
	}
	
	return baseURL
}

// findProductImage tries to find the product image URL
func (c *ContinenteScraper) findProductImage(s *goquery.Selection) string {
	if src, exists := s.Find("img").Attr("src"); exists {
		if strings.HasPrefix(src, "http") {
			return src
		} else if strings.HasPrefix(src, "/") {
			return c.baseURL + src
		}
	}
	
	return ""
}

// findProductBrand tries to find the product brand
func (c *ContinenteScraper) findProductBrand(s *goquery.Selection) string {
	brandSelectors := []string{
		".brand",
		".product-brand",
		"[data-testid*='brand']",
		".manufacturer",
	}
	
	for _, selector := range brandSelectors {
		if brand := s.Find(selector).First().Text(); brand != "" {
			return c.CleanText(brand)
		}
	}
	
	return ""
}
