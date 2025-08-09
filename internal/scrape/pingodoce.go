package scraper

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
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
	
	return &PingoDoceScraper{
		BaseScraper: base,
	}
}

// ScrapeProducts scrapes products from Pingo Doce
func (p *PingoDoceScraper) ScrapeProducts(searchTerm string) ([]*storage.Product, error) {
	var products []*storage.Product
	
	if searchTerm == "" {
		// If no search term, scrape some popular categories
		categories := []string{
			"frescos",
			"mercearia",
			"congelados",
			"bebidas",
			"casa-e-jardim",
		}
		
		for _, category := range categories {
			categoryProducts, err := p.scrapeCategory(category)
			if err != nil {
				fmt.Printf("Warning: failed to scrape Pingo Doce category %s: %v\n", category, err)
				continue
			}
			products = append(products, categoryProducts...)
		}
	} else {
		// Search for specific products
		searchProducts, err := p.searchProducts(searchTerm)
		if err != nil {
			return nil, err
		}
		products = append(products, searchProducts...)
	}
	
	return products, nil
}

// searchProducts searches for products using the search functionality
func (p *PingoDoceScraper) searchProducts(searchTerm string) ([]*storage.Product, error) {
	// Encode the search term for URL
	encodedTerm := url.QueryEscape(searchTerm)
	searchURL := fmt.Sprintf("%s/pesquisar?q=%s", p.baseURL, encodedTerm)
	
	fmt.Printf("Searching Pingo Doce for: %s\n", searchTerm)
	
	doc, err := p.FetchPage(searchURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch search page: %w", err)
	}
	
	return p.extractProductsFromPage(doc, searchURL)
}

// scrapeCategory scrapes products from a specific category
func (p *PingoDoceScraper) scrapeCategory(category string) ([]*storage.Product, error) {
	categoryURL := fmt.Sprintf("%s/%s", p.baseURL, category)
	
	fmt.Printf("Scraping Pingo Doce category: %s\n", category)
	
	doc, err := p.FetchPage(categoryURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch category page: %w", err)
	}
	
	return p.extractProductsFromPage(doc, categoryURL)
}

// extractProductsFromPage extracts products from a page document
func (p *PingoDoceScraper) extractProductsFromPage(doc *goquery.Document, pageURL string) ([]*storage.Product, error) {
	var products []*storage.Product
	
	// Common selectors for Pingo Doce product listings
	productSelectors := []string{
		".product-item",
		".product-card",
		".product",
		"[data-testid*='product']",
		".pd-product-tile",
		".item-product",
	}
	
	var foundProducts bool
	
	for _, selector := range productSelectors {
		doc.Find(selector).Each(func(i int, s *goquery.Selection) {
			foundProducts = true
			product := p.extractProductFromElement(s, pageURL)
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
				product := p.extractProductFromElement(s, pageURL)
				if product != nil {
					products = append(products, product)
				}
			}
		})
	}
	
	return products, nil
}

// extractProductFromElement extracts product information from a DOM element
func (p *PingoDoceScraper) extractProductFromElement(s *goquery.Selection, baseURL string) *storage.Product {
	// Try to find product name
	name := p.findProductName(s)
	if name == "" {
		return nil
	}
	
	// Try to find price
	priceStr := p.findProductPrice(s)
	if priceStr == "" {
		return nil
	}
	
	// Try to find product URL
	productURL := p.findProductURL(s, baseURL)
	
	// Try to find image URL
	imageURL := p.findProductImage(s)
	
	// Try to find brand
	brand := p.findProductBrand(s)
	
	// Create product
	product, err := p.CreateProduct(name, priceStr, productURL)
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
func (p *PingoDoceScraper) findProductName(s *goquery.Selection) string {
	nameSelectors := []string{
		".product-name",
		".product-title",
		"h1", "h2", "h3", "h4",
		"[data-testid*='name']",
		"[data-testid*='title']",
		".title",
		".name",
		".item-name",
	}
	
	for _, selector := range nameSelectors {
		if name := s.Find(selector).First().Text(); name != "" {
			return p.CleanText(name)
		}
	}
	
	// Fallback: use alt text from images
	if alt, exists := s.Find("img").Attr("alt"); exists && alt != "" {
		return p.CleanText(alt)
	}
	
	return ""
}

// findProductPrice tries to find the product price
func (p *PingoDoceScraper) findProductPrice(s *goquery.Selection) string {
	priceSelectors := []string{
		".price",
		".product-price",
		"[data-testid*='price']",
		".value",
		".amount",
		".item-price",
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
func (p *PingoDoceScraper) findProductURL(s *goquery.Selection, baseURL string) string {
	// Look for links
	if href, exists := s.Find("a").Attr("href"); exists {
		if strings.HasPrefix(href, "http") {
			return href
		} else if strings.HasPrefix(href, "/") {
			return p.baseURL + href
		}
	}
	
	// Check if the element itself is a link
	if href, exists := s.Attr("href"); exists {
		if strings.HasPrefix(href, "http") {
			return href
		} else if strings.HasPrefix(href, "/") {
			return p.baseURL + href
		}
	}
	
	return baseURL
}

// findProductImage tries to find the product image URL
func (p *PingoDoceScraper) findProductImage(s *goquery.Selection) string {
	if src, exists := s.Find("img").Attr("src"); exists {
		if strings.HasPrefix(src, "http") {
			return src
		} else if strings.HasPrefix(src, "/") {
			return p.baseURL + src
		}
	}
	
	return ""
}

// findProductBrand tries to find the product brand
func (p *PingoDoceScraper) findProductBrand(s *goquery.Selection) string {
	brandSelectors := []string{
		".brand",
		".product-brand",
		"[data-testid*='brand']",
		".manufacturer",
		".item-brand",
	}
	
	for _, selector := range brandSelectors {
		if brand := s.Find(selector).First().Text(); brand != "" {
			return p.CleanText(brand)
		}
	}
	
	return ""
}
