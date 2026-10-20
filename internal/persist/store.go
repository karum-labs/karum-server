// Package persist applies the forward-only Postgres schema and stores
// campaigns, runs, evidence snapshots, drafts, and trace events.
//
// A re-run inserts a new run row. The previous snapshot is not updated. The
// preset path reads the newest ready row. When Postgres is unreachable the
// gateway keeps the in-memory store and reports postgres as down.
package persist

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karum-labs/karum-server/internal/orchestrator"
	"github.com/karum-labs/karum-server/internal/presets"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Store is the Postgres run store plus the in-process cache the SSE hub reads.
type Store struct {
	pool *pgxpool.Pool
	mem  *orchestrator.Store
}

// Open connects, applies migrations, and seeds the four preset campaigns.
func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := poolConfig(dsn)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, err
	}
	store := &Store{pool: pool, mem: orchestrator.NewStore()}
	if err := store.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := store.seedPresets(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := store.hydrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return store, nil
}

// poolConfig parses a Postgres URI. The connect timeout leaves room for a
// sleeping hosted database to wake before the pool gives up.
func poolConfig(dsn string) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	if cfg.ConnConfig.ConnectTimeout < 10*time.Second {
		cfg.ConnConfig.ConnectTimeout = 10 * time.Second
	}
	return cfg, nil
}

func (s *Store) hydrate(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (campaign_id) id, campaign_id, payload
		FROM runs
		ORDER BY campaign_id, created_at DESC
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	runSeq, campaignSeq := 0, 0
	for rows.Next() {
		var id, campaignID string
		var payload []byte
		if err := rows.Scan(&id, &campaignID, &payload); err != nil {
			return err
		}
		var run orchestrator.Run
		if err := json.Unmarshal(payload, &run); err != nil {
			return err
		}
		s.mem.Put(&run)
		if n, ok := numericSuffix(id, "run-"); ok && n > runSeq {
			runSeq = n
		}
		if n, ok := numericSuffix(campaignID, "cmp-"); ok && n > campaignSeq {
			campaignSeq = n
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.mem.ReserveRunSeq(runSeq)
	s.mem.ReserveCampaignSeq(campaignSeq)
	return nil
}

// numericSuffix parses "run-0012" and "cmp-0003" so hydrated ids advance the
// in-memory counters. Preset campaign ids have a word suffix and are ignored.
func numericSuffix(id, prefix string) (int, bool) {
	if !strings.HasPrefix(id, prefix) {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(id, prefix))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// Bridge is the run store the runner writes. Memory serves the HTTP handlers;
// Postgres keeps the snapshot a restart can hydrate.
type Bridge struct {
	Mem *orchestrator.Store
	SQL *Store
}

// NextID implements orchestrator.RunStore.
func (b Bridge) NextID() string { return b.Mem.NextID() }

// Get implements orchestrator.RunStore.
func (b Bridge) Get(id string) (*orchestrator.Run, bool) { return b.Mem.Get(id) }

// Put implements orchestrator.RunStore. A SQL error is ignored here so a
// database blip does not fail a campaign the memory store already has.
func (b Bridge) Put(run *orchestrator.Run) {
	if b.Mem != nil {
		b.Mem.Put(run)
	}
	if b.SQL == nil || run == nil {
		return
	}
	// A flagship payload carries thousands of heatmap cells and can take
	// longer than a couple of seconds to update on a hosted Postgres. The
	// final state write must not be abandoned after the scouting call.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = b.SQL.Put(ctx, run)
}

// Close releases the pool.
func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

// Memory is the hot cache that satisfies orchestrator.RunStore.
func (s *Store) Memory() *orchestrator.Store {
	if s == nil {
		return nil
	}
	return s.mem
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		var applied bool
		err := s.pool.QueryRow(ctx, `SELECT true FROM schema_migrations WHERE version = $1`, entry.Name()).Scan(&applied)
		if err == nil && applied {
			continue
		}
		sql, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("migration %s: %w", entry.Name(), err)
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1) ON CONFLICT DO NOTHING`, entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) seedPresets(ctx context.Context) error {
	for _, preset := range presets.New().All() {
		seed, err := json.Marshal(preset.Seed)
		if err != nil {
			return err
		}
		_, err = s.pool.Exec(ctx, `
			INSERT INTO campaigns (id, preset_id, mode, city, latitude, longitude, radius_meters, brand_name, seed_json)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (id) DO NOTHING
		`, presets.CampaignID(preset.ID), preset.ID, preset.Mode, preset.City, preset.Lat, preset.Lng, preset.RadiusMeters, preset.Brand, seed)
		if err != nil {
			return err
		}
	}
	return nil
}

// Put writes a new snapshot. An existing run id is replaced only in the hot
// cache during the pipeline; the SQL row is inserted once and then updated in
// place for that same id so trace events accumulate. A re-run uses a new id,
// so the previous row stays intact.
func (s *Store) Put(ctx context.Context, run *orchestrator.Run) error {
	if s == nil || run == nil {
		return nil
	}
	s.mem.Put(run)
	payload, err := json.Marshal(run)
	if err != nil {
		return err
	}
	trace, err := json.Marshal(run.Trace)
	if err != nil {
		return err
	}
	evidence, err := json.Marshal(run.Evidence)
	if err != nil {
		return err
	}
	seedJSON, err := json.Marshal(run.Seed)
	if err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO campaigns (id, preset_id, mode, city, latitude, longitude, radius_meters, brand_name, seed_json, account_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10)
		ON CONFLICT (id) DO UPDATE SET
			account_id = COALESCE(EXCLUDED.account_id, campaigns.account_id)
	`, emptyCampaign(run.CampaignID), run.PresetID, string(run.Mode), run.City, run.Point.Lat, run.Point.Lng, run.RadiusMeters, run.Brand.Name, seedJSON, nullableAccount(run.AccountID)); err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO runs (
			id, campaign_id, preset_id, state, free_entry, baseline_text,
			qloo_payload, grounding_payload, trace_json, payload, created_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			COALESCE($7::jsonb, '{}'::jsonb), COALESCE($8::jsonb, '{}'::jsonb), $9::jsonb, $10::jsonb, $11
		)
		ON CONFLICT (id) DO UPDATE SET
			state = EXCLUDED.state,
			baseline_text = EXCLUDED.baseline_text,
			qloo_payload = EXCLUDED.qloo_payload,
			grounding_payload = EXCLUDED.grounding_payload,
			trace_json = EXCLUDED.trace_json,
			payload = EXCLUDED.payload
	`, run.ID, emptyCampaign(run.CampaignID), run.PresetID, string(run.State), run.FreeEntry, run.Baseline.Text,
		nullableJSON(run.QlooPayload), nullableJSON(run.GroundingPayload), trace, payload, run.CreatedAt)
	if err != nil {
		return err
	}
	if run.State == orchestrator.StateReady || run.State == orchestrator.StateDegraded {
		_, err = s.pool.Exec(ctx, `
			INSERT INTO evidence_snapshots (id, run_id, evidence_json)
			VALUES ($1, $2, $3::jsonb)
			ON CONFLICT (id) DO NOTHING
		`, "evsnap-"+run.ID, run.ID, evidence)
		if err != nil {
			return err
		}
	}
	for _, event := range run.Trace {
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO trace_events (run_id, sequence, cursor, stage, elapsed_ms, cache_hit, detail)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (run_id, sequence) DO NOTHING
		`, run.ID, event.Sequence, event.Cursor, string(event.Stage), event.ElapsedMS, event.CacheHit, event.Detail); err != nil {
			return err
		}
	}
	return nil
}

// SaveDraft inserts a draft row and leaves earlier drafts in place.
func (s *Store) SaveDraft(ctx context.Context, runID, doorID, kind, body string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO drafts (id, run_id, door_id, kind, body)
		VALUES ($1, $2, $3, $4, $5)
	`, fmt.Sprintf("draft-%s-%s-%d", runID, kind, time.Now().UnixNano()), runID, doorID, kind, body)
	return err
}

func emptyCampaign(id string) string {
	if id == "" {
		return "cmp-unscoped"
	}
	return id
}

func nullableJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

func nullableAccount(id string) any {
	if id == "" {
		return nil
	}
	return id
}
