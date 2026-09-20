-- Prices a store demonstrably uses as placeholders for unpublished or
-- unavailable products, derived from the data rather than hardcoded.
--
-- The discriminator is near_baseline_pct: of all the products that ever sat
-- at this exact price, what share of them actually *normally* cost about this
-- much (baseline within 2x)? A genuine shelf price scores high — Continente's
-- 21.99 is 89%, Auchan's 2.99 is 99%. A placeholder scores low, because it
-- gets attached to products whose real prices are all over the map:
-- Continente's 500.00 scores 0.5%, its 349.00 scores 0.6%, and Auchan's 1.90
-- scores 36% across 4,189 products.
--
-- Counting how often a price recurs per product is NOT enough on its own: it
-- would wrongly flag Continente's 12.53 and 21.99 (genuine, ~1.0 intervals
-- per product because those products simply never returned to that price)
-- and wrongly clear Continente's placeholder use of common prices like 2.99
-- and 4.49, which it uses both genuinely and as filler.
CREATE TABLE IF NOT EXISTS store_placeholder_prices (
    store              TEXT NOT NULL,
    price              NUMERIC NOT NULL,
    products           INTEGER NOT NULL,
    near_baseline_pct  DOUBLE PRECISION NOT NULL,
    PRIMARY KEY (store, price)
);
