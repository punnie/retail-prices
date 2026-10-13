-- The raw observation log written by `retail scrape`. Until the scraper moved
-- into this repository it created this table itself at startup, so on an
-- existing database this is a no-op: IF NOT EXISTS leaves the live table and
-- its indexes exactly as they are. On a fresh database it creates the same
-- shape the scraper's Postgres storage used, with timestamptz for
-- scraped_at (the type the live data is exported with).
CREATE TABLE IF NOT EXISTS products (
    id          SERIAL PRIMARY KEY,
    store       VARCHAR(255) NOT NULL,
    name        TEXT NOT NULL,
    price       DECIMAL(10,2) NOT NULL,
    currency    VARCHAR(10) NOT NULL DEFAULT 'EUR',
    url         TEXT,
    image_url   TEXT,
    category    VARCHAR(255),
    brand       VARCHAR(255),
    description TEXT,
    scraped_at  TIMESTAMPTZ NOT NULL,
    UNIQUE (store, name, price, scraped_at)
);

CREATE INDEX IF NOT EXISTS idx_products_store ON products (store);
CREATE INDEX IF NOT EXISTS idx_products_name ON products (name);
CREATE INDEX IF NOT EXISTS idx_products_scraped_at ON products (scraped_at);
CREATE INDEX IF NOT EXISTS idx_products_store_name ON products (store, name);
