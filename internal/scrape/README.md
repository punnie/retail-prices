# Portuguese Food Retailers Price Scraper

A high-performance, configurable web scraper for Portuguese online food retailers including Continente, Pingo Doce, and Auchan.

## Features

### Performance Improvements
- **Parallel Processing**: Configurable worker pools for concurrent scraping
- **Rate Limiting**: Built-in rate limiting to respect server resources
- **Streaming Storage**: Memory-efficient streaming storage to handle large datasets
- **Configurable Limits**: Flexible limits on sitemaps, products, and requests

### Supported Stores
- **Continente** (`continente`) - Sitemap-based scraping with parallel support
- **Pingo Doce** (`pingodoce`) - API-based scraping
- **Auchan** (`auchan`) - Sitemap-based scraping with parallel support

### Scraping Modes
- **Streaming Mode** (default): Sequential processing with streaming storage
- **Parallel Mode**: Concurrent processing with worker pools and rate limiting

## Installation

### Using Nix (Recommended)

This project provides both flakes and legacy Nix support for easy installation and integration.

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
# Build with nix-build
git clone <repository-url>
cd price-scraper
nix-build
./result/bin/price-scraper continente

# Install to your profile
nix-env -f . -i
```

#### Using as an Overlay

This project provides a Nix overlay that makes the `price-scraper` package available in your nixpkgs instance:

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
            price-scraper  # Now available via overlay
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
    price-scraper  # Available via overlay
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

**In configuration.nix (legacy):**
```nix
{ pkgs, ... }:

let
  price-scraper = pkgs.callPackage (pkgs.fetchFromGitHub {
    owner = "punnie";
    repo = "price-scraper";
    rev = "main"; # or specific commit
    sha256 = "..."; # nix-prefetch-url --unpack <github-url>
  }) {};
in
{
  environment.systemPackages = [ price-scraper ];
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

### Basic Usage

```bash
# Scrape Continente using streaming mode (default)
./price-scraper continente

# Scrape Pingo Doce using parallel mode
./price-scraper pingodoce parallel

# Show help and current configuration
./price-scraper
```

### Configuration

The scraper is configured via environment variables. Copy `example.env` to `.env` and modify as needed:

```bash
cp example.env .env
# Edit .env with your preferred settings
```

#### Configuration Options

| Variable | Default | Description |
|----------|---------|-------------|
| `PRICE_SCRAPER_DB_PATH` | `~/.price-scraper/prices.db` | SQLite database path |
| `PRICE_SCRAPER_USER_AGENT` | Chrome UA | HTTP User-Agent string |
| `PRICE_SCRAPER_HTTP_TIMEOUT` | `30s` | HTTP request timeout |
| `PRICE_SCRAPER_MAX_WORKERS` | `5` | Number of parallel workers |
| `PRICE_SCRAPER_REQUEST_DELAY` | `500ms` | Delay between requests (streaming mode) |
| `PRICE_SCRAPER_REQUESTS_PER_SECOND` | `2.0` | Rate limit for parallel mode |
| `PRICE_SCRAPER_MAX_SITEMAPS` | `-1` | Max sitemaps to process (-1 = unlimited) |
| `PRICE_SCRAPER_MAX_PRODUCTS_TOTAL` | `-1` | Max total products (-1 = unlimited) |
| `PRICE_SCRAPER_MAX_PRODUCTS_PER_SITEMAP` | `100` | Max products per sitemap |

#### Example Configurations

**Conservative (slow but respectful):**
```bash
export PRICE_SCRAPER_MAX_WORKERS=2
export PRICE_SCRAPER_REQUEST_DELAY=1s
export PRICE_SCRAPER_REQUESTS_PER_SECOND=1.0
export PRICE_SCRAPER_MAX_PRODUCTS_PER_SITEMAP=50
```

**Aggressive (fast but may get blocked):**
```bash
export PRICE_SCRAPER_MAX_WORKERS=10
export PRICE_SCRAPER_REQUEST_DELAY=100ms
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

- **Manager**: Orchestrates scraping operations and manages worker pools
- **BaseScraper**: Common functionality for all scrapers (HTTP, parsing, rate limiting)
- **Individual Scrapers**: Store-specific implementations
- **WorkerPool**: Manages parallel workers with rate limiting
- **Storage**: SQLite-based product storage

### Parallel Processing

The parallel mode uses:
- **Worker Pools**: Configurable number of concurrent workers
- **Rate Limiting**: Token bucket algorithm to control request rate
- **Context Cancellation**: Graceful shutdown and timeout handling
- **Atomic Counters**: Thread-safe progress tracking

### Performance Optimizations

1. **Streaming Storage**: Products are saved as they're scraped, reducing memory usage
2. **Rate Limiting**: Prevents overwhelming target servers
3. **Connection Reuse**: HTTP client with connection pooling
4. **Configurable Limits**: Prevents runaway scraping jobs
5. **Progress Reporting**: Real-time feedback on scraping progress

## Development

### Adding New Scrapers

1. Implement the `Scraper` interface:
   ```go
   type Scraper interface {
       GetName() string
       ScrapeProducts() ([]*storage.Product, error)
       ScrapeProductsWithCallback(callback ProductCallback) error
       SetHTTPClient(client *http.Client)
   }
   ```

2. For parallel support, also implement `ParallelScraper`:
   ```go
   type ParallelScraper interface {
       Scraper
       ScrapeProductsParallel(cfg *config.Config, storage storage.Storage, limiter *rate.Limiter) error
   }
   ```

3. Register the scraper in `main.go`:
   ```go
   scraperManager.RegisterScraper("newstore", scraper.NewNewStoreScraper())
   ```

### Testing

```bash
# Build and test
go build -o price-scraper main.go

# Test with small limits
export PRICE_SCRAPER_MAX_PRODUCTS_TOTAL=10
./price-scraper continente streaming

# Test parallel mode
./price-scraper continente parallel
```

## Performance Comparison

### Before (Sequential)
- Single-threaded processing
- Fixed 1-2 second delays
- Hardcoded limits (100 products)
- No rate limiting
- Memory inefficient

### After (Parallel)
- Configurable worker pools (default: 5 workers)
- Intelligent rate limiting (default: 2 req/sec)
- Flexible limits via environment variables
- Memory-efficient streaming
- 3-5x performance improvement typical

## Best Practices

1. **Start Conservative**: Use default settings initially
2. **Monitor Performance**: Watch for HTTP errors or blocks
3. **Respect Servers**: Don't set overly aggressive limits
4. **Use Streaming**: For large datasets, always use streaming mode
5. **Configure Limits**: Set appropriate limits for your use case

## Troubleshooting

### Common Issues

**Build Errors:**
```bash
go mod tidy
go build -o price-scraper main.go
```

**Rate Limiting:**
- Reduce `PRICE_SCRAPER_REQUESTS_PER_SECOND`
- Increase `PRICE_SCRAPER_REQUEST_DELAY`
- Reduce `PRICE_SCRAPER_MAX_WORKERS`

**Memory Issues:**
- Use streaming mode instead of parallel
- Reduce `PRICE_SCRAPER_MAX_PRODUCTS_TOTAL`
- Reduce `PRICE_SCRAPER_MAX_WORKERS`

**HTTP Timeouts:**
- Increase `PRICE_SCRAPER_HTTP_TIMEOUT`
- Reduce concurrency settings

## License

[Add your license here]

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests
5. Submit a pull request

## Changelog

### v2.0.0 (Current)
- Added parallel processing with worker pools
- Implemented configurable rate limiting
- Added comprehensive configuration system
- Improved memory efficiency with streaming
- Enhanced error handling and progress reporting
- Added support for unlimited scraping options

### v1.0.0
- Basic sequential scraping
- Fixed delays and limits
- Simple streaming storage
