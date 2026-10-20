-- First-party accounts and opaque sessions for the preseeded judge account.
-- The public demo stays anonymous; a session is only attribution, not a wall.
-- Credentials are stored as bcrypt hashes; the plaintext stays in the
-- deployment secret manager.

CREATE TABLE IF NOT EXISTS accounts (
    id text PRIMARY KEY,
    email text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    display_name text NOT NULL DEFAULT '',
    is_judge boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sessions (
    token_hash text PRIMARY KEY,
    account_id text NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS sessions_account_id ON sessions (account_id);
CREATE INDEX IF NOT EXISTS sessions_expires_at ON sessions (expires_at);

ALTER TABLE campaigns
    ADD COLUMN IF NOT EXISTS account_id text REFERENCES accounts (id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS campaigns_account_id ON campaigns (account_id);
