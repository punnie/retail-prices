package scraper

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"strconv"
	"strings"
	"time"

	"price-scraper/internal/storage"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/time/rate"
)

// BaseScraper provides common functionality for all scrapers
type BaseScraper struct {
	name      string
	baseURL   string
	userAgent string
	client    *http.Client
	delay     time.Duration
}

// NewBaseScraper creates a new base scraper
func NewBaseScraper(name, baseURL, userAgent string) *BaseScraper {
	// Create HTTP client with cookie jar
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar:     jar,
		Timeout: 30 * time.Second,
	}

	return &BaseScraper{
		name:      name,
		baseURL:   baseURL,
		userAgent: userAgent,
		client:    client,
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
	// Don't set Accept-Encoding to let Go's HTTP client handle compression automatically
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Detect redirects: if the final URL differs from the requested URL, the page doesn't exist
	if finalURL := resp.Request.URL.String(); finalURL != url {
		return nil, fmt.Errorf("page redirected from %s to %s", url, finalURL)
	}

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

// ScrapeProductsWithCallback provides a default implementation that calls the old method
// Individual scrapers should override this for true streaming behavior
func (b *BaseScraper) ScrapeProductsWithCallback(callback func(*storage.Product) error) error {
	// This is a fallback implementation that calls the old method
	// Individual scrapers should override this method for true streaming behavior
	products, err := b.ScrapeProducts()
	if err != nil {
		return err
	}

	for _, product := range products {
		if err := callback(product); err != nil {
			return fmt.Errorf("callback failed for product %s: %w", product.Name, err)
		}
	}

	return nil
}

// ScrapeProducts provides a default implementation that should be overridden by individual scrapers
func (b *BaseScraper) ScrapeProducts() ([]*storage.Product, error) {
	return nil, fmt.Errorf("ScrapeProducts not implemented for %s", b.name)
}

// SitemapIndex represents the structure of a sitemap index XML
type SitemapIndex struct {
	XMLName  xml.Name  `xml:"sitemapindex"`
	Sitemaps []Sitemap `xml:"sitemap"`
}

// Sitemap represents a single sitemap entry in the index
type Sitemap struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod"`
}

// URLSet represents the structure of a sitemap XML with URLs
type URLSet struct {
	XMLName xml.Name `xml:"urlset"`
	URLs    []URL    `xml:"url"`
}

// URL represents a single URL entry in a sitemap
type URL struct {
	Loc        string `xml:"loc"`
	LastMod    string `xml:"lastmod"`
	ChangeFreq string `xml:"changefreq"`
	Priority   string `xml:"priority"`
}

// FetchXML fetches an XML document from a URL and returns the response body
func (b *BaseScraper) FetchXML(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", b.userAgent)
	req.Header.Set("Accept", "application/xml,text/xml,*/*")
	req.Header.Set("Accept-Language", "pt-PT,pt;q=0.9,en;q=0.8")

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP error: %d", resp.StatusCode)
	}

	// Read the response body
	body := make([]byte, 0)
	buf := make([]byte, 1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			body = append(body, buf[:n]...)
		}
		if err != nil {
			break
		}
	}

	// Add delay to be respectful to the server
	time.Sleep(b.delay)

	return body, nil
}

// FetchPageWithRateLimit fetches a web page with rate limiting
func (b *BaseScraper) FetchPageWithRateLimit(ctx context.Context, url string, limiter *rate.Limiter) (*goquery.Document, error) {
	// Wait for rate limiter
	if err := limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limiter error: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", b.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "pt-PT,pt;q=0.9,en;q=0.8")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	fmt.Printf("Fetching product at %s\n", url)

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Detect redirects: if the final URL differs from the requested URL, the page doesn't exist
	if finalURL := resp.Request.URL.String(); finalURL != url {
		return nil, fmt.Errorf("page redirected from %s to %s", url, finalURL)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP error: %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	return doc, nil
}

// FetchXMLWithRateLimit fetches an XML document with rate limiting
func (b *BaseScraper) FetchXMLWithRateLimit(ctx context.Context, url string, limiter *rate.Limiter) ([]byte, error) {
	// Wait for rate limiter
	if err := limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limiter error: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", b.userAgent)
	req.Header.Set("Accept", "application/xml,text/xml,*/*")
	req.Header.Set("Accept-Language", "pt-PT,pt;q=0.9,en;q=0.8")

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP error: %d", resp.StatusCode)
	}

	// Read the response body
	body := make([]byte, 0)
	buf := make([]byte, 1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			body = append(body, buf[:n]...)
		}
		if err != nil {
			break
		}
	}

	return body, nil
}

// ParseSitemapIndex parses a sitemap index XML and returns the sitemap URLs
func (b *BaseScraper) ParseSitemapIndex(xmlData []byte) (*SitemapIndex, error) {
	var sitemapIndex SitemapIndex
	err := xml.Unmarshal(xmlData, &sitemapIndex)
	if err != nil {
		return nil, fmt.Errorf("failed to parse sitemap index: %w", err)
	}
	return &sitemapIndex, nil
}

// ParseSitemap parses a sitemap XML and returns the URLs
func (b *BaseScraper) ParseSitemap(xmlData []byte) (*URLSet, error) {
	var urlSet URLSet
	err := xml.Unmarshal(xmlData, &urlSet)
	if err != nil {
		return nil, fmt.Errorf("failed to parse sitemap: %w", err)
	}
	return &urlSet, nil
}

// FilterURLsBySinceDate filters URLs based on their lastmod date
func (b *BaseScraper) FilterURLsBySinceDate(urls []URL, sinceDate *time.Time) []URL {
	if sinceDate == nil {
		return urls // No filtering if no date provided
	}

	var filteredURLs []URL
	for _, url := range urls {
		if url.LastMod == "" {
			// If no lastmod date, include the URL (assume it might be recent)
			filteredURLs = append(filteredURLs, url)
			continue
		}

		// Parse the lastmod date (ISO 8601 format)
		lastModTime, err := time.Parse(time.RFC3339, url.LastMod)
		if err != nil {
			// If we can't parse the date, include the URL to be safe
			filteredURLs = append(filteredURLs, url)
			continue
		}

		// Include URL if it was modified after the since date
		if lastModTime.After(*sinceDate) {
			filteredURLs = append(filteredURLs, url)
		}
	}

	return filteredURLs
}
