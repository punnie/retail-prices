# Portuguese Price Scraper

An extensible web scraper for Portuguese retail stores that collects product prices and stores them in a SQLite database for price tracking and comparison.

## Features

- **Extensible Architecture**: Easy to add new store scrapers
- **Multiple Store Support**: Currently supports Continente, Pingo Doce, and Auchan
- **SQLite Storage**: Persistent storage with price history tracking
- **Nix Development Environment**: Reproducible development setup
- **Search Functionality**: Search for specific products or scrape categories
- **Price History**: Track price changes over time

## Supported Stores

- **Continente** (`continente`) - Uses sitemap-based scraping for comprehensive product discovery
- **Pingo Doce** (`pingodoce`) - Traditional category and search-based scraping
- **Auchan** (`auchan`) - Traditional category and search-based scraping

## Prerequisites

- [Nix](https://nixos.org/download.html) with flakes enabled

## Setup

1. Clone the repository:
```bash
git clone <repository-url>
cd price-scraper
```

2. Enter the Nix development environment:
```bash
nix develop
```

3. Build the project:
```bash
go build .
```

## Usage

### Basic Usage

Scrape all products from a specific store:
```bash
./price-scraper continente
```

Search for specific products:
```bash
./price-scraper continente "leite"
./price-scraper pingodoce "pão"
./price-scraper auchan "água"
```

### Available Commands

- `./price-scraper <store_name>` - Scrape all categories from a store
- `./price-scraper <store_name> <search_term>` - Search for specific products

### Environment Variables

- `PRICE_SCRAPER_DB_PATH` - Custom database path (default: `~/.price-scraper/prices.db`)
- `PRICE_SCRAPER_USER_AGENT` - Custom user agent string

## Architecture

The scraper is built with an extensible architecture:

```
├── main.go                    # Entry point
├── internal/
│   ├── config/               # Configuration management
│   ├── storage/              # Database operations
│   └── scraper/              # Scraping logic
│       ├── scraper.go        # Main scraper interface and manager
│       ├── base.go           # Base scraper with common functionality
│       ├── continente.go     # Continente store scraper
│       ├── pingodoce.go      # Pingo Doce store scraper
│       └── auchan.go         # Auchan store scraper
```

## Adding New Stores

To add a new store scraper:

1. Create a new file in `internal/scraper/` (e.g., `newstore.go`)
2. Implement the `Scraper` interface:
   ```go
   type Scraper interface {
       GetName() string
       ScrapeProducts(searchTerm string) ([]*storage.Product, error)
       SetHTTPClient(client *http.Client)
   }
   ```
3. Embed `*BaseScraper` for common functionality
4. Register the scraper in `main.go`:
   ```go
   scraperManager.RegisterScraper("newstore", scraper.NewNewStoreScraper())
   ```

## Database Schema

The SQLite database stores products with the following schema:

```sql
CREATE TABLE products (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    store TEXT NOT NULL,
    name TEXT NOT NULL,
    price REAL NOT NULL,
    currency TEXT NOT NULL DEFAULT 'EUR',
    url TEXT,
    image_url TEXT,
    category TEXT,
    brand TEXT,
    description TEXT,
    scraped_at DATETIME NOT NULL,
    UNIQUE(store, name, price, scraped_at)
);
```

## Development

### Running Tests
```bash
go test ./...
```

### Building
```bash
go build .
```

### Code Structure

- **Config**: Manages application configuration and environment variables
- **Storage**: Handles database operations and data persistence
- **Scraper**: Contains the scraping logic with a pluggable architecture
  - **Manager**: Coordinates multiple scrapers
  - **Base**: Provides common functionality (HTTP requests, price parsing, etc.)
  - **Store-specific**: Individual scrapers for each supported store

## Important Notes

- **Respectful Scraping**: The scraper includes delays between requests to be respectful to the target websites
- **Error Handling**: Robust error handling with fallback mechanisms
- **Extensibility**: Easy to add new stores without modifying existing code
- **Data Deduplication**: Prevents duplicate entries using database constraints

## Legal Considerations

This tool is for educational and personal use only. Please ensure you comply with the terms of service of the websites you're scraping and respect robots.txt files. Always scrape responsibly and consider the impact on the target servers.

## License

This project is provided as-is for educational purposes. Please use responsibly and in accordance with applicable laws and website terms of service.
