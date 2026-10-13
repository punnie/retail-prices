# Portuguese Food Retailers Price Scraper

A high-performance web scraper for Portuguese online food retailers including Continente, Pingo Doce, Auchan, and Celeiro.

## Features

- **Parallel Processing**: Configurable worker pools for concurrent scraping
- **Rate Limiting**: Built-in rate limiting to respect server resources
- **Streaming Storage**: Products are saved to the database as they're scraped, reducing memory usage
- **Configurable Limits**: Flexible limits on sitemaps, products, and requests
- **Date Filtering**: Only scrape products modified after a given date
- **Multiple Databases**: Supports both SQLite and PostgreSQL

### Supported Stores

- **Continente** (`continente`) — Sitemap-based scraping
- **Pingo Doce** (`pingodoce`) — API-based scraping
- **Auchan** (`auchan`) — Sitemap-based scraping
- **Celeiro** (`celeiro`) — Sitemap-based scraping

## Installation

### Using Nix (Recommended)

This project provides both flakes and legacy Nix support.

#### With Nix Flakes

```bash
# Run directly from the repository
nix run github:punnie/price-scraper -- continente

# Install to your profile
nix profile install github:punnie/price-scraper

# Build locally
git clone <repository-url>
cd price-scraper
nix build
./result/bin/price-scraper continente
```

#### Legacy Nix

```bash
git clone <repository-url>
cd price-scraper
nix-build
./result/bin/price-scraper continente
```

#### Using as an Overlay

**With flakes:**

```nix
{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    price-scraper.url = "github:punnie/price-scraper";
  };

  outputs = { self, nixpkgs, price-scraper }: {
    nixosConfigurations.myhost = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        {
          nixpkgs.overlays = [ price-scraper.overlays.default ];
          environment.systemPackages = with pkgs; [
            price-scraper
          ];
        }
      ];
    };
  };
}
```

**Legacy (non-flake):**

```nix
let
  price-scraper-flake = builtins.getFlake "github:punnie/price-scraper";
  pkgs = import <nixpkgs> {
    overlays = [ price-scraper-flake.overlay ];
  };
in
{
  environment.systemPackages = with pkgs; [
    price-scraper
  ];
}
```

#### Integration in NixOS/Home Manager

**In your flake.nix:**

```nix
{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    price-scraper.url = "github:punnie/price-scraper";
  };

  outputs = { self, nixpkgs, price-scraper }: {
    nixosConfigurations.myhost = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        {
          environment.systemPackages = [
            price-scraper.packages.x86_64-linux.default
          ];
        }
      ];
    };
  };
}
```

**In Home Manager:**

```nix
{ pkgs, ... }:

{
  home.packages = [
    (pkgs.callPackage (pkgs.fetchFromGitHub {
      owner = "punnie";
      repo = "price-scraper";
      rev = "main";
      sha256 = "...";
    }) {})
  ];
}
```

### Manual Installation

```bash
git clone <repository-url>
cd price-scraper
go mod tidy
go build -o price-scraper main.go
```

## Usage

```bash
# Scrape Continente
./price-scraper continente

# Scrape Pingo Doce
./price-scraper pingodoce

# Scrape Auchan
./price-scraper auchan

# Scrape Celeiro
./price-scraper celeiro

# Only scrape products modified after a specific date
./price-scraper continente --since=2025-01-01

# Show help and current configuration
./price-scraper
```

### Configuration

The scraper is configured via environment variables. Copy `example.env` to `.env` and modify as needed:

```bash
cp example.env .env
```

#### Configuration Options

| Variable | Default | Description |
|----------|---------|-------------|
| `PRICE_SCRAPER_DB_TYPE` | `sqlite` | Database type: `sqlite` or `postgres` |
| `PRICE_SCRAPER_DB_PATH` | `~/.price-scraper/prices.db` | SQLite database path |
| `PRICE_SCRAPER_DB_CONNECTION_STRING` | (auto) | Database connection string |
| `PRICE_SCRAPER_USER_AGENT` | Chrome UA | HTTP User-Agent string |
| `PRICE_SCRAPER_HTTP_TIMEOUT` | `30s` | HTTP request timeout |
| `PRICE_SCRAPER_MAX_WORKERS` | `5` | Number of parallel workers |
| `PRICE_SCRAPER_REQUESTS_PER_SECOND` | `2.0` | Rate limit (requests per second) |
| `PRICE_SCRAPER_MAX_SITEMAPS` | `-1` | Max sitemaps to process (-1 = unlimited) |
| `PRICE_SCRAPER_MAX_PRODUCTS_TOTAL` | `-1` | Max total products (-1 = unlimited) |
| `PRICE_SCRAPER_MAX_PRODUCTS_PER_SITEMAP` | `100` | Max products per sitemap |
| `PRICE_SCRAPER_PAGE_SIZE` | `200` | Page size for API-based scrapers (Pingo Doce) |
| `PRICE_SCRAPER_PAGE_DISCOVERY_SIZE` | `1000` | Max pages to discover (Pingo Doce) |

#### Database Configuration

**SQLite (Default):**

```bash
export PRICE_SCRAPER_DB_TYPE=sqlite
export PRICE_SCRAPER_DB_PATH=/path/to/your/prices.db
```

**PostgreSQL:**

```bash
export PRICE_SCRAPER_DB_TYPE=postgres
export PRICE_SCRAPER_DB_CONNECTION_STRING="host=localhost port=5432 user=postgres password=yourpassword dbname=price_scraper sslmode=disable"
```

**PostgreSQL Setup:**

```sql
CREATE DATABASE price_scraper;
CREATE USER scraper_user WITH PASSWORD 'yourpassword';
GRANT ALL PRIVILEGES ON DATABASE price_scraper TO scraper_user;
```

The application will automatically create the necessary tables on first run.

#### Example Configurations

**Conservative (slow but respectful):**

```bash
export PRICE_SCRAPER_MAX_WORKERS=2
export PRICE_SCRAPER_REQUESTS_PER_SECOND=1.0
export PRICE_SCRAPER_MAX_PRODUCTS_PER_SITEMAP=50
```

**Aggressive (fast but may get blocked):**

```bash
export PRICE_SCRAPER_MAX_WORKERS=10
export PRICE_SCRAPER_REQUESTS_PER_SECOND=5.0
export PRICE_SCRAPER_MAX_PRODUCTS_PER_SITEMAP=200
```

**Testing (small sample):**

```bash
export PRICE_SCRAPER_MAX_SITEMAPS=2
export PRICE_SCRAPER_MAX_PRODUCTS_TOTAL=50
export PRICE_SCRAPER_MAX_PRODUCTS_PER_SITEMAP=25
```

## Architecture

### Core Components

- **Manager** — Orchestrates scraping operations, creates worker pools and rate limiters
- **BaseScraper** — Common functionality shared by all scrapers (HTTP fetching, XML/HTML parsing, JSON-LD extraction, rate limiting)
- **Individual Scrapers** — Store-specific implementations (CSS selectors, page structure)
- **WorkerPool** — Manages parallel workers with rate limiting and error handling
- **Storage** — Database abstraction layer supporting SQLite and PostgreSQL

### How It Works

1. The scraper fetches the store's sitemap index (or API endpoint for Pingo Doce)
2. Product sitemaps are filtered and limited per configuration
3. Each sitemap is processed in parallel using a worker pool
4. Individual product pages are fetched with rate limiting
5. Products are extracted from HTML (via CSS selectors and JSON-LD structured data)
6. Products are saved to the database as they're scraped

### Adding New Scrapers

Implement the `Scraper` interface:

```go
type Scraper interface {
    GetName() string
    ScrapeProducts(cfg *config.Config, storage storage.Storage, limiter *rate.Limiter) error
    SetHTTPClient(client *http.Client)
}
```

Register the scraper in `main.go`:

```go
scraperManager.RegisterScraper("newstore", scraper.NewNewStoreScraper())
```

### Testing

```bash
go test ./...

# Test with a small sample
PRICE_SCRAPER_MAX_PRODUCTS_TOTAL=10 ./price-scraper continente
```

## Troubleshooting

**Build Errors:**

```bash
go mod tidy
go build -o price-scraper main.go
```

**Rate Limiting / Getting Blocked:**

- Reduce `PRICE_SCRAPER_REQUESTS_PER_SECOND`
- Reduce `PRICE_SCRAPER_MAX_WORKERS`

**Memory Issues:**

- Reduce `PRICE_SCRAPER_MAX_PRODUCTS_TOTAL`
- Reduce `PRICE_SCRAPER_MAX_WORKERS`

**HTTP Timeouts:**

- Increase `PRICE_SCRAPER_HTTP_TIMEOUT`
- Reduce `PRICE_SCRAPER_MAX_WORKERS`

## License

[Add your license here]

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests
5. Submit a pull request