-- The idempotency table grows with every create. The gateway sweeps the rows
-- older than the retention window, and this index keeps that sweep cheap.
CREATE INDEX IF NOT EXISTS idempotency_keys_created_at_idx
    ON idempotency_keys (created_at);
