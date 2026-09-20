-- Derived schema. The raw `products` table (the append-only scrape log) is
-- never modified by any of this — everything here is rebuilt by `retail
-- refresh` from that log and can be dropped and regenerated at any time.

-- One row per detected scrape run per store, from gap-clustering scraped_at
-- (see internal/profile). completeness_ratio lets every later query ignore
-- partial/aborted runs instead of treating "absent from this run" as meaningful.
CREATE TABLE IF NOT EXISTS scrape_runs (
    id                 BIGSERIAL PRIMARY KEY,
    store              TEXT NOT NULL,
    run_started        TIMESTAMPTZ NOT NULL,
    run_ended          TIMESTAMPTZ NOT NULL,
    observation_count  INTEGER NOT NULL,
    completeness_ratio DOUBLE PRECISION NOT NULL,
    is_complete        BOOLEAN NOT NULL,
    UNIQUE (store, run_started)
);
CREATE INDEX IF NOT EXISTS idx_scrape_runs_store ON scrape_runs (store, run_started);

-- The product dimension. Identity is (store, sku), with sku recovered from
-- the URL (see internal/normalize) since the raw log has no product id.
CREATE TABLE IF NOT EXISTS retail_products (
    id                BIGSERIAL PRIMARY KEY,
    store             TEXT NOT NULL,
    sku               TEXT NOT NULL,
    url               TEXT NOT NULL,
    name              TEXT NOT NULL,
    brand             TEXT,
    category          TEXT,
    image_url         TEXT,
    name_norm         TEXT NOT NULL,
    brand_norm        TEXT,
    pack_value        NUMERIC,
    pack_unit         TEXT,
    unit_count        INTEGER,
    is_phantom        BOOLEAN NOT NULL DEFAULT false,
    first_seen        TIMESTAMPTZ NOT NULL,
    last_seen         TIMESTAMPTZ NOT NULL,
    observation_count INTEGER NOT NULL,
    UNIQUE (store, sku)
);
CREATE INDEX IF NOT EXISTS idx_retail_products_name_trgm
    ON retail_products USING gin (name_norm gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_retail_products_brand_pack
    ON retail_products (brand_norm, pack_value, pack_unit);

-- Run-length-encoded price series: consecutive identical prices collapse to
-- one row, so tens of millions of raw observations become a few million
-- intervals and every downstream query (reports, baskets) reads this instead
-- of the raw log.
CREATE TABLE IF NOT EXISTS price_intervals (
    id                BIGSERIAL PRIMARY KEY,
    retail_product_id BIGINT NOT NULL REFERENCES retail_products(id) ON DELETE CASCADE,
    price             NUMERIC NOT NULL,
    valid_from        TIMESTAMPTZ NOT NULL,
    valid_to          TIMESTAMPTZ NOT NULL,
    observation_count INTEGER NOT NULL,
    is_suspect        BOOLEAN NOT NULL DEFAULT false
);
CREATE INDEX IF NOT EXISTS idx_price_intervals_product
    ON price_intervals (retail_product_id, valid_from);

-- One row per transition between consecutive price_intervals.
CREATE TABLE IF NOT EXISTS price_events (
    id                BIGSERIAL PRIMARY KEY,
    retail_product_id BIGINT NOT NULL REFERENCES retail_products(id) ON DELETE CASCADE,
    old_price         NUMERIC,
    new_price         NUMERIC NOT NULL,
    pct_change        DOUBLE PRECISION,
    changed_at        TIMESTAMPTZ NOT NULL,
    kind              TEXT NOT NULL CHECK (kind IN ('promo_start', 'promo_end', 'increase', 'decrease', 'suspect')),
    confidence        DOUBLE PRECISION NOT NULL DEFAULT 1.0
);
CREATE INDEX IF NOT EXISTS idx_price_events_product ON price_events (retail_product_id, changed_at);
CREATE INDEX IF NOT EXISTS idx_price_events_kind ON price_events (kind, changed_at);

-- Human-confirmed cross-retailer product identity (Step 5). A group holds up
-- to one member per store; membership is exclusive (a product belongs to at
-- most one group), enforced by the UNIQUE on retail_product_id.
CREATE TABLE IF NOT EXISTS product_groups (
    id         BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS product_group_members (
    product_group_id  BIGINT NOT NULL REFERENCES product_groups(id) ON DELETE CASCADE,
    retail_product_id BIGINT NOT NULL REFERENCES retail_products(id) ON DELETE CASCADE,
    PRIMARY KEY (product_group_id, retail_product_id),
    UNIQUE (retail_product_id)
);

-- Generated candidate pairs awaiting human review, and the persisted
-- decisions so a rejected pair is never re-queued.
CREATE TABLE IF NOT EXISTS match_candidates (
    id               BIGSERIAL PRIMARY KEY,
    left_product_id  BIGINT NOT NULL REFERENCES retail_products(id) ON DELETE CASCADE,
    right_product_id BIGINT NOT NULL REFERENCES retail_products(id) ON DELETE CASCADE,
    score            DOUBLE PRECISION NOT NULL,
    blocking_reason  TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (left_product_id, right_product_id)
);
CREATE INDEX IF NOT EXISTS idx_match_candidates_score ON match_candidates (score DESC);

CREATE TABLE IF NOT EXISTS match_decisions (
    left_product_id  BIGINT NOT NULL REFERENCES retail_products(id) ON DELETE CASCADE,
    right_product_id BIGINT NOT NULL REFERENCES retail_products(id) ON DELETE CASCADE,
    decision         TEXT NOT NULL CHECK (decision IN ('matched', 'rejected')),
    decided_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (left_product_id, right_product_id)
);

-- Shopping baskets. An item references a product_group where cross-retailer
-- identity has been established, otherwise a single retail_product.
CREATE TABLE IF NOT EXISTS baskets (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS basket_items (
    id                 BIGSERIAL PRIMARY KEY,
    basket_id          BIGINT NOT NULL REFERENCES baskets(id) ON DELETE CASCADE,
    product_group_id   BIGINT REFERENCES product_groups(id) ON DELETE CASCADE,
    retail_product_id  BIGINT REFERENCES retail_products(id) ON DELETE CASCADE,
    quantity           NUMERIC NOT NULL DEFAULT 1,
    CHECK (
        (product_group_id IS NOT NULL AND retail_product_id IS NULL) OR
        (product_group_id IS NULL AND retail_product_id IS NOT NULL)
    )
);

-- Singleton watermark row: the highest raw products.id already folded into
-- the derived layer, so `retail refresh` can process new rows incrementally.
CREATE TABLE IF NOT EXISTS refresh_state (
    id          BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
    last_raw_id BIGINT NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO refresh_state (id) VALUES (true) ON CONFLICT DO NOTHING;
