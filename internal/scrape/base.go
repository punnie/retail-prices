package scrape

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"strconv"
	"strings"
	"time"

	"retail-prices/internal/scrape/rawlog"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/time/rate"
)

// BaseScraper provides common functionality for all scrapers
type BaseScraper struct {
	name      string
	baseURL   string
	userAgent string
	client    *http.Client
}

// NewBaseScraper creates a new base scraper
func NewBaseScraper(name, baseURL, userAgent string) *BaseScraper {
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

// FetchPage fetches a web page and returns a goquery document.
// If limiter is non-nil, it waits for rate limit permission before fetching.
func (b *BaseScraper) FetchPage(ctx context.Context, url string, limiter *rate.Limiter) (*goquery.Document, error) {
	if limiter != nil {
		if err := limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limiter error: %w", err)
		}
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

// FetchXML fetches an XML document from a URL and returns the response body.
// If limiter is non-nil, it waits for rate limit permission before fetching.
func (b *BaseScraper) FetchXML(ctx context.Context, url string, limiter *rate.Limiter) ([]byte, error) {
	if limiter != nil {
		if err := limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limiter error: %w", err)
		}
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

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	return body, nil
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
	text = strings.TrimSpace(text)
	text = regexp.MustCompile(`\s+`).ReplaceAllString(text, " ")
	return text
}

// CreateProduct creates a product with common fields filled
func (b *BaseScraper) CreateProduct(name, priceStr, url string) (*rawlog.Product, error) {
	price, err := b.ParsePrice(priceStr)
	if err != nil {
		return nil, err
	}

	return &rawlog.Product{
		Store:     b.name,
		Name:      b.CleanText(name),
		Price:     price,
		Currency:  "EUR",
		URL:       url,
		ScrapedAt: time.Now(),
	}, nil
}

// --- Sitemap XML types ---

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
		return urls
	}

	var filteredURLs []URL
	for _, url := range urls {
		if url.LastMod == "" {
			filteredURLs = append(filteredURLs, url)
			continue
		}

		lastModTime, err := time.Parse(time.RFC3339, url.LastMod)
		if err != nil {
			filteredURLs = append(filteredURLs, url)
			continue
		}

		if lastModTime.After(*sinceDate) {
			filteredURLs = append(filteredURLs, url)
		}
	}

	return filteredURLs
}

// --- JSON-LD extraction helpers ---

// ExtractFromJSONLD extracts a field from JSON-LD structured data on the page
func (b *BaseScraper) ExtractFromJSONLD(doc *goquery.Document, field string) string {
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

// ExtractPriceFromJSONLD extracts price from JSON-LD structured data
func (b *BaseScraper) ExtractPriceFromJSONLD(doc *goquery.Document) string {
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

// ExtractBrandFromJSONLD extracts brand name from JSON-LD structured data
func (b *BaseScraper) ExtractBrandFromJSONLD(doc *goquery.Document) string {
	var foundBrand string
	doc.Find("script[type='application/ld+json']").Each(func(i int, s *goquery.Selection) {
		if foundBrand != "" {
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

		if typeVal, ok := data["@type"]; ok && typeVal == "Product" {
			if brand, ok := data["brand"]; ok {
				if brandMap, ok := brand.(map[string]interface{}); ok {
					if name, ok := brandMap["name"]; ok {
						if nameStr, ok := name.(string); ok && nameStr != "" {
							foundBrand = nameStr
							return
						}
					}
				}
			}
		}
	})

	return foundBrand
}
