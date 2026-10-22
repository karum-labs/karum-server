// Package resilience holds the gateway's deadlines, circuit breakers, rate
// limits, and idempotency store (PLAN.md §26).
package resilience

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"sync"
	"syscall"
	"time"

	"golang.org/x/time/rate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

// Record counts a failure or clears the streak on success. A cancellation is
// not an upstream failure, so it is ignored: the streak stays as it is and
// five aborted calls never open the circuit.
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
	if IsCancellation(err) {
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

// retryAttempts is how many times a call is tried: the first attempt plus
// one extra. A second attempt is enough to ride out a blip; more would
// stretch the call past the deadline the caller asked for.
const retryAttempts = 2

// retryBackoff is the base pause between the two attempts. The pause is
// jittered so parallel calls do not retry in lockstep.
const retryBackoff = 50 * time.Millisecond

// DoWithRetry runs fn with a deadline and tries it once more when the
// failure is a transient transport failure: a gRPC Unavailable or
// DeadlineExceeded, or a dropped connection.
//
// A business failure never reaches this helper as an error: it arrives as a
// code on the response. A failure the caller caused, a cancelled or expired
// context, is returned as it is and is never retried.
//
// Both attempts share the call deadline, so the retry can never push the
// call past the deadline the caller asked for.
func DoWithRetry(ctx context.Context, deadline, fallback time.Duration, fn func(context.Context) error) error {
	if deadline <= 0 {
		deadline = fallback
	}
	if deadline <= 0 {
		deadline = 8 * time.Second
	}
	child, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	var err error
	for attempt := 1; attempt <= retryAttempts; attempt++ {
		err = fn(child)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			// The caller cancelled the call or its own deadline passed: the
			// upstream is not at fault and a retry would not help.
			return err
		}
		if child.Err() != nil {
			// The call deadline is spent; a retry would overrun it.
			return err
		}
		if !IsTransient(err) {
			return err
		}
		if attempt == retryAttempts {
			return err
		}
		if !sleep(ctx, child, retryPause(attempt)) {
			return err
		}
	}
	return err
}

// sleep waits out the pause before the next attempt. It reports false when
// the call was cancelled while waiting.
func sleep(ctx, child context.Context, pause time.Duration) bool {
	timer := time.NewTimer(pause)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	case <-child.Done():
		return false
	}
}

// retryPause is the jittered pause before the next attempt.
func retryPause(attempt int) time.Duration {
	base := retryBackoff * time.Duration(attempt)
	return base + time.Duration(rand.IntN(int(retryBackoff)))
}

// IsTransient reports whether err is a transport failure worth one more
// attempt: a gRPC Unavailable or DeadlineExceeded status, or a dropped
// connection. A business failure is not transient: it arrives as a code on
// the response, not as an error.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	if statusErr, ok := err.(interface{ StatusCode() int }); ok {
		// An HTTP-status error: 429 and 5xx are transient, 4xx is permanent.
		code := statusErr.StatusCode()
		if code == 429 || code >= 500 {
			return true
		}
		return false
	}
	var grpcStatus interface{ GRPCStatus() *status.Status }
	if errors.As(err, &grpcStatus) {
		switch grpcStatus.GRPCStatus().Code() {
		case codes.Unavailable, codes.DeadlineExceeded:
			return true
		}
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	return errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE)
}

// IsCancellation reports whether err is the caller's own cancellation: a
// cancelled context, or one whose deadline passed. A cancelled call says
// nothing about the upstream, so a breaker must not count it as a failure.
func IsCancellation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return IsCanceled(err)
}

// IsCanceled reports whether err is a cancellation and nothing else: a
// cancelled context, or a gRPC call the caller canceled.
func IsCanceled(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	var statusErr interface{ GRPCStatus() *status.Status }
	if errors.As(err, &statusErr) {
		return statusErr.GRPCStatus().Code() == codes.Canceled
	}
	return false
}

// Limiter enforces a per-IP limit and a global limit.
type Limiter struct {
	global *rate.Limiter
	perIP  rate.Limit
	burst  int
	mu     sync.Mutex
	ips    map[string]*rate.Limiter
	// seen records the last request per IP so an unbounded key space cannot
	// grow the map forever. The map is swept when it reaches limit.
	seen  map[string]time.Time
	limit int
	now   func() time.Time
}

// limiterIPLimit bounds the per-IP map. A public host sees many addresses;
// the map keeps the most recently active ones and drops the rest.
const limiterIPLimit = 10000

// limiterIPIdle is how long an IP bucket may sit unused before a sweep is
// allowed to drop it.
const limiterIPIdle = 10 * time.Minute

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
		seen:   map[string]time.Time{},
		limit:  limiterIPLimit,
		now:    time.Now,
	}
}

// Allow reports whether this IP may make another request. The per-IP bucket
// is checked first so a rejected IP does not consume the global budget.
func (l *Limiter) Allow(ip string) bool {
	if l == nil {
		return true
	}
	now := l.now()
	l.mu.Lock()
	lim := l.ips[ip]
	if lim == nil {
		if len(l.ips) >= l.limit {
			l.sweepLocked(now)
		}
		lim = rate.NewLimiter(l.perIP, l.burst)
		l.ips[ip] = lim
	}
	l.seen[ip] = now
	l.mu.Unlock()
	if !lim.Allow() {
		return false
	}
	return l.global.Allow()
}

// sweepLocked drops idle buckets, then the least recently seen ones, until
// the map is below its limit. The caller holds mu.
func (l *Limiter) sweepLocked(now time.Time) {
	cutoff := now.Add(-limiterIPIdle)
	for ip, last := range l.seen {
		if last.Before(cutoff) {
			delete(l.seen, ip)
			delete(l.ips, ip)
		}
	}
	for len(l.ips) >= l.limit {
		oldestIP := ""
		var oldest time.Time
		for ip, last := range l.seen {
			if oldestIP == "" || last.Before(oldest) {
				oldestIP, oldest = ip, last
			}
		}
		if oldestIP == "" {
			// A bucket exists without a seen entry; drop it rather than spin.
			for ip := range l.ips {
				delete(l.ips, ip)
				break
			}
			continue
		}
		delete(l.seen, oldestIP)
		delete(l.ips, oldestIP)
	}
}

// Size reports how many per-IP buckets are held, for the eviction test.
func (l *Limiter) Size() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.ips)
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
