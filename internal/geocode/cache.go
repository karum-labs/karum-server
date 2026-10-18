package geocode

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const cacheTTL = 24 * time.Hour

// Cache stores geocode results. A Redis outage falls back to memory and
// raises the degraded bit the health endpoint reads.
type Cache interface {
	Get(ctx context.Context, query string) (Result, bool)
	Set(ctx context.Context, query string, result Result)
	Degraded() bool
}

// MemoryCache is the in-process geocode cache.
type MemoryCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	now   func() time.Time
	items map[string]memoryItem
}

type memoryItem struct {
	result  Result
	expires time.Time
}

// NewMemoryCache returns an empty cache with the geocode TTL.
func NewMemoryCache() *MemoryCache {
	return &MemoryCache{
		ttl:   cacheTTL,
		now:   time.Now,
		items: map[string]memoryItem{},
	}
}

func (c *MemoryCache) Get(_ context.Context, query string) (Result, bool) {
	if c == nil {
		return Result{}, false
	}
	key := cacheKey(query)
	c.mu.Lock()
	defer c.mu.Unlock()
	item, ok := c.items[key]
	if !ok || c.now().After(item.expires) {
		delete(c.items, key)
		return Result{}, false
	}
	result := item.result
	result.CacheHit = true
	return result, true
}

func (c *MemoryCache) Set(_ context.Context, query string, result Result) {
	if c == nil {
		return
	}
	result.CacheHit = false
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[cacheKey(query)] = memoryItem{result: result, expires: c.now().Add(c.ttl)}
}

func (c *MemoryCache) Degraded() bool { return false }

// TieredCache tries Redis and falls back to memory. A Redis error flips the
// degraded bit and does not fail the lookup.
type TieredCache struct {
	memory   *MemoryCache
	redis    *redis.Client
	degraded bool
	mu       sync.Mutex
}

// NewTieredCache wraps memory with an optional Redis address. An empty address
// keeps the cache in memory only, which is not a degraded state: Redis is
// optional behind the in-process fallback.
func NewTieredCache(addr string) *TieredCache {
	tier := &TieredCache{memory: NewMemoryCache()}
	if addr == "" {
		return tier
	}
	tier.redis = redis.NewClient(&redis.Options{
		Addr:         addr,
		DialTimeout:  200 * time.Millisecond,
		ReadTimeout:  200 * time.Millisecond,
		WriteTimeout: 200 * time.Millisecond,
	})
	return tier
}

func (c *TieredCache) Get(ctx context.Context, query string) (Result, bool) {
	if c == nil {
		return Result{}, false
	}
	if c.redis != nil {
		raw, err := c.redis.Get(ctx, "geocode:"+cacheKey(query)).Result()
		if err == nil {
			result, ok := decodeCached(raw)
			if ok {
				result.CacheHit = true
				return result, true
			}
		} else if err != redis.Nil {
			c.markDegraded()
		}
	}
	return c.memory.Get(ctx, query)
}

func (c *TieredCache) Set(ctx context.Context, query string, result Result) {
	if c == nil {
		return
	}
	c.memory.Set(ctx, query, result)
	if c.redis == nil {
		return
	}
	if err := c.redis.Set(ctx, "geocode:"+cacheKey(query), encodeCached(result), cacheTTL).Err(); err != nil {
		c.markDegraded()
	}
}

func (c *TieredCache) Degraded() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.degraded
}

func (c *TieredCache) markDegraded() {
	c.mu.Lock()
	c.degraded = true
	c.mu.Unlock()
}

func cacheKey(query string) string {
	return strings.ToLower(strings.TrimSpace(query))
}

func encodeCached(result Result) string {
	result.CacheHit = false
	// lat,lng,address,attribution, then candidates as address|lat|lng separated by \n
	b := strings.Builder{}
	b.WriteString(formatFloat(result.Resolved.Lat))
	b.WriteByte('\t')
	b.WriteString(formatFloat(result.Resolved.Lng))
	b.WriteByte('\t')
	b.WriteString(escape(result.FormattedAddress))
	b.WriteByte('\t')
	b.WriteString(escape(result.Attribution))
	for _, cand := range result.Candidates {
		b.WriteByte('\n')
		b.WriteString(escape(cand.FormattedAddress))
		b.WriteByte('\t')
		b.WriteString(formatFloat(cand.Point.Lat))
		b.WriteByte('\t')
		b.WriteString(formatFloat(cand.Point.Lng))
	}
	return b.String()
}

func decodeCached(raw string) (Result, bool) {
	lines := strings.Split(raw, "\n")
	if len(lines) == 0 {
		return Result{}, false
	}
	head := strings.Split(lines[0], "\t")
	if len(head) != 4 {
		return Result{}, false
	}
	lat, ok1 := parseFloat(head[0])
	lng, ok2 := parseFloat(head[1])
	if !ok1 || !ok2 {
		return Result{}, false
	}
	result := Result{
		Resolved:         Point{Lat: lat, Lng: lng},
		FormattedAddress: unescape(head[2]),
		Attribution:      unescape(head[3]),
	}
	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) != 3 {
			return Result{}, false
		}
		clat, ok1 := parseFloat(cols[1])
		clng, ok2 := parseFloat(cols[2])
		if !ok1 || !ok2 {
			return Result{}, false
		}
		result.Candidates = append(result.Candidates, Candidate{
			FormattedAddress: unescape(cols[0]),
			Point:            Point{Lat: clat, Lng: clng},
		})
	}
	return result, true
}

func escape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "\t", `\t`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

func unescape(s string) string {
	var b strings.Builder
	escaped := false
	for _, r := range s {
		if escaped {
			switch r {
			case 't':
				b.WriteByte('\t')
			case 'n':
				b.WriteByte('\n')
			default:
				b.WriteRune(r)
			}
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func formatFloat(v float64) string {
	return strings.TrimSpace(strconvFormat(v))
}

func parseFloat(s string) (float64, bool) {
	return parseFloatImpl(s)
}
