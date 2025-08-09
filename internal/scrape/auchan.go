package scraper

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"price-scraper/internal/storage"
)

// AuchanScraper scrapes products from Auchan online store
type AuchanScraper struct {
	*BaseScraper
}

// NewAuchanScraper creates a new Auchan scraper
func NewAuchanScraper() *AuchanScraper {
	userAgent := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	base := NewBaseScraper("Auchan", "https://www.auchan.pt", userAgent)
	
	return &AuchanScraper{
		BaseScraper: base,
	}
}

// ScrapeProducts scrapes products from Auchan
func (a *AuchanScraper) ScrapeProducts(searchTerm string) ([]*storage.Product, error) {
	var products []*storage.Product
	
	if searchTerm == "" {
		// If no search term, scrape some popular categories
		categories := []string{
			"alimentacao",
			"frescos",
			"congelados",
			"bebidas",
			"casa-jardim",
		}
		
		for _, category := range categories {
			categoryProducts, err := a.scrapeCategory(category)
			if err != nil {
				fmt.Printf("Warning: failed to scrape Auchan category %s: %v\n", category, err)
				continue
			}
			products = append(products, categoryProducts...)
		}
	} else {
		// Search for specific products
		searchProducts, err := a.searchProducts(searchTerm)
		if err != nil {
			return nil, err
		}
		products = append(products, searchProducts...)
	}
	
	return products, nil
}

// searchProducts searches for products using the search functionality
func (a *AuchanScraper) searchProducts(searchTerm string) ([]*storage.Product, error) {
	// Encode the search term for URL
	encodedTerm := url.QueryEscape(searchTerm)
	searchURL := fmt.Sprintf("%s/search?q=%s", a.baseURL, encodedTerm)
	
	fmt.Printf("Searching Auchan for: %s\n", searchTerm)
	
	doc, err := a.FetchPage(searchURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch search page: %w", err)
	}
	
	return a.extractProductsFromPage(doc, searchURL)
}

// scrapeCategory scrapes products from a specific category
func (a *AuchanScraper) scrapeCategory(category string) ([]*storage.Product, error) {
	categoryURL := fmt.Sprintf("%s/%s", a.baseURL, category)
	
	fmt.Printf("Scraping Auchan category: %s\n", category)
	
	doc, err := a.FetchPage(categoryURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch category page: %w", err)
	}
	
	return a.extractProductsFromPage(doc, categoryURL)
}

// extractProductsFromPage extracts products from a page document
func (a *AuchanScraper) extractProductsFromPage(doc *goquery.Document, pageURL string) ([]*storage.Product, error) {
	var products []*storage.Product
	
	// Common selectors for Auchan product listings
	productSelectors := []string{
		".product-item",
		".product-card",
		".product",
		"[data-testid*='product']",
		".au-product-tile",
		".item-product",
		".product-tile",
	}
	
	var foundProducts bool
	
	for _, selector := range productSelectors {
		doc.Find(selector).Each(func(i int, s *goquery.Selection) {
			foundProducts = true
			product := a.extractProductFromElement(s, pageURL)
			if product != nil {
				products = append(products, product)
			}
		})
		
		if foundProducts {
			break
		}
	}
	
	if !foundProducts {
		// Fallback: try to find any element that might contain product information
		doc.Find("*").Each(func(i int, s *goquery.Selection) {
			text := s.Text()
			if strings.Contains(text, "€") && len(strings.TrimSpace(text)) < 200 {
				product := a.extractProductFromElement(s, pageURL)
				if product != nil {
					products = append(products, product)
				}
			}
		})
	}
	
	return products, nil
}

// extractProductFromElement extracts product information from a DOM element
func (a *AuchanScraper) extractProductFromElement(s *goquery.Selection, baseURL string) *storage.Product {
	// Try to find product name
	name := a.findProductName(s)
	if name == "" {
		return nil
	}
	
	// Try to find price
	priceStr := a.findProductPrice(s)
	if priceStr == "" {
		return nil
	}
	
	// Try to find product URL
	productURL := a.findProductURL(s, baseURL)
	
	// Try to find image URL
	imageURL := a.findProductImage(s)
	
	// Try to find brand
	brand := a.findProductBrand(s)
	
	// Create product
	product, err := a.CreateProduct(name, priceStr, productURL)
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
func (a *AuchanScraper) findProductName(s *goquery.Selection) string {
	nameSelectors := []string{
		".product-name",
		".product-title",
		"h1", "h2", "h3", "h4",
		"[data-testid*='name']",
		"[data-testid*='title']",
		".title",
		".name",
		".item-name",
		".product-label",
	}
	
	for _, selector := range nameSelectors {
		if name := s.Find(selector).First().Text(); name != "" {
			return a.CleanText(name)
		}
	}
	
	// Fallback: use alt text from images
	if alt, exists := s.Find("img").Attr("alt"); exists && alt != "" {
		return a.CleanText(alt)
	}
	
	return ""
}

// findProductPrice tries to find the product price
func (a *AuchanScraper) findProductPrice(s *goquery.Selection) string {
	priceSelectors := []string{
		".price",
		".product-price",
		"[data-testid*='price']",
		".value",
		".amount",
		".item-price",
		".price-current",
	}
	
	for _, selector := range priceSelectors {
		if price := s.Find(selector).First().Text(); price != "" && strings.Contains(price, "€") {
			return price
		}
	}
	
	// Fallback: look for any text containing €
	text := s.Text()
	if strings.Contains(text, "€") {
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
func (a *AuchanScraper) findProductURL(s *goquery.Selection, baseURL string) string {
	// Look for links
	if href, exists := s.Find("a").Attr("href"); exists {
		if strings.HasPrefix(href, "http") {
			return href
		} else if strings.HasPrefix(href, "/") {
			return a.baseURL + href
		}
	}
	
	// Check if the element itself is a link
	if href, exists := s.Attr("href"); exists {
		if strings.HasPrefix(href, "http") {
			return href
		} else if strings.HasPrefix(href, "/") {
			return a.baseURL + href
		}
	}
	
	return baseURL
}

// findProductImage tries to find the product image URL
func (a *AuchanScraper) findProductImage(s *goquery.Selection) string {
	if src, exists := s.Find("img").Attr("src"); exists {
		if strings.HasPrefix(src, "http") {
			return src
		} else if strings.HasPrefix(src, "/") {
			return a.baseURL + src
		}
	}
	
	return ""
}

// findProductBrand tries to find the product brand
func (a *AuchanScraper) findProductBrand(s *goquery.Selection) string {
	brandSelectors := []string{
		".brand",
		".product-brand",
		"[data-testid*='brand']",
		".manufacturer",
		".item-brand",
		".brand-name",
	}
	
	for _, selector := range brandSelectors {
		if brand := s.Find(selector).First().Text(); brand != "" {
			return a.CleanText(brand)
		}
	}
	
	return ""
}
