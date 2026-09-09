-- Initial schema for the auction platform: users, sessions, auctions, bids.
-- Money is integer cents (BIGINT); auctions use a CHECK-constrained status.

CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    name          TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Email uniqueness is case-insensitive; the application normalizes to lower
-- case before insert, and this index is the concurrency authority.
CREATE UNIQUE INDEX users_email_lower_idx ON users (lower(email));

-- Sessions store only the SHA-256 hash of the raw cookie token.
CREATE TABLE sessions (
    token_hash TEXT PRIMARY KEY,
    user_id    UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

CREATE TABLE auctions (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    seller_id            UUID NOT NULL REFERENCES users (id),
    title                TEXT NOT NULL,
    description          TEXT NOT NULL DEFAULT '',
    starting_price_cents BIGINT NOT NULL CHECK (starting_price_cents > 0),
    min_increment_cents  BIGINT NOT NULL CHECK (min_increment_cents >= 1),
    reserve_price_cents  BIGINT CHECK (reserve_price_cents IS NULL OR reserve_price_cents >= starting_price_cents),
    status               TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'closed', 'cancelled')),
    ends_at              TIMESTAMPTZ NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at            TIMESTAMPTZ
);
CREATE INDEX auctions_status_ends_at_idx ON auctions (status, ends_at);

CREATE TABLE bids (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    auction_id   UUID NOT NULL REFERENCES auctions (id) ON DELETE CASCADE,
    bidder_id    UUID NOT NULL REFERENCES users (id),
    amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX bids_auction_created_idx ON bids (auction_id, created_at DESC);
