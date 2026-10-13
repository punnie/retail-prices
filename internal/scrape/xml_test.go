package scrape

import (
	"strings"
	"testing"
)

func TestParseSitemapIndex(t *testing.T) {
	base := NewBaseScraper("Test", "https://example.com", "test-agent")

	xmlData := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
    <sitemap>
        <loc>https://example.com/sitemap1.xml</loc>
        <lastmod>2025-08-08T22:00:01+00:00</lastmod>
    </sitemap>
    <sitemap>
        <loc>https://example.com/sitemap2.xml</loc>
        <lastmod>2025-08-08T22:00:01+00:00</lastmod>
    </sitemap>
</sitemapindex>`)

	sitemapIndex, err := base.ParseSitemapIndex(xmlData)
	if err != nil {
		t.Fatalf("Failed to parse sitemap index: %v", err)
	}

	if len(sitemapIndex.Sitemaps) != 2 {
		t.Errorf("Expected 2 sitemaps, got %d", len(sitemapIndex.Sitemaps))
	}

	if sitemapIndex.Sitemaps[0].Loc != "https://example.com/sitemap1.xml" {
		t.Errorf("Expected first sitemap URL to be 'https://example.com/sitemap1.xml', got '%s'", sitemapIndex.Sitemaps[0].Loc)
	}

	if sitemapIndex.Sitemaps[1].Loc != "https://example.com/sitemap2.xml" {
		t.Errorf("Expected second sitemap URL to be 'https://example.com/sitemap2.xml', got '%s'", sitemapIndex.Sitemaps[1].Loc)
	}
}

func TestParseSitemap(t *testing.T) {
	base := NewBaseScraper("Test", "https://example.com", "test-agent")

	xmlData := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
    <url>
        <loc>https://example.com/product1.html</loc>
        <lastmod>2025-08-08T22:00:01+00:00</lastmod>
        <changefreq>daily</changefreq>
        <priority>0.5</priority>
    </url>
    <url>
        <loc>https://example.com/product2.html</loc>
        <lastmod>2025-08-08T22:00:01+00:00</lastmod>
        <changefreq>daily</changefreq>
        <priority>0.5</priority>
    </url>
</urlset>`)

	urlSet, err := base.ParseSitemap(xmlData)
	if err != nil {
		t.Fatalf("Failed to parse sitemap: %v", err)
	}

	if len(urlSet.URLs) != 2 {
		t.Errorf("Expected 2 URLs, got %d", len(urlSet.URLs))
	}

	if urlSet.URLs[0].Loc != "https://example.com/product1.html" {
		t.Errorf("Expected first URL to be 'https://example.com/product1.html', got '%s'", urlSet.URLs[0].Loc)
	}

	if urlSet.URLs[1].Loc != "https://example.com/product2.html" {
		t.Errorf("Expected second URL to be 'https://example.com/product2.html', got '%s'", urlSet.URLs[1].Loc)
	}

	if urlSet.URLs[0].ChangeFreq != "daily" {
		t.Errorf("Expected first URL changefreq to be 'daily', got '%s'", urlSet.URLs[0].ChangeFreq)
	}

	if urlSet.URLs[0].Priority != "0.5" {
		t.Errorf("Expected first URL priority to be '0.5', got '%s'", urlSet.URLs[0].Priority)
	}
}

func TestFilterProductSitemaps(t *testing.T) {
	scraper := NewContinenteScraper()

	sitemaps := []Sitemap{
		{Loc: "https://example.com/sitemap-product.xml"},
		{Loc: "https://example.com/sitemap-category.xml"},
		{Loc: "https://example.com/sitemap-custom_sitemap_1-product.xml"},
		{Loc: "https://example.com/sitemap-image.xml"},
		{Loc: "https://example.com/sitemap-custom_sitemap_2-product.xml"},
	}

	productSitemaps := scraper.filterProductSitemaps(sitemaps)

	expectedCount := 3
	if len(productSitemaps) != expectedCount {
		t.Errorf("Expected %d product sitemaps, got %d", expectedCount, len(productSitemaps))
	}

	// Check that all returned sitemaps contain "product"
	for _, sitemap := range productSitemaps {
		if !strings.Contains(sitemap.Loc, "product") {
			t.Errorf("Expected sitemap URL to contain 'product', got '%s'", sitemap.Loc)
		}
	}
}
