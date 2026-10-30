-- One brand profile per account: what the brand is, so a pitch argues why
-- the brand fits a door. The SKU list is a JSON array of {id, name}.
CREATE TABLE IF NOT EXISTS brand_profiles (
    account_id text PRIMARY KEY REFERENCES accounts (id) ON DELETE CASCADE,
    name text NOT NULL DEFAULT '',
    category text NOT NULL DEFAULT '',
    description text NOT NULL DEFAULT '',
    positioning text NOT NULL DEFAULT '',
    audience text NOT NULL DEFAULT '',
    skus_json jsonb NOT NULL DEFAULT '[]',
    updated_at timestamptz NOT NULL DEFAULT now()
);
