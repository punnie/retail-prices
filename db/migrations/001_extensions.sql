-- pg_trgm powers the fuzzy name matching used for cross-retailer product
-- candidate generation (blocking pass 2).
CREATE EXTENSION IF NOT EXISTS pg_trgm;
