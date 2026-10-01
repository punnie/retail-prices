-- Replaces the discount-event classifier (price_events: promo_start /
-- promo_end / increase / decrease, anchored on a per-product baseline) with
-- the shape-based spell decomposition from PRICING_CONCEPTS.md: a product's
-- raw price is a wave, and no single-transition rule can tell a promotion
-- from a retailer repricing. See internal/pricing.
DROP TABLE IF EXISTS price_events;

-- One row per constant-price run (a "spell"), the output of
-- internal/pricing.Classify. regular_price is the carried-forward price this
-- spell is judged against; NULL for spells before the first regular spell is
-- ever established ("unknown" — a series that opens low could be mid-promo
-- or mid-staircase, and the data can't say which).
--
-- series_index increments on a series break (the product vanished for longer
-- than pricing_params.max_gap_days across complete scrape runs) so a
-- delisted-then-relisted product never carries a regular price across the
-- gap — see internal/pricing's deviation from the reference implementations,
-- neither of which handles this case.
CREATE TABLE IF NOT EXISTS price_spells (
    id                BIGSERIAL PRIMARY KEY,
    retail_product_id BIGINT NOT NULL REFERENCES retail_products(id) ON DELETE CASCADE,
    series_index      INTEGER NOT NULL DEFAULT 0,
    spell_index        INTEGER NOT NULL,
    price             NUMERIC NOT NULL,
    start_date        DATE NOT NULL,
    end_date          DATE NOT NULL,
    days              INTEGER NOT NULL,
    is_regular        BOOLEAN NOT NULL,
    regular_price     NUMERIC,
    label             TEXT NOT NULL CHECK (label IN ('regular', 'promo', 'pending', 'unknown', 'short_high'))
);
CREATE INDEX IF NOT EXISTS idx_price_spells_product ON price_spells (retail_product_id, start_date);
CREATE INDEX IF NOT EXISTS idx_price_spells_label ON price_spells (label, end_date);

-- One row per regular-price change: a real price increase, or a decrease
-- that outlasted the promo window and so was confirmed permanent. Dated at
-- the regular spell's start — for an increase revealed only on return from a
-- promo, that is the return date, not when the promo itself started.
CREATE TABLE IF NOT EXISTS regular_price_changes (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    retail_product_id BIGINT NOT NULL REFERENCES retail_products(id) ON DELETE CASCADE,
    change_date       DATE NOT NULL,
    old_price         NUMERIC NOT NULL,
    new_price         NUMERIC NOT NULL,
    pct_change        DOUBLE PRECISION NOT NULL,
    direction         TEXT NOT NULL CHECK (direction IN ('increase', 'permanent_decrease'))
);
CREATE INDEX IF NOT EXISTS idx_regular_price_changes_product ON regular_price_changes (retail_product_id, change_date);

-- One row per product: the product-page header in a single read, and the
-- sort/filter seam a future Discovery view would use. Rebuilt in full by
-- `retail refresh` alongside price_spells, never hand-edited.
CREATE TABLE IF NOT EXISTS product_pricing_snapshot (
    retail_product_id    BIGINT PRIMARY KEY REFERENCES retail_products(id) ON DELETE CASCADE,
    current_price         NUMERIC,
    current_label          TEXT,
    current_spell_days     INTEGER,
    regular_price          NUMERIC,
    regular_price_1y       NUMERIC,
    avg_paid_price         NUMERIC,
    avg_paid_price_1y      NUMERIC,
    promo_share            DOUBLE PRECISION,
    typical_promo_depth    DOUBLE PRECISION,
    typical_promo_days     DOUBLE PRECISION,
    promos_per_year        DOUBLE PRECISION,
    low_90d                NUMERIC,
    low_1y                 NUMERIC,
    last_regular_change_at  DATE,
    last_regular_change_pct DOUBLE PRECISION,
    has_enough_history      BOOLEAN NOT NULL DEFAULT false,
    last_observed_at        TIMESTAMPTZ
);

-- Spell-classifier parameters, overridable per store (store = '' is the
-- global default). Seeded with the reference implementation's defaults so
-- recalibration from real data (see PRICING_CONCEPTS.md §2) doesn't need a
-- recompile. Per-category overrides are left out: Continente's category is
-- empty on 100% of its rows, so a category dimension has no signal there.
CREATE TABLE IF NOT EXISTS pricing_params (
    store             TEXT PRIMARY KEY,
    min_reg_days      INTEGER NOT NULL DEFAULT 3,
    max_promo_days    INTEGER NOT NULL DEFAULT 56,
    trend_window_days INTEGER NOT NULL DEFAULT 84,
    max_gap_days      INTEGER NOT NULL DEFAULT 90
);
INSERT INTO pricing_params (store) VALUES ('') ON CONFLICT DO NOTHING;
