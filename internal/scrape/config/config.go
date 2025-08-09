package config

import (
	"os"
	"path/filepath"
)

// Config holds the application configuration
type Config struct {
	DatabasePath string
	UserAgent    string
	RequestDelay int // milliseconds between requests
}

// Load loads the configuration from environment variables or defaults
func Load() (*Config, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	dbPath := os.Getenv("PRICE_SCRAPER_DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join(homeDir, ".price-scraper", "prices.db")
	}

	// Ensure the directory exists
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, err
	}

	userAgent := os.Getenv("PRICE_SCRAPER_USER_AGENT")
	if userAgent == "" {
		userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	}

	return &Config{
		DatabasePath: dbPath,
		UserAgent:    userAgent,
		RequestDelay: 1000, // 1 second delay between requests
	}, nil
}
