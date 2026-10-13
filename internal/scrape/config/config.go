package config

import (
	"os"
	"strconv"
	"time"
)

// Config holds the scraper's tuning knobs. The database comes from
// DATABASE_URL (see internal/db), like every other retail subcommand.
type Config struct {
	UserAgent string

	// Scraping performance settings
	MaxConcurrentWorkers int // number of parallel workers

	// Limits
	MaxSitemaps           int // maximum number of sitemaps to process (-1 for unlimited)
	MaxProductsTotal      int // maximum total products to scrape (-1 for unlimited)
	MaxProductsPerSitemap int // maximum products per sitemap (-1 for unlimited)

	// Sequential page sizing limits
	PageSize          int // page size for sequential loads
	PageDiscoverySize int // number of pages to discover

	// Timeouts
	HTTPTimeout time.Duration // HTTP client timeout

	// Rate limiting
	RequestsPerSecond float64 // requests per second rate limit

	// Date filtering
	SinceDate *time.Time // only scrape products modified after this date
}

// Load loads the configuration from environment variables or defaults
func Load() *Config {
	userAgent := os.Getenv("PRICE_SCRAPER_USER_AGENT")
	if userAgent == "" {
		userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	}

	// Parse performance settings
	maxWorkers := parseEnvInt("PRICE_SCRAPER_MAX_WORKERS", 5)
	maxSitemaps := parseEnvInt("PRICE_SCRAPER_MAX_SITEMAPS", -1)            // unlimited by default
	maxProductsTotal := parseEnvInt("PRICE_SCRAPER_MAX_PRODUCTS_TOTAL", -1) // unlimited by default
	maxProductsPerSitemap := parseEnvInt("PRICE_SCRAPER_MAX_PRODUCTS_PER_SITEMAP", 100)
	httpTimeout := parseEnvDuration("PRICE_SCRAPER_HTTP_TIMEOUT", 30*time.Second)
	requestsPerSecond := parseEnvFloat("PRICE_SCRAPER_REQUESTS_PER_SECOND", 2.0)
	pageSize := parseEnvInt("PRICE_SCRAPER_PAGE_SIZE", 200)
	pageDiscoverySize := parseEnvInt("PRICE_SCRAPER_PAGE_DISCOVERY_SIZE", 1000)

	return &Config{
		UserAgent:             userAgent,
		MaxConcurrentWorkers:  maxWorkers,
		MaxSitemaps:           maxSitemaps,
		MaxProductsTotal:      maxProductsTotal,
		MaxProductsPerSitemap: maxProductsPerSitemap,
		PageSize:              pageSize,
		PageDiscoverySize:     pageDiscoverySize,
		HTTPTimeout:           httpTimeout,
		RequestsPerSecond:     requestsPerSecond,
	}
}

// parseEnvInt parses an environment variable as an integer with a default value
func parseEnvInt(key string, defaultValue int) int {
	if str := os.Getenv(key); str != "" {
		if val, err := strconv.Atoi(str); err == nil {
			return val
		}
	}
	return defaultValue
}

// parseEnvDuration parses an environment variable as a duration with a default value
func parseEnvDuration(key string, defaultValue time.Duration) time.Duration {
	if str := os.Getenv(key); str != "" {
		if val, err := time.ParseDuration(str); err == nil {
			return val
		}
	}
	return defaultValue
}

// parseEnvFloat parses an environment variable as a float64 with a default value
func parseEnvFloat(key string, defaultValue float64) float64 {
	if str := os.Getenv(key); str != "" {
		if val, err := strconv.ParseFloat(str, 64); err == nil {
			return val
		}
	}
	return defaultValue
}
