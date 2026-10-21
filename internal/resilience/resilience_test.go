package resilience

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBreakerOpensAndCloses(t *testing.T) {
	b := NewBreaker(2, time.Hour)
	if !b.Allow() {
		t.Fatal("fresh breaker must allow")
	}
	b.Record(errors.New("down"))
	if !b.Allow() {
		t.Fatal("one failure must not open")
	}
	b.Record(errors.New("down"))
	if b.Allow() {
		t.Fatal("threshold must open the breaker")
	}
	b.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if !b.Allow() {
		t.Fatal("cooldown must close the breaker")
	}
	b.Record(nil)
	if !b.Allow() {
		t.Fatal("success must keep the breaker closed")
	}
}

func TestDoHonorsParentCancelAndDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Do(ctx, time.Second, time.Second, func(ctx context.Context) error {
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want canceled", err)
	}

	err = Do(context.Background(), 15*time.Millisecond, time.Second, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline", err)
	}
}

func TestLimiterPerIPAndGlobal(t *testing.T) {
	l := NewLimiter(100, 1, 100, 2)
	if !l.Allow("a") {
		t.Fatal("first request must pass")
	}
	if l.Allow("a") {
		t.Fatal("per-IP burst of 1 must reject the second")
	}
	if !l.Allow("b") {
		t.Fatal("a different IP still has its own burst")
	}
	if l.Allow("c") {
		t.Fatal("global burst of 2 must reject the third distinct IP")
	}
}

func TestIdempotencyRemembersTheFirstBody(t *testing.T) {
	ctx := context.Background()
	s := NewIdempotency()
	won, err := s.Remember(ctx, "k", Idempotent{Hash: "h1", JobID: "run-1"})
	if err != nil || !won {
		t.Fatalf("first remember must win: won=%v err=%v", won, err)
	}
	won, err = s.Remember(ctx, "k", Idempotent{Hash: "h2", JobID: "run-2"})
	if err != nil || won {
		t.Fatalf("second remember must lose: won=%v err=%v", won, err)
	}
	got, ok, err := s.Lookup(ctx, "k")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !ok || got.JobID != "run-1" || got.Hash != "h1" {
		t.Fatalf("stored = %+v ok=%v", got, ok)
	}
}

func TestIdempotencyExpiresAndBoundsTheMap(t *testing.T) {
	ctx := context.Background()
	s := NewIdempotency()
	s.ttl = time.Minute
	s.limit = 2
	now := time.Now()
	s.now = func() time.Time { return now }

	if _, err := s.Remember(ctx, "k", Idempotent{Hash: "h1", JobID: "run-1"}); err != nil {
		t.Fatalf("remember: %v", err)
	}
	now = now.Add(2 * time.Minute)
	if _, ok, err := s.Lookup(ctx, "k"); err != nil || ok {
		t.Fatalf("an expired key must not be served: ok=%v err=%v", ok, err)
	}

	now = time.Now()
	for _, key := range []string{"a", "b", "c"} {
		if _, err := s.Remember(ctx, key, Idempotent{Hash: "h", JobID: key}); err != nil {
			t.Fatalf("remember %s: %v", key, err)
		}
	}
	if len(s.keys) > s.limit {
		t.Fatalf("the map grew past its limit: %d keys", len(s.keys))
	}
}

func TestFallbackStoreUsesMemoryWhenThePrimaryFails(t *testing.T) {
	ctx := context.Background()
	failing := &failingStore{}
	store := &FallbackStore{Primary: failing, Memory: NewIdempotency()}

	if _, err := store.Remember(ctx, "k", Idempotent{Hash: "h1", JobID: "run-1"}); err != nil {
		t.Fatalf("remember: %v", err)
	}
	got, ok, err := store.Lookup(ctx, "k")
	if err != nil || !ok || got.JobID != "run-1" {
		t.Fatalf("the memory fallback must serve the key: got=%+v ok=%v err=%v", got, ok, err)
	}
}

func TestLimiterEvictsTheOldestIPBuckets(t *testing.T) {
	l := NewLimiter(100, 100, 1000, 1000)
	l.limit = 2
	base := time.Now()
	l.now = func() time.Time { return base }

	if !l.Allow("first") {
		t.Fatal("the first IP must be allowed")
	}
	base = base.Add(time.Second)
	if !l.Allow("second") {
		t.Fatal("the second IP must be allowed")
	}
	base = base.Add(time.Second)
	if !l.Allow("third") {
		t.Fatal("the third IP must be allowed")
	}

	if got := l.Size(); got != 2 {
		t.Fatalf("limiter buckets = %d, want the limit (2)", got)
	}
	if _, ok := l.ips["first"]; ok {
		t.Fatal("the least recently seen bucket must be evicted")
	}
	for _, ip := range []string{"second", "third"} {
		if _, ok := l.ips[ip]; !ok {
			t.Fatalf("the recent bucket %q must be kept", ip)
		}
	}
}

// failingStore is a store whose database is unreachable.
type failingStore struct{}

func (f *failingStore) Lookup(context.Context, string) (Idempotent, bool, error) {
	return Idempotent{}, false, errors.New("database is down")
}

func (f *failingStore) Remember(context.Context, string, Idempotent) (bool, error) {
	return false, errors.New("database is down")
}
