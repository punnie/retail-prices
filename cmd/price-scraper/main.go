package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"price-scraper/internal/config"
	"price-scraper/internal/scraper"
	"price-scraper/internal/storage"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	store, err := storage.NewStorage(cfg.DatabaseType, cfg.DatabaseConnectionString)
	if err != nil {
		log.Fatalf("Failed to initialize storage: %v", err)
	}
	defer store.Close()

	scraperManager := scraper.NewManager(store, cfg)

	// Register available scrapers
	scraperManager.RegisterScraper("continente", scraper.NewContinenteScraper())
	scraperManager.RegisterScraper("pingodoce", scraper.NewPingoDoceScraper())
	scraperManager.RegisterScraper("auchan", scraper.NewAuchanScraper())

	if len(os.Args) < 2 {
		fmt.Println("Usage: price-scraper <store_name> [--since=YYYY-MM-DD]")
		fmt.Println("Available stores: continente, pingodoce, auchan")
		fmt.Println("Options:")
		fmt.Println("  --since=YYYY-MM-DD  Only scrape products modified after this date")
		fmt.Printf("\nConfiguration (via environment variables):\n")
		fmt.Printf("  PRICE_SCRAPER_DB_TYPE=%s (database type: sqlite or postgres)\n", cfg.DatabaseType)
		fmt.Printf("  PRICE_SCRAPER_MAX_WORKERS=%d (parallel workers)\n", cfg.MaxConcurrentWorkers)
		fmt.Printf("  PRICE_SCRAPER_MAX_SITEMAPS=%d (max sitemaps, -1=unlimited)\n", cfg.MaxSitemaps)
		fmt.Printf("  PRICE_SCRAPER_MAX_PRODUCTS_TOTAL=%d (max total products, -1=unlimited)\n", cfg.MaxProductsTotal)
		fmt.Printf("  PRICE_SCRAPER_MAX_PRODUCTS_PER_SITEMAP=%d (max products per sitemap)\n", cfg.MaxProductsPerSitemap)
		fmt.Printf("  PRICE_SCRAPER_REQUESTS_PER_SECOND=%.1f (rate limit)\n", cfg.RequestsPerSecond)
		fmt.Printf("  PRICE_SCRAPER_HTTP_TIMEOUT=%v (HTTP timeout)\n", cfg.HTTPTimeout)
		os.Exit(1)
	}

	storeName := os.Args[1]
	var sinceDate *time.Time

	// Parse remaining arguments
	for i := 2; i < len(os.Args); i++ {
		arg := os.Args[i]
		if strings.HasPrefix(arg, "--since=") {
			dateStr := strings.TrimPrefix(arg, "--since=")
			parsedDate, err := time.Parse("2006-01-02", dateStr)
			if err != nil {
				log.Fatalf("Invalid date format for --since parameter. Use YYYY-MM-DD format: %v", err)
			}
			sinceDate = &parsedDate
		} else {
			log.Fatalf("Unknown argument: %s", arg)
		}
	}

	// Set the sinceDate in configuration
	cfg.SinceDate = sinceDate

	fmt.Printf("Configuration:\n")
	fmt.Printf("  Workers: %d\n", cfg.MaxConcurrentWorkers)
	fmt.Printf("  Max Sitemaps: %d\n", cfg.MaxSitemaps)
	fmt.Printf("  Max Products Total: %d\n", cfg.MaxProductsTotal)
	fmt.Printf("  Max Products Per Sitemap: %d\n", cfg.MaxProductsPerSitemap)
	fmt.Printf("  Requests Per Second: %.1f\n", cfg.RequestsPerSecond)
	fmt.Printf("  HTTP Timeout: %v\n", cfg.HTTPTimeout)
	if cfg.SinceDate != nil {
		fmt.Printf("  Since Date: %s\n", cfg.SinceDate.Format("2006-01-02"))
	}
	fmt.Println()

	if err := scraperManager.ScrapeStore(storeName); err != nil {
		log.Fatalf("Failed to scrape store %s: %v", storeName, err)
	}
	fmt.Printf("Successfully scraped %s\n", storeName)
}
