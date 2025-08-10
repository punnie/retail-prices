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

	scraperManager := scraper.NewManager(store)

	// Register available scrapers
	scraperManager.RegisterScraper("continente", scraper.NewContinenteScraper())
	scraperManager.RegisterScraper("pingodoce", scraper.NewPingoDoceScraper())
	scraperManager.RegisterScraper("auchan", scraper.NewAuchanScraper())

	if len(os.Args) < 2 {
		fmt.Println("Usage: price-scraper <store_name> [product_search_term]")
		fmt.Println("Available stores: continente, pingodoce, auchan")
		os.Exit(1)
	}

	storeName := os.Args[1]
	searchTerm := ""
	if len(os.Args) > 2 {
		searchTerm = os.Args[2]
	}

	// Use streaming scraping for better memory usage and crash resilience
	if err := scraperManager.ScrapeStoreStreaming(storeName, searchTerm); err != nil {
		log.Fatalf("Failed to scrape store %s: %v", storeName, err)
	}

	fmt.Printf("Successfully scraped %s using streaming mode\n", storeName)
}
