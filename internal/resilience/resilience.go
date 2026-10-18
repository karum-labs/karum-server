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

// Idempotency remembers the first body hash and job id for a key.
type Idempotency struct {
	mu   sync.Mutex
	keys map[string]Idempotent
}

// Idempotent is one stored create.
type Idempotent struct {
	Hash  string
	JobID string
}

// NewIdempotency returns an empty store.
func NewIdempotency() *Idempotency {
	return &Idempotency{keys: map[string]Idempotent{}}
}

// Lookup returns a previous create for the key.
func (s *Idempotency) Lookup(key string) (Idempotent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.keys[key]
	return item, ok
}

// Remember stores the key. A concurrent first-writer wins; the bool is false
// when another request stored a different record first.
func (s *Idempotency) Remember(key string, item Idempotent) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.keys[key]; ok {
		return false
	}
	s.keys[key] = item
	return true
}
