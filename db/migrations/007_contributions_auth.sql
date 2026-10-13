-- Public release: anonymous guest contributions go through a moderation
-- queue; an administrator signs in with a single-use link printed by
-- `retail admin login-link` and acts immediately.

-- Only SHA-256 hashes of tokens are stored, so a leaked database can't be
-- used to sign in.
CREATE TABLE IF NOT EXISTS admin_login_tokens (
    token_hash BYTEA PRIMARY KEY,
    admin_name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS admin_sessions (
    token_hash   BYTEA PRIMARY KEY,
    admin_name   TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A guest proposal. Every kind is about a pair of products, stored in
-- left<right order like match_decisions:
--   link       "these two are the same product"
--   not_match  "these two are not the same product" (a suggestion-queue vote)
--   unlink     "these two were linked, wrongly"
-- Nothing here changes the catalogue until an administrator accepts it.
CREATE TABLE IF NOT EXISTS contributions (
    id               BIGSERIAL PRIMARY KEY,
    kind             TEXT NOT NULL CHECK (kind IN ('link', 'not_match', 'unlink')),
    left_product_id  BIGINT NOT NULL REFERENCES retail_products(id) ON DELETE CASCADE,
    right_product_id BIGINT NOT NULL REFERENCES retail_products(id) ON DELETE CASCADE,
    note             TEXT NOT NULL DEFAULT '' CHECK (length(note) <= 500),
    contributor_id   TEXT NOT NULL,
    contributor_name TEXT NOT NULL DEFAULT '' CHECK (length(contributor_name) <= 40),
    ip_hash          BYTEA NOT NULL,
    status           TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'rejected')),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    reviewed_at      TIMESTAMPTZ,
    reviewed_by      TEXT,
    review_note      TEXT,
    CHECK (left_product_id < right_product_id)
);

-- One pending copy of a proposal per contributor; several contributors
-- making the same proposal stay separate rows and are counted together.
CREATE UNIQUE INDEX IF NOT EXISTS idx_contributions_pending_unique
    ON contributions (kind, left_product_id, right_product_id, contributor_id)
    WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_contributions_status ON contributions (status, created_at);
CREATE INDEX IF NOT EXISTS idx_contributions_contributor ON contributions (contributor_id, status);

-- Who made each decision: 'admin:<name>' or 'contribution:<id>'. Rows from
-- before this migration stay NULL (all made by the operator).
ALTER TABLE match_decisions ADD COLUMN IF NOT EXISTS decided_by TEXT;

-- Server-generated secrets that must stay stable across restarts (the key
-- used to hash guest IPs for rate limiting and abuse review).
CREATE TABLE IF NOT EXISTS app_secrets (
    name  TEXT PRIMARY KEY,
    value BYTEA NOT NULL
);
