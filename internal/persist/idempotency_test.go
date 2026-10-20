package persist

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/karum-labs/karum-server/internal/resilience"
)

// fakePool answers the two statements the idempotency store sends.
type fakePool struct {
	rows     map[string]resilience.Idempotent
	execErr  error
	rowErr   error
	execSeen []string
}

func (f *fakePool) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	if f.rowErr != nil {
		return fakeRow{err: f.rowErr}
	}
	key, _ := args[0].(string)
	item, ok := f.rows[key]
	if !ok {
		return fakeRow{err: pgx.ErrNoRows}
	}
	return fakeRow{hash: item.Hash, jobID: item.JobID}
}

func (f *fakePool) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.execSeen = append(f.execSeen, sql)
	if f.execErr != nil {
		return pgconn.CommandTag{}, f.execErr
	}
	if len(args) == 3 {
		key, _ := args[0].(string)
		hash, _ := args[1].(string)
		jobID, _ := args[2].(string)
		if _, exists := f.rows[key]; exists {
			return pgconn.CommandTag{}, nil
		}
		f.rows[key] = resilience.Idempotent{Hash: hash, JobID: jobID}
		return pgconn.NewCommandTag("INSERT 0 1"), nil
	}
	return pgconn.NewCommandTag("DELETE 1"), nil
}

// fakeRow is one result row.
type fakeRow struct {
	hash    string
	jobID   string
	err     error
	scanned bool
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i := range dest {
		switch i {
		case 0:
			*(dest[0].(*string)) = r.hash
		case 1:
			*(dest[1].(*string)) = r.jobID
		}
	}
	return nil
}

func TestIdempotencyRemembersTheFirstBody(t *testing.T) {
	ctx := context.Background()
	pool := &fakePool{rows: map[string]resilience.Idempotent{}}
	store := &Idempotency{pool: pool, ttl: time.Hour}

	won, err := store.Remember(ctx, "k", resilience.Idempotent{Hash: "h1", JobID: "run-1"})
	if err != nil || !won {
		t.Fatalf("first remember must win: won=%v err=%v", won, err)
	}
	got, ok, err := store.Lookup(ctx, "k")
	if err != nil || !ok {
		t.Fatalf("lookup = %+v ok=%v err=%v", got, ok, err)
	}
	if got.Hash != "h1" || got.JobID != "run-1" {
		t.Fatalf("stored = %+v", got)
	}
}

func TestIdempotencyLookupOfAnUnknownKey(t *testing.T) {
	ctx := context.Background()
	store := &Idempotency{pool: &fakePool{rows: map[string]resilience.Idempotent{}}, ttl: time.Hour}

	_, ok, err := store.Lookup(ctx, "missing")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if ok {
		t.Fatal("an unknown key must not be reported as stored")
	}
}

func TestIdempotencySurfacesAQueryFailure(t *testing.T) {
	ctx := context.Background()
	store := &Idempotency{pool: &fakePool{rowErr: errors.New("database is down")}, ttl: time.Hour}

	if _, _, err := store.Lookup(ctx, "k"); err == nil {
		t.Fatal("a query failure must be surfaced, not swallowed")
	}
}

func TestIdempotencySweepDeletesTheOldRows(t *testing.T) {
	pool := &fakePool{rows: map[string]resilience.Idempotent{}}
	store := &Idempotency{pool: pool, ttl: time.Hour}

	if err := store.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(pool.execSeen) != 1 {
		t.Fatalf("exec statements = %v", pool.execSeen)
	}
}
