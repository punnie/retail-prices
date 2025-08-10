package scraper

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

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
func (p *PingoDoceScraper) ScrapeProducts(searchTerm string) ([]*storage.Product, error) {
	var allProducts []*storage.Product
	
	// Use the callback version and collect products
	callback := func(product *storage.Product) error {
		allProducts = append(allProducts, product)
		return nil
	}
	
	err := p.ScrapeProductsWithCallback(searchTerm, callback)
	if err != nil {
		return nil, err
	}
	
	return allProducts, nil
}

// ScrapeProductsWithCallback scrapes products from Pingo Doce using the API endpoint with streaming storage
func (p *PingoDoceScraper) ScrapeProductsWithCallback(searchTerm string, callback ProductCallback) error {
	// Use the API endpoint to get all products with pagination
	start := 0
	pageSize := 20
	
	fmt.Printf("Starting to scrape Pingo Doce products using API endpoint (streaming mode)\n")
	
	searchLower := ""
	if searchTerm != "" {
		searchLower = strings.ToLower(searchTerm)
	}
	
	for {
		apiURL := fmt.Sprintf("https://www.pingodoce.pt/on/demandware.store/Sites-pingo-doce-Site/default/Search-UpdateGrid?start=%d&sz=%d", start, pageSize)
		
		fmt.Printf("Fetching page: start=%d, size=%d\n", start, pageSize)
		
		doc, err := p.FetchPage(apiURL)
		if err != nil {
			return fmt.Errorf("failed to fetch API page: %w", err)
		}
		
		pageProductCount, err := p.extractProductsFromAPIResponseWithCallback(doc, searchLower, callback)
		if err != nil {
			return fmt.Errorf("failed to extract products from page: %w", err)
		}
		
		if pageProductCount == 0 {
			fmt.Printf("No products found on this page, stopping pagination\n")
			break
		}
		
		fmt.Printf("Processed %d products from this page\n", pageProductCount)
		
		// If we got less than the page size, we've reached the end
		if pageProductCount < pageSize {
			fmt.Printf("Reached end of pagination (got %d < %d products)\n", pageProductCount, pageSize)
			break
		}
		
		start += pageSize
	}
	
	fmt.Printf("Finished scraping Pingo Doce\n")
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
func (p *PingoDoceScraper) extractProductsFromAPIResponseWithCallback(doc *goquery.Document, searchLower string, callback ProductCallback) (int, error) {
	productCount := 0
	
	// Find all product containers
	doc.Find(".product[data-pid]").Each(func(i int, s *goquery.Selection) {
		product := p.extractProductFromElement(s)
		if product == nil {
			return
		}
		
		// Filter by search term if provided
		if searchLower != "" {
			if !strings.Contains(strings.ToLower(product.Name), searchLower) &&
			   !strings.Contains(strings.ToLower(product.Brand), searchLower) &&
			   !strings.Contains(strings.ToLower(product.Description), searchLower) {
				return
			}
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
