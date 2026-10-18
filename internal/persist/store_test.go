package persist

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/karum-labs/karum-server/internal/orchestrator"
	"github.com/karum-labs/karum-server/internal/presets"
)

func TestNeonURIParses(t *testing.T) {
	cfg, err := poolConfig("postgresql://user:secret@ep-example-pooler.c-2.aws.neon.tech/karumdb?sslmode=require&channel_binding=require")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.ConnectTimeout < 10*time.Second {
		t.Fatalf("connect timeout %s", cfg.ConnConfig.ConnectTimeout)
	}
	if cfg.ConnConfig.Host != "ep-example-pooler.c-2.aws.neon.tech" {
		t.Fatalf("host %s", cfg.ConnConfig.Host)
	}
}

func TestMigrationsExist(t *testing.T) {
	raw, err := migrationFS.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, table := range []string{"campaigns", "runs", "evidence_snapshots", "drafts", "trace_events", "idempotency_keys"} {
		if !contains(text, "CREATE TABLE "+table) && !contains(text, "CREATE TABLE IF NOT EXISTS "+table) {
			t.Fatalf("migration missing %s", table)
		}
	}
}

func TestPostgresReRunKeepsThePreviousSnapshot(t *testing.T) {
	dsn := os.Getenv("KARUM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("KARUM_TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	store, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	first := &orchestrator.Run{
		ID: "run-pg-1", CampaignID: presets.CampaignID("austin-wholesale"),
		PresetID: "austin-wholesale", State: orchestrator.StateReady,
		Baseline: orchestrator.BaselineResult{Text: "first"}, CreatedAt: time.Now().Add(-time.Minute),
	}
	second := &orchestrator.Run{
		ID: "run-pg-2", CampaignID: presets.CampaignID("austin-wholesale"),
		PresetID: "austin-wholesale", State: orchestrator.StateReady,
		Baseline: orchestrator.BaselineResult{Text: "second"}, CreatedAt: time.Now(),
	}
	if err := store.Put(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, second); err != nil {
		t.Fatal(err)
	}
	var baseline string
	if err := store.pool.QueryRow(ctx, `SELECT baseline_text FROM runs WHERE id = $1`, first.ID).Scan(&baseline); err != nil {
		t.Fatal(err)
	}
	if baseline != "first" {
		t.Fatalf("previous snapshot baseline = %q", baseline)
	}
	var newest string
	if err := store.pool.QueryRow(ctx, `
		SELECT id FROM runs
		WHERE preset_id = $1 AND state = 'ready'
		ORDER BY created_at DESC LIMIT 1
	`, "austin-wholesale").Scan(&newest); err != nil {
		t.Fatal(err)
	}
	if newest != second.ID {
		t.Fatalf("newest = %s, want %s", newest, second.ID)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || len(needle) == 0 ||
		(func() bool { return stringIndex(haystack, needle) >= 0 })())
}

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
