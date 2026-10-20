package persist

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karum-labs/karum-server/internal/resilience"
)

// idemPool is the slice of the pgx pool the idempotency store uses. It is an
// interface so the store can be tested without a database.
type idemPool interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Idempotency remembers a create key in Postgres, so a double submit returns
// the original job even after a restart or on another replica (PLAN.md §26).
type Idempotency struct {
	pool idemPool
	// ttl is how long a key is kept. The sweeper deletes older rows.
	ttl time.Duration
}

// NewIdempotency returns a Postgres-backed idempotency store.
func NewIdempotency(pool *pgxpool.Pool) *Idempotency {
	return &Idempotency{pool: pool, ttl: 24 * time.Hour}
}

// Lookup returns the record stored for the key.
func (s *Idempotency) Lookup(ctx context.Context, key string) (resilience.Idempotent, bool, error) {
	if s == nil || s.pool == nil {
		return resilience.Idempotent{}, false, nil
	}
	var item resilience.Idempotent
	err := s.pool.QueryRow(ctx, `
		SELECT body_hash, job_id
		FROM idempotency_keys
		WHERE key = $1
	`, key).Scan(&item.Hash, &item.JobID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return resilience.Idempotent{}, false, nil
		}
		return resilience.Idempotent{}, false, err
	}
	return item, true, nil
}

// Remember stores the key. The insert is the lock: the first writer wins and
// a concurrent second writer reads the row the first one wrote.
func (s *Idempotency) Remember(ctx context.Context, key string, item resilience.Idempotent) (bool, error) {
	if s == nil || s.pool == nil {
		return true, nil
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO idempotency_keys (key, body_hash, job_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (key) DO NOTHING
	`, key, item.Hash, item.JobID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// Sweep deletes the keys older than the retention window. It runs on a timer
// so the table does not grow with every create.
func (s *Idempotency) Sweep(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		DELETE FROM idempotency_keys
		WHERE created_at < now() - make_interval(secs => $1)
	`, s.ttl.Seconds())
	return err
}

// StartSweeper deletes expired keys every interval until the context ends.
func (s *Idempotency) StartSweeper(ctx context.Context, interval time.Duration) {
	if s == nil || s.pool == nil || interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				err := s.Sweep(sweepCtx)
				cancel()
				if err != nil {
					continue
				}
			}
		}
	}()
}
