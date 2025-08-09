package scraper

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"price-scraper/internal/storage"
)

// BaseScraper provides common functionality for all scrapers
type BaseScraper struct {
	name       string
	baseURL    string
	userAgent  string
	client     *http.Client
	delay      time.Duration
}

// NewBaseScraper creates a new base scraper
func NewBaseScraper(name, baseURL, userAgent string) *BaseScraper {
	return &BaseScraper{
		name:      name,
		baseURL:   baseURL,
		userAgent: userAgent,
		delay:     time.Second, // Default 1 second delay
	}
}

// GetName returns the scraper name
func (b *BaseScraper) GetName() string {
	return b.name
}

// SetHTTPClient sets the HTTP client
func (b *BaseScraper) SetHTTPClient(client *http.Client) {
	b.client = client
}

// SetDelay sets the delay between requests
func (b *BaseScraper) SetDelay(delay time.Duration) {
	b.delay = delay
}

// FetchPage fetches a web page and returns a goquery document
func (b *BaseScraper) FetchPage(url string) (*goquery.Document, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", b.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "pt-PT,pt;q=0.9,en;q=0.8")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP error: %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	// Add delay to be respectful to the server
	time.Sleep(b.delay)

	return doc, nil
}

// ParsePrice extracts price from a string, handling various formats
func (b *BaseScraper) ParsePrice(priceStr string) (float64, error) {
	// Remove common currency symbols and whitespace
	priceStr = strings.TrimSpace(priceStr)
	priceStr = strings.ReplaceAll(priceStr, "€", "")
	priceStr = strings.ReplaceAll(priceStr, "EUR", "")
	priceStr = strings.ReplaceAll(priceStr, " ", "")
	
	// Handle Portuguese decimal format (comma as decimal separator)
	priceStr = strings.ReplaceAll(priceStr, ",", ".")
	
	// Extract numbers using regex
	re := regexp.MustCompile(`\d+\.?\d*`)
	matches := re.FindAllString(priceStr, -1)
	
	if len(matches) == 0 {
		return 0, fmt.Errorf("no price found in string: %s", priceStr)
	}
	
	// Take the last match (usually the actual price)
	priceStr = matches[len(matches)-1]
	
	price, err := strconv.ParseFloat(priceStr, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse price '%s': %w", priceStr, err)
	}
	
	return price, nil
}

// CleanText cleans and normalizes text content
func (b *BaseScraper) CleanText(text string) string {
	// Remove extra whitespace and normalize
	text = strings.TrimSpace(text)
	text = regexp.MustCompile(`\s+`).ReplaceAllString(text, " ")
	return text
}

// CreateProduct creates a product with common fields filled
func (b *BaseScraper) CreateProduct(name, priceStr, url string) (*storage.Product, error) {
	price, err := b.ParsePrice(priceStr)
	if err != nil {
		return nil, err
	}

	return &storage.Product{
		Store:     b.name,
		Name:      b.CleanText(name),
		Price:     price,
		Currency:  "EUR",
		URL:       url,
		ScrapedAt: time.Now(),
	}, nil
}
