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
	s := NewIdempotency()
	if !s.Remember("k", Idempotent{Hash: "h1", JobID: "run-1"}) {
		t.Fatal("first remember must win")
	}
	if s.Remember("k", Idempotent{Hash: "h2", JobID: "run-2"}) {
		t.Fatal("second remember must lose")
	}
	got, ok := s.Lookup("k")
	if !ok || got.JobID != "run-1" || got.Hash != "h1" {
		t.Fatalf("stored = %+v ok=%v", got, ok)
	}
}
