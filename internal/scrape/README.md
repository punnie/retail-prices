# Scrapers

Scrapers for Portuguese online food retailers. Each run appends one price
observation per product page to the raw `products` table, which `retail
refresh` turns into the catalogue. This code was the standalone
[`price-scraper`](https://github.com/punnie/price-scraper) repository until it
moved here with its history.

| Store | id | How it scrapes |
|---|---|---|
| Continente | `continente` | Sitemap index, then product pages |
| Pingo Doce | `pingodoce` | Paged product grid (ignores `--since`) |
| Auchan | `auchan` | Sitemap index, then product pages |
| Celeiro | `celeiro` | Sitemap index, then product pages |

## Running

```sh
go run ./cmd/retail scrape continente                    # scrape only
go run ./cmd/retail scrape continente --since=2026-10-01
go run ./cmd/retail run continente                       # scrape, then refresh + match that store
```

Without `--since`, a scrape only fetches sitemap entries modified since the
start date of that store's last successful scrape in `scrape_jobs` (twenty
years back for a store's first scrape). Every scrape is recorded there as
`ok`, `partial` (a sitemap or page failed, or more than 20% of product pages
failed) or `failed` (nothing could be scraped); a failed scrape exits non-zero.

## Configuration

The database is `DATABASE_URL`, as for every `retail` command
(`PRICE_SCRAPER_DB_CONNECTION_STRING` is still read when it is unset). Tuning
comes from these environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `PRICE_SCRAPER_USER_AGENT` | Chrome UA | HTTP User-Agent string |
| `PRICE_SCRAPER_HTTP_TIMEOUT` | `30s` | HTTP request timeout |
| `PRICE_SCRAPER_MAX_WORKERS` | `5` | Number of parallel workers |
| `PRICE_SCRAPER_REQUESTS_PER_SECOND` | `2.0` | Rate limit (requests per second) |
| `PRICE_SCRAPER_MAX_SITEMAPS` | `-1` | Max sitemaps to process (-1 = unlimited) |
| `PRICE_SCRAPER_MAX_PRODUCTS_TOTAL` | `-1` | Max total products (-1 = unlimited) |
| `PRICE_SCRAPER_MAX_PRODUCTS_PER_SITEMAP` | `100` | Max products per sitemap (-1 = unlimited) |
| `PRICE_SCRAPER_PAGE_SIZE` | `200` | Page size for Pingo Doce's grid |
| `PRICE_SCRAPER_PAGE_DISCOVERY_SIZE` | `1000` | Max grid pages to discover (Pingo Doce) |

For a quick test against a scratch database:

```sh
PRICE_SCRAPER_MAX_SITEMAPS=1 PRICE_SCRAPER_MAX_PRODUCTS_TOTAL=10 go run ./cmd/retail scrape celeiro
```

## How it works

1. The scraper fetches the store's sitemap index (or product grid for Pingo Doce).
2. Product sitemaps are filtered by `--since` and limited per configuration.
3. Sitemaps are processed in parallel through a worker pool sharing one rate limiter.
4. Products are extracted from each page (CSS selectors and JSON-LD) and saved
   as they are scraped (`rawlog.Writer`), which also counts failures.

## Adding a retailer

1. Add it to `internal/retailer` (id, stored name, SKU pattern in its product URLs).
2. Implement `Scraper` here (embed `BaseScraper`) and register it in
   `constructors` in `scraper.go`. `TestEveryRetailerHasAScraper` checks the two agree.
3. Add it to `services.retail-prices.retailers` on the server.
