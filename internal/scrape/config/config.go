package config

import (
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Config holds the application configuration
type Config struct {
	// Database configuration
	DatabaseType             string // "sqlite" or "postgres"
	DatabasePath             string // for SQLite: file path
	DatabaseConnectionString string // for PostgreSQL: connection string

	UserAgent string

	// Scraping performance settings
	RequestDelay         time.Duration // delay between requests
	MaxConcurrentWorkers int           // number of parallel workers

	// Limits
	MaxSitemaps           int // maximum number of sitemaps to process (-1 for unlimited)
	MaxProductsTotal      int // maximum total products to scrape (-1 for unlimited)
	MaxProductsPerSitemap int // maximum products per sitemap (-1 for unlimited)

	// Timeouts
	HTTPTimeout time.Duration // HTTP client timeout

	// Rate limiting
	RequestsPerSecond float64 // requests per second rate limit

	// Date filtering
	SinceDate *time.Time // only scrape products modified after this date
}

// Load loads the configuration from environment variables or defaults
func Load() (*Config, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	// Database configuration
	dbType := os.Getenv("PRICE_SCRAPER_DB_TYPE")
	if dbType == "" {
		dbType = "sqlite" // default to SQLite
	}

	dbPath := os.Getenv("PRICE_SCRAPER_DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join(homeDir, ".price-scraper", "prices.db")
	}

	// For SQLite, ensure the directory exists
	if dbType == "sqlite" || dbType == "sqlite3" {
		if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
			return nil, err
		}
	}

	dbConnectionString := os.Getenv("PRICE_SCRAPER_DB_CONNECTION_STRING")
	// If no connection string is provided, use the appropriate default
	if dbConnectionString == "" {
		if dbType == "postgres" || dbType == "postgresql" {
			dbConnectionString = "host=localhost port=5432 user=postgres dbname=price_scraper sslmode=disable"
		} else {
			dbConnectionString = dbPath // for SQLite, connection string is the file path
		}
	}

	userAgent := os.Getenv("PRICE_SCRAPER_USER_AGENT")
	if userAgent == "" {
		userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	}

	// Parse performance settings
	requestDelay := parseEnvDuration("PRICE_SCRAPER_REQUEST_DELAY", 500*time.Millisecond)
	maxWorkers := parseEnvInt("PRICE_SCRAPER_MAX_WORKERS", 5)
	maxSitemaps := parseEnvInt("PRICE_SCRAPER_MAX_SITEMAPS", -1)            // unlimited by default
	maxProductsTotal := parseEnvInt("PRICE_SCRAPER_MAX_PRODUCTS_TOTAL", -1) // unlimited by default
	maxProductsPerSitemap := parseEnvInt("PRICE_SCRAPER_MAX_PRODUCTS_PER_SITEMAP", 100)
	httpTimeout := parseEnvDuration("PRICE_SCRAPER_HTTP_TIMEOUT", 30*time.Second)
	requestsPerSecond := parseEnvFloat("PRICE_SCRAPER_REQUESTS_PER_SECOND", 2.0)

	return &Config{
		DatabaseType:             dbType,
		DatabasePath:             dbPath,
		DatabaseConnectionString: dbConnectionString,
		UserAgent:                userAgent,
		RequestDelay:             requestDelay,
		MaxConcurrentWorkers:     maxWorkers,
		MaxSitemaps:              maxSitemaps,
		MaxProductsTotal:         maxProductsTotal,
		MaxProductsPerSitemap:    maxProductsPerSitemap,
		HTTPTimeout:              httpTimeout,
		RequestsPerSecond:        requestsPerSecond,
	}, nil
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
