# retail-prices

A year of scraped prices from four Portuguese retailers (Continente, Pingo
Doce, Auchan, Celeiro — see [`price-scraper`](https://github.com/punnie/price-scraper))
turned into: a price-change report, cross-retailer product matching tooling,
and shopping-basket cost tracking/optimisation.

See `for-the-last-year-snoopy-popcorn.md`-style planning notes for the full
design rationale. Short version: the source table is an append-only
observation log with no product identity and no cross-retailer link, so most
of the work is building a trustworthy derived layer underneath the three
user-facing features.

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
go run ./cmd/retail profile   # read-only data-quality report over the raw log
```

`migrate`, `refresh`, `report`, and `serve` are not implemented yet.

## Layout

```
db/migrations/   derived-schema SQL, applied by `retail migrate`
cmd/retail/      CLI entrypoint
internal/db/     Postgres connection
internal/profile/  Step 2: raw-data profiling (see profile.go)
internal/normalize/  accents, pack sizes, unit prices, brands (planned)
internal/quality/    run/outlier/phantom classification (planned)
internal/events/     interval compaction, price-event derivation (planned)
internal/matching/   cross-retailer candidate generation + scoring (planned)
internal/basket/     cost-over-time, store-subset optimiser (planned)
internal/web/        handlers, templates (planned)
```
