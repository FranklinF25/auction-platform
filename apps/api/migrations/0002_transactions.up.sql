-- M4 checkout: one payment transaction per sold auction. Created by the
-- closing sweep (status pending) the moment an auction closes with a winner;
-- the winner then pays (simulated) within 48 hours or the expiry sweeper
-- moves it to expired. amount_cents is the final price at close; money stays
-- integer cents.

CREATE TABLE transactions (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- UNIQUE: exactly one transaction per auction (created inside the same
    -- FOR UPDATE transaction that closes the auction).
    auction_id   UUID NOT NULL UNIQUE REFERENCES auctions (id),
    winner_id    UUID NOT NULL REFERENCES users (id),
    amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
    status       TEXT NOT NULL CHECK (status IN ('pending', 'completed', 'failed', 'expired')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Payment window: the winner must pay before this instant.
    expires_at   TIMESTAMPTZ NOT NULL,
    paid_at      TIMESTAMPTZ
);

CREATE INDEX transactions_winner_idx ON transactions (winner_id);
-- The expiry sweeper's scan: pending/failed rows whose window has passed.
CREATE INDEX transactions_status_expires_idx ON transactions (status, expires_at);
