package main

import (
	"fmt"
	"log"
	"os"

	"price-scraper/internal/config"
	"price-scraper/internal/scraper"
	"price-scraper/internal/storage"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	store, err := storage.NewSQLiteStorage(cfg.DatabasePath)
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
		fmt.Println("Usage: price-scraper <store_name> [mode]")
		fmt.Println("Available stores: continente, pingodoce, auchan")
		fmt.Println("Available modes: streaming (default), parallel")
		fmt.Printf("\nConfiguration (via environment variables):\n")
		fmt.Printf("  PRICE_SCRAPER_MAX_WORKERS=%d (parallel workers)\n", cfg.MaxConcurrentWorkers)
		fmt.Printf("  PRICE_SCRAPER_MAX_SITEMAPS=%d (max sitemaps, -1=unlimited)\n", cfg.MaxSitemaps)
		fmt.Printf("  PRICE_SCRAPER_MAX_PRODUCTS_TOTAL=%d (max total products, -1=unlimited)\n", cfg.MaxProductsTotal)
		fmt.Printf("  PRICE_SCRAPER_MAX_PRODUCTS_PER_SITEMAP=%d (max products per sitemap)\n", cfg.MaxProductsPerSitemap)
		fmt.Printf("  PRICE_SCRAPER_REQUEST_DELAY=%v (delay between requests)\n", cfg.RequestDelay)
		fmt.Printf("  PRICE_SCRAPER_REQUESTS_PER_SECOND=%.1f (rate limit)\n", cfg.RequestsPerSecond)
		fmt.Printf("  PRICE_SCRAPER_HTTP_TIMEOUT=%v (HTTP timeout)\n", cfg.HTTPTimeout)
		os.Exit(1)
	}

	storeName := os.Args[1]
	mode := "streaming"
	if len(os.Args) > 2 {
		mode = os.Args[2]
	}

	fmt.Printf("Configuration:\n")
	fmt.Printf("  Workers: %d\n", cfg.MaxConcurrentWorkers)
	fmt.Printf("  Max Sitemaps: %d\n", cfg.MaxSitemaps)
	fmt.Printf("  Max Products Total: %d\n", cfg.MaxProductsTotal)
	fmt.Printf("  Max Products Per Sitemap: %d\n", cfg.MaxProductsPerSitemap)
	fmt.Printf("  Request Delay: %v\n", cfg.RequestDelay)
	fmt.Printf("  Requests Per Second: %.1f\n", cfg.RequestsPerSecond)
	fmt.Printf("  HTTP Timeout: %v\n", cfg.HTTPTimeout)
	fmt.Println()

	switch mode {
	case "parallel":
		if err := scraperManager.ScrapeStoreParallel(storeName); err != nil {
			log.Fatalf("Failed to scrape store %s in parallel mode: %v", storeName, err)
		}
		fmt.Printf("Successfully scraped %s using parallel mode\n", storeName)
	case "streaming":
		if err := scraperManager.ScrapeStoreStreaming(storeName); err != nil {
			log.Fatalf("Failed to scrape store %s in streaming mode: %v", storeName, err)
		}
		fmt.Printf("Successfully scraped %s using streaming mode\n", storeName)
	default:
		log.Fatalf("Unknown mode: %s. Available modes: streaming, parallel", mode)
	}
}
