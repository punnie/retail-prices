-- One row per `retail scrape` invocation. Unlike scrape_runs (inferred from
-- gaps in products.scraped_at and rebuilt by every refresh), this is an
-- explicit record of what the scraper was asked to do and how it went. The
-- next scrape's --since date comes from here, replacing the lastRun-<store>
-- files the old price-scraper NixOS module kept on disk.
CREATE TABLE IF NOT EXISTS scrape_jobs (
    id               BIGSERIAL PRIMARY KEY,
    store            TEXT NOT NULL,
    started_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at      TIMESTAMPTZ,
    since_date       DATE,
    status           TEXT NOT NULL DEFAULT 'running'
                     CHECK (status IN ('running', 'ok', 'partial', 'failed')),
    products_saved   INTEGER NOT NULL DEFAULT 0,
    products_failed  INTEGER NOT NULL DEFAULT 0,
    sources_failed   INTEGER NOT NULL DEFAULT 0,
    error            TEXT
);
CREATE INDEX IF NOT EXISTS idx_scrape_jobs_store ON scrape_jobs (store, started_at);
