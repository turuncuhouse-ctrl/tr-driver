package personalbackup

import (
	"sync"
	"time"
)

// RateLimiter is a per-key token bucket with a lazy refill. It protects the
// write endpoints from runaway clients (a phone stuck in a retry loop) without
// adding a database round-trip per request.
//
// The limiter is intentionally in-process: the deployment target is a single
// server instance, and the endpoints only ever touch the caller's own rows.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64 // tokens per second
	burst   float64
	now     func() time.Time
}

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

// NewRateLimiter allows burst requests at once and then perMinute requests per
// minute per key.
func NewRateLimiter(perMinute, burst int) *RateLimiter {
	if perMinute < 1 {
		perMinute = 1
	}
	if burst < 1 {
		burst = 1
	}
	return &RateLimiter{
		buckets: make(map[string]*bucket),
		rate:    float64(perMinute) / 60.0,
		burst:   float64(burst),
		now:     time.Now,
	}
}

// Allow consumes one token for key and reports whether the request may run.
func (l *RateLimiter) Allow(key string) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= 4096 {
			l.pruneLocked(now)
		}
		b = &bucket{tokens: l.burst, lastSeen: now}
		l.buckets[key] = b
	}
	elapsed := now.Sub(b.lastSeen).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
	}
	b.lastSeen = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// pruneLocked drops idle buckets so a large user base cannot grow the map
// without bound. Full buckets idle for ten minutes carry no state worth keeping.
func (l *RateLimiter) pruneLocked(now time.Time) {
	for key, b := range l.buckets {
		if now.Sub(b.lastSeen) > 10*time.Minute {
			delete(l.buckets, key)
		}
	}
}
