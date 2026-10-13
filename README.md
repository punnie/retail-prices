# retail-prices

Prices scraped from four Portuguese retailers (Continente, Pingo Doce,
Auchan, Celeiro — see [`internal/scrape`](internal/scrape/README.md))
turned into an observational product catalogue: product pages with full
price history and a shape-based regular-price/promo decomposition (see
`PRICING_CONCEPTS.md`), plus a human-reviewed backend for linking the same
product across retailers.

The source table is an append-only observation log with no product identity
and no cross-retailer link, so most of the work is building a trustworthy
derived layer underneath the catalogue: product identity recovery (SKU from
URL, since retailers regenerate slugs constantly), data-quality quarantine
(retailers park unpublished products at placeholder prices), and the pricing
analysis itself (`internal/pricing`, a Go port of the reference classifier
vendored at `internal/pricing/reference/`).

This deliberately does **not** try to automatically classify every price
transition as a "discount" — an earlier version of this derived layer did
exactly that, and it produced nonsense (a bread roll "promotion" from
€55 to €0.89, €0.99→€1000 "price increases") because a high-low retailer's
raw price is a wave and a per-transition rule can't tell a promotion from a
retailer repricing. The regular-price/promo/average-paid decomposition in
`internal/pricing` fixes that by working in shape, not single transitions.

## Getting started

```sh
direnv allow      # or: nix develop
db-init           # first time only: initialize the local Postgres cluster
db-start          # start postgres, create the price_scraper database
db-psql -c "CREATE EXTENSION IF NOT EXISTS pg_trgm;"
db-restore /path/to/price_scraper-*.dump   # load the raw products table
```

`nix develop` exports `DATABASE_URL` pointing at the project-local cluster
(`.pgdata/`, socket in `.pgrun/`, both gitignored — no daemon, no docker).

## Commands

```sh
go run ./cmd/retail run <store>     # scrape a retailer, then refresh and match its slice of the catalogue
go run ./cmd/retail scrape <store>  # scrape only (see internal/scrape/README.md)
go run ./cmd/retail profile   # read-only data-quality report over the raw log
go run ./cmd/retail migrate   # apply database migrations
go run ./cmd/retail refresh   # rebuild retail_products, price_intervals, pricing analysis
go run ./cmd/retail match     # generate cross-retailer match candidates for human review
go run ./cmd/retail serve     # catalogue + product pages + matching review queue, on :8080
go run ./cmd/retail admin login-link --name <you>   # one-time administrator sign-in link
```

Stores are `continente`, `pingodoce`, `auchan` and `celeiro`.

`refresh` rebuilds one store at a time, each in its own transaction, so the
site keeps showing a store's previous catalogue until its new one is ready.
`refresh --store <store>` rebuilds just that store; `run` does that right
after each scrape. `refresh --pricing-only` skips the raw-log rescan and only
redoes placeholder derivation + pricing analysis — use it after changing
`pricing_params` or the classifier, without re-scanning 28M raw rows.

`match --store <store> --since <time>` only looks for matches of that store's
products first seen since then; `run` uses it for each scrape's new products.

Database tests run when `TEST_DATABASE_URL` points at a Postgres database
they may create throwaway schemas in (`go test ./...` skips them otherwise).

## Deploying on NixOS

The flake provides `packages.default` (the `retail` binary),
`overlays.default` and `nixosModules.default` (`services.retail-prices`). The
module runs migrations, the web app, and one pipeline service plus timer per
retailer, so each retailer's catalogue is refreshed as soon as its scrape
ends. See [`nix/example.nix`](nix/example.nix).

### Moving over from the price-scraper module

The scraper used to be its own repository with its own NixOS module
(`services.price-scraper`). To switch a host over:

1. Check the live raw table with `\d products`; migration 008 only creates it
   when it doesn't exist, so it never alters it.
2. Back up the database (`pg_dump -Fc`).
3. Disable `services.price-scraper` and enable `services.retail-prices` with
   `DATABASE_URL` pointing at the same database. With peer authentication,
   give the `retail-prices` user a role that owns or can write the tables.
4. Copy `/var/lib/price-scraper/lastRun-*` into `/var/lib/retail-prices/`, so
   each retailer's first scrape continues from where the old one stopped.
5. Run `retail refresh` once, then start one pipeline by hand
   (`systemctl start retail-prices-pipeline-celeiro`) and check the catalogue.
6. Replace the `github:punnie/price-scraper` flake input with this repository.

## Roles and moderation

Anyone visiting the site is an anonymous **guest**. Guests can browse
everything and help build the cross-retailer catalogue: answer suggested
pairs ("Same product" / "Not a match"), suggest links on the manual link
page, and report a wrong link from a product page. None of that changes the
catalogue directly. It all lands in a moderation queue (`/admin/moderation`).

An **administrator** can do all of the above with immediate effect, plus
bulk review, moderation and unlinking. There are no passwords. Sign in with a
single-use link printed on the server:

```sh
go run ./cmd/retail admin login-link --name pedro --base-url https://prices.example
go run ./cmd/retail admin revoke-sessions   # sign every administrator out everywhere
```

The link is valid for 15 minutes, and opening it only shows a "Sign in"
button, so chat or mail link previews can't use it up. Signing in lasts 30
days on that device. Every decision records who made it
(`match_decisions.decided_by`).

## Running it publicly

- Put it behind a reverse proxy (Caddy, nginx) that terminates TLS, and start
  it with `serve --trust-proxy` so the client IP and HTTPS come from
  `X-Forwarded-For`/`-Proto`. Never use `--trust-proxy` without a proxy in
  front, or clients could spoof their IP. Session cookies are only marked
  `Secure` on HTTPS requests, so plain-HTTP local development still works.
- Cross-site form posts are rejected (Go's `http.CrossOriginProtection`), so
  there are no CSRF tokens to manage.
- Guest submissions are rate-limited per IP (30/hour, 200/day), capped at 300
  pending per contributor, and screened by a honeypot field. IPs are only ever
  stored as a keyed hash (the key lives in `app_secrets`).
- Retailer images are served through the caching proxy at `/img` (1-month
  TTL), so visitors never hit retailer CDNs directly.

## Layout

```
db/migrations/      schema SQL (raw log, scrape jobs, derived layer), applied by `retail migrate`
cmd/retail/         CLI entrypoint
nix/                Nix package and NixOS module
internal/retailer/  the list of supported retailers: ids, stored names, SKU patterns
internal/scrape/    per-retailer scrapers; rawlog/ writes the raw products log
internal/pipeline/  `retail scrape` and `retail run`: scrape jobs, then refresh + match one store
internal/testdb/    throwaway migrated schemas for database tests
internal/db/        Postgres connection
internal/profile/   raw-data profiling (`retail profile`)
internal/normalize/ accents, pack sizes, unit prices, brands
internal/quality/   interval construction + per-product baseline/placeholder quarantine
internal/pricing/   regular price / promo / average-paid decomposition (see PRICING_CONCEPTS.md)
internal/refresh/   the `retail refresh` pipeline: raw log -> retail_products -> pricing analysis, per store
internal/matching/  cross-retailer candidate scoring + generation (`retail match`)
internal/auth/      administrator sign-in links and sessions, guest identity, rate limiting
internal/web/       catalogue, product pages, linking + moderation (`retail serve`)
```

`internal/basket` (shopping-list cost tracking) is schema-only
(`baskets`/`basket_items` in migration 002) — no code yet, left dormant.
