-- Spell-based regular price / promo classifier for high-low retail pricing.
-- Same logic as price_spells.py. Written for PostgreSQL; input table prices(product_id, date, price).
-- Gaps in the daily data are fine: a spell runs until the next price change.
-- Parameters: 3 = MIN_REG_DAYS, 56 = MAX_PROMO_DAYS, 83 = TREND_WINDOW - 1. Search and replace to tune.
-- DuckDB: wrap generate_series(...) in unnest(). BigQuery/Snowflake: swap date arithmetic for DATE_DIFF/DATEDIFF.

CREATE TABLE price_spells AS
WITH flagged AS (
    SELECT *, CASE WHEN price = LAG(price) OVER w THEN 0 ELSE 1 END AS changed
    FROM prices
    WINDOW w AS (PARTITION BY product_id ORDER BY date)
),
numbered AS (
    SELECT *, SUM(changed) OVER (PARTITION BY product_id ORDER BY date) AS spell_id
    FROM flagged
),
raw_runs AS (
    SELECT product_id, spell_id, MIN(price) AS price, MIN(date) AS start_date, MAX(date) AS last_seen
    FROM numbered
    GROUP BY product_id, spell_id
),
-- Drop short spikes (a 1-2 day high above both neighbours) so a glitch can't split a regular
-- spell in two, then re-merge. The glitch days are absorbed into the preceding spell.
raw_spells AS (
    SELECT *, COALESCE(LEAD(start_date) OVER w - 1, last_seen) - start_date + 1 AS days,
           LAG(price) OVER w AS prev_price, LEAD(price) OVER w AS next_price,
           MAX(last_seen) OVER (PARTITION BY product_id) AS series_end
    FROM raw_runs
    WINDOW w AS (PARTITION BY product_id ORDER BY spell_id)
),
deglitched AS (
    SELECT product_id, price, start_date, series_end,
           CASE WHEN price = LAG(price) OVER w THEN 0 ELSE 1 END AS changed
    FROM raw_spells
    WHERE NOT COALESCE(days < 3 AND price > prev_price AND price > next_price, FALSE)
    WINDOW w AS (PARTITION BY product_id ORDER BY start_date)
),
renumbered AS (
    SELECT *, SUM(changed) OVER (PARTITION BY product_id ORDER BY start_date) AS spell_id
    FROM deglitched
),
runs AS (
    SELECT product_id, spell_id, MIN(price) AS price, MIN(start_date) AS start_date,
           MAX(series_end) AS last_seen
    FROM renumbered
    GROUP BY product_id, spell_id
),
spells AS (
    SELECT product_id, spell_id, price, start_date,
           COALESCE(LEAD(start_date) OVER w - 1, last_seen)                  AS end_date,
           COALESCE(LEAD(start_date) OVER w - 1, last_seen) - start_date + 1 AS days,
           LAG(price)  OVER w AS prev_price,
           LEAD(price) OVER w AS next_price,
           LEAD(spell_id) OVER w IS NULL AS is_last
    FROM runs
    WINDOW w AS (PARTITION BY product_id ORDER BY spell_id)
),
-- Pass 1: regular = a local high that persists, or any spell too long to be a promo
pass1 AS (
    SELECT *,
           ((prev_price IS NULL OR price > prev_price)
             AND (next_price IS NULL OR price > next_price)
             AND days >= 3)
           OR days > 56 AS is_reg
    FROM spells
),
-- Pass 2: V-filter on the regular candidates. A lower high sandwiched between two
-- equal-or-higher highs within the promo window is a shallow promo, not a price cut.
cands AS (
    SELECT product_id, spell_id, price, days,
           LAG(price)       OVER w AS prev_reg,
           LEAD(price)      OVER w AS next_reg,
           LEAD(start_date) OVER w - LAG(end_date) OVER w - 1 AS gap
    FROM pass1
    WHERE is_reg
    WINDOW w AS (PARTITION BY product_id ORDER BY spell_id)
),
pass2 AS (
    SELECT p.*,
           p.is_reg AND NOT COALESCE(c.price < c.prev_reg AND c.next_reg >= c.prev_reg
                                     AND c.gap <= 56 AND c.days <= 56, FALSE) AS is_reg2
    FROM pass1 p
    LEFT JOIN cands c USING (product_id, spell_id)
),
-- Carry the last regular price forward (COUNT ignores NULLs, so it only ticks on regular spells)
carry2 AS (
    SELECT *, COUNT(CASE WHEN is_reg2 THEN 1 END) OVER (PARTITION BY product_id ORDER BY spell_id) AS grp
    FROM pass2
),
-- Pass 3: staircase increases. A persistent spell at or above the carried regular can't be a promo.
pass3 AS (
    SELECT *,
           is_reg2 OR (days >= 3 AND price >= MAX(CASE WHEN is_reg2 THEN price END)
                                              OVER (PARTITION BY product_id, grp)) AS is_reg3
    FROM carry2
),
carry3 AS (
    SELECT *, COUNT(CASE WHEN is_reg3 THEN 1 END) OVER (PARTITION BY product_id ORDER BY spell_id) AS grp3
    FROM pass3
),
final AS (
    SELECT *, MAX(CASE WHEN is_reg3 THEN price END) OVER (PARTITION BY product_id, grp3) AS regular_price
    FROM carry3
)
SELECT product_id, spell_id, price, start_date, end_date, days,
       is_reg3 AS is_reg, regular_price,
       CASE WHEN is_reg3                           THEN 'regular'
            WHEN regular_price IS NULL             THEN 'unknown'   -- before the first regular spell
            WHEN is_last AND price < regular_price THEN 'pending'   -- too recent to call
            WHEN price < regular_price             THEN 'promo'
            ELSE 'short_high' END AS label
FROM final;


-- Regular price change events: increases and permanent decreases
CREATE TABLE regular_price_changes AS
SELECT * FROM (
    SELECT product_id, start_date AS change_date,
           LAG(price) OVER w AS old_price, price AS new_price,
           price / LAG(price) OVER w - 1 AS pct_change
    FROM price_spells
    WHERE is_reg
    WINDOW w AS (PARTITION BY product_id ORDER BY spell_id)
) t
WHERE old_price IS NOT NULL AND new_price <> old_price;


-- Daily series with the three trend metrics (spells expanded to days, so gaps are filled)
CREATE TABLE price_daily AS
WITH days AS (
    SELECT product_id, price, regular_price, label,
           generate_series(start_date, end_date, INTERVAL '1 day')::date AS date
    FROM price_spells
)
SELECT product_id, date, price, regular_price, label,
       AVG(price) OVER w                                                     AS avg_paid_price,
       AVG(CASE WHEN label IN ('promo', 'pending') THEN 1.0 ELSE 0.0 END) OVER w AS promo_share,
       AVG(CASE WHEN label IN ('promo', 'pending') THEN 1 - price / regular_price END) OVER w AS avg_promo_depth
FROM days
WINDOW w AS (PARTITION BY product_id ORDER BY date ROWS BETWEEN 83 PRECEDING AND CURRENT ROW);


-- "Has the price moved since date T?"
-- SELECT n.product_id, t.regular_price AS regular_then, n.regular_price AS regular_now,
--        n.regular_price / t.regular_price - 1 AS regular_pct,
--        n.avg_paid_price / t.avg_paid_price - 1 AS avg_paid_pct, n.label AS current_status
-- FROM price_daily n
-- JOIN price_daily t USING (product_id)
-- WHERE t.date = DATE '2026-02-01'
--   AND n.date = (SELECT MAX(date) FROM price_daily d WHERE d.product_id = n.product_id);
