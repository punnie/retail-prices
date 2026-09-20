-- The observation-weighted median of a product's own price history: its
-- "normal" price. Everything in the quality layer is judged relative to this
-- rather than to the immediately preceding price, because when the previous
-- price is itself a retailer placeholder (Continente parks unpublished
-- products at 55.00, 349.00, 500.00, ...) a prev-anchored comparison inverts:
-- placeholder -> real price reads as a promotion, and real price ->
-- placeholder reads as the promotion ending.
ALTER TABLE retail_products ADD COLUMN IF NOT EXISTS baseline_price NUMERIC;
