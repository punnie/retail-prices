-- GiST trigram index for ordered "nearest name" lookups (the <-> KNN
-- distance operator), used by `retail match` to find each product's top-N
-- most similar names.
--
-- The existing GIN index (idx_retail_products_name_trgm) accelerates the %
-- similarity *filter* but can't drive an ordered LIMIT query: every row
-- that clears the threshold still has to be pulled out of the heap and
-- sorted by similarity() in memory before the LIMIT is applied. For short
-- grocery-product names sharing a lot of vocabulary ("leite", "iogurte",
-- pack-size tokens...), that's thousands of candidate rows per anchor
-- product even at a 0.35 threshold — fine for one product, not for a
-- 180k-product catalogue (`retail match` effectively never finished).
--
-- GiST does this properly: Postgres can walk the index directly in
-- nearest-first order and stop as soon as it has the N closest matches,
-- without ever materializing the rest.
CREATE INDEX IF NOT EXISTS idx_retail_products_name_trgm_gist
    ON retail_products USING gist (name_norm gist_trgm_ops);
