-- Forward-only schema for campaigns, runs, evidence snapshots, drafts, and
-- trace events. A re-run inserts a new runs row and does not update the
-- previous snapshot.

CREATE TABLE campaigns (
    id text PRIMARY KEY,
    preset_id text NOT NULL DEFAULT '',
    mode text NOT NULL,
    city text NOT NULL,
    latitude double precision,
    longitude double precision,
    radius_meters integer NOT NULL,
    brand_name text NOT NULL,
    seed_json jsonb NOT NULL DEFAULT '[]',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE runs (
    id text PRIMARY KEY,
    campaign_id text NOT NULL REFERENCES campaigns (id),
    preset_id text NOT NULL DEFAULT '',
    state text NOT NULL,
    free_entry boolean NOT NULL DEFAULT false,
    baseline_text text NOT NULL DEFAULT '',
    qloo_payload jsonb NOT NULL DEFAULT '{}',
    grounding_payload jsonb NOT NULL DEFAULT '{}',
    trace_json jsonb NOT NULL DEFAULT '[]',
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX runs_preset_ready ON runs (preset_id, created_at DESC);
CREATE INDEX runs_campaign_created ON runs (campaign_id, created_at DESC);

CREATE TABLE evidence_snapshots (
    id text PRIMARY KEY,
    run_id text NOT NULL REFERENCES runs (id),
    evidence_json jsonb NOT NULL,
    frozen_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE drafts (
    id text PRIMARY KEY,
    run_id text NOT NULL REFERENCES runs (id),
    door_id text NOT NULL DEFAULT '',
    kind text NOT NULL,
    body text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE trace_events (
    run_id text NOT NULL REFERENCES runs (id),
    sequence bigint NOT NULL,
    cursor text NOT NULL,
    stage text NOT NULL,
    elapsed_ms bigint NOT NULL,
    cache_hit boolean NOT NULL,
    detail text NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, sequence)
);

CREATE TABLE idempotency_keys (
    key text PRIMARY KEY,
    body_hash text NOT NULL,
    job_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
