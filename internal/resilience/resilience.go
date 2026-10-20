// Package resilience holds the gateway's deadlines, circuit breakers, rate
// limits, and idempotency store (PLAN.md §26).
package resilience

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Breaker opens after consecutive failures and fails fast until the cooldown
// passes. One breaker is kept per downstream dependency.
type Breaker struct {
	mu        sync.Mutex
	failures  int
	openUntil time.Time
	threshold int
	cooldown  time.Duration
	now       func() time.Time
}

// NewBreaker returns a breaker that opens after threshold consecutive
// failures and stays open for cooldown.
func NewBreaker(threshold int, cooldown time.Duration) *Breaker {
	if threshold < 1 {
		threshold = 5
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	return &Breaker{threshold: threshold, cooldown: cooldown, now: time.Now}
}

// Allow reports whether a call may proceed.
func (b *Breaker) Allow() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.openUntil.IsZero() {
		return true
	}
	if b.now().After(b.openUntil) {
		b.openUntil = time.Time{}
		b.failures = 0
		return true
	}
	return false
}

// Record counts a failure or clears the streak on success.
func (b *Breaker) Record(err error) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err == nil {
		b.failures = 0
		b.openUntil = time.Time{}
		return
	}
	b.failures++
	if b.failures >= b.threshold {
		b.openUntil = b.now().Add(b.cooldown)
	}
}

// Open reports whether the breaker is currently rejecting calls.
func (b *Breaker) Open() bool { return !b.Allow() }

// Do runs fn with a deadline. The parent context is honored, so a client
// abort cancels the downstream call. A zero deadline uses fallback.
func Do(ctx context.Context, deadline, fallback time.Duration, fn func(context.Context) error) error {
	if deadline <= 0 {
		deadline = fallback
	}
	if deadline <= 0 {
		deadline = 8 * time.Second
	}
	child, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	return fn(child)
}

// Limiter enforces a per-IP limit and a global limit.
type Limiter struct {
	global *rate.Limiter
	perIP  rate.Limit
	burst  int
	mu     sync.Mutex
	ips    map[string]*rate.Limiter
}

// NewLimiter builds the two limits. perIP and global are events per second.
func NewLimiter(perIP float64, perIPBurst int, global float64, globalBurst int) *Limiter {
	if perIP <= 0 {
		perIP = 5
	}
	if perIPBurst < 1 {
		perIPBurst = 10
	}
	if global <= 0 {
		global = 50
	}
	if globalBurst < 1 {
		globalBurst = 100
	}
	return &Limiter{
		global: rate.NewLimiter(rate.Limit(global), globalBurst),
		perIP:  rate.Limit(perIP),
		burst:  perIPBurst,
		ips:    map[string]*rate.Limiter{},
	}
}

// Allow reports whether this IP may make another request. The per-IP bucket
// is checked first so a rejected IP does not consume the global budget.
func (l *Limiter) Allow(ip string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	lim := l.ips[ip]
	if lim == nil {
		lim = rate.NewLimiter(l.perIP, l.burst)
		l.ips[ip] = lim
	}
	l.mu.Unlock()
	if !lim.Allow() {
		return false
	}
	return l.global.Allow()
}

// Idempotent is one stored create.
type Idempotent struct {
	Hash  string
	JobID string
}

// IdempotencyStore remembers the first body hash and job id for a create key,
// so a double submit returns the original job (PLAN.md §26).
type IdempotencyStore interface {
	// Lookup returns the record stored for the key.
	Lookup(ctx context.Context, key string) (Idempotent, bool, error)
	// Remember stores the key. It reports false when another request stored a
	// record for the key first.
	Remember(ctx context.Context, key string, item Idempotent) (bool, error)
}

// Idempotency is the in-process store: the fallback when Postgres is
// unreachable, and the store a single-replica deployment uses.
type Idempotency struct {
	mu   sync.Mutex
	keys map[string]idempotentEntry
	// ttl bounds how long a key is remembered; a create key older than this
	// is treated as a new create.
	ttl time.Duration
	// limit bounds the map, so an unbounded key space cannot grow forever.
	limit int
	now   func() time.Time
}

// idempotentEntry is one remembered create with the time it was stored.
type idempotentEntry struct {
	item   Idempotent
	stored time.Time
}

// NewIdempotency returns an empty in-process store.
func NewIdempotency() *Idempotency {
	return &Idempotency{
		keys:  map[string]idempotentEntry{},
		ttl:   24 * time.Hour,
		limit: 10000,
		now:   time.Now,
	}
}

// Lookup returns a previous create for the key.
func (s *Idempotency) Lookup(ctx context.Context, key string) (Idempotent, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.keys[key]
	if !ok {
		return Idempotent{}, false, nil
	}
	if s.expired(entry) {
		delete(s.keys, key)
		return Idempotent{}, false, nil
	}
	return entry.item, true, nil
}

// Remember stores the key. A concurrent first-writer wins; the bool is false
// when another request stored a different record first.
func (s *Idempotency) Remember(ctx context.Context, key string, item Idempotent) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.keys[key]; ok && !s.expired(entry) {
		return false, nil
	}
	s.keys[key] = idempotentEntry{item: item, stored: s.now()}
	s.evictLocked()
	return true, nil
}

// expired reports whether the entry is older than the store's ttl.
func (s *Idempotency) expired(entry idempotentEntry) bool {
	if s.ttl <= 0 {
		return false
	}
	return s.now().Sub(entry.stored) > s.ttl
}

// evictLocked drops the oldest entries when the map is over its limit.
func (s *Idempotency) evictLocked() {
	if s.limit <= 0 || len(s.keys) <= s.limit {
		return
	}
	oldestKey := ""
	var oldest time.Time
	for key, entry := range s.keys {
		if oldestKey == "" || entry.stored.Before(oldest) {
			oldestKey, oldest = key, entry.stored
		}
	}
	if oldestKey != "" {
		delete(s.keys, oldestKey)
	}
}

// FallbackStore tries the primary store and falls back to the in-process one
// when the primary is unreachable, so a create never fails because the
// database is down (PLAN.md §25).
type FallbackStore struct {
	Primary IdempotencyStore
	Memory  *Idempotency
}

// Lookup reads the primary store and falls back to memory on error.
func (f *FallbackStore) Lookup(ctx context.Context, key string) (Idempotent, bool, error) {
	if f.Primary != nil {
		item, ok, err := f.Primary.Lookup(ctx, key)
		if err == nil {
			return item, ok, nil
		}
	}
	if f.Memory == nil {
		return Idempotent{}, false, nil
	}
	return f.Memory.Lookup(ctx, key)
}

// Remember writes to the primary store and falls back to memory on error.
func (f *FallbackStore) Remember(ctx context.Context, key string, item Idempotent) (bool, error) {
	if f.Primary != nil {
		won, err := f.Primary.Remember(ctx, key, item)
		if err == nil {
			return won, nil
		}
	}
	if f.Memory == nil {
		return true, nil
	}
	return f.Memory.Remember(ctx, key, item)
}
