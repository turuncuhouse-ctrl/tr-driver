// Package ratelimit provides a small in-process, per-key token bucket used to
// throttle the authenticated shared-space and torrent endpoints and the public
// share-link endpoint.
//
// It is intentionally in-memory: TR Driver runs as a single instance, and the
// goal is to stop runaway clients and guessing attempts, not to do accounting.
package ratelimit

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// maxBuckets bounds memory use; idle buckets are pruned when it is reached.
const maxBuckets = 8192

// Limiter is a per-key token bucket with lazy refill.
type Limiter struct {
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

// New allows burst requests at once and then perMinute requests per minute for
// each key.
func New(perMinute, burst int) *Limiter {
	if perMinute < 1 {
		perMinute = 1
	}
	if burst < 1 {
		burst = 1
	}
	return &Limiter{
		buckets: make(map[string]*bucket),
		rate:    float64(perMinute) / 60.0,
		burst:   float64(burst),
		now:     time.Now,
	}
}

// SetClock replaces the time source. It exists for tests.
func (l *Limiter) SetClock(now func() time.Time) {
	l.mu.Lock()
	l.now = now
	l.mu.Unlock()
}

// Allow consumes one token for key and reports whether the request may run.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()

	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= maxBuckets {
			l.pruneLocked(now)
		}
		if len(l.buckets) >= maxBuckets {
			// Still full of active keys: fail closed rather than grow without bound.
			return false
		}
		b = &bucket{tokens: l.burst, lastSeen: now}
		l.buckets[key] = b
	}
	if elapsed := now.Sub(b.lastSeen).Seconds(); elapsed > 0 {
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

// RetryAfter is a conservative hint, in seconds, for a rejected caller.
func (l *Limiter) RetryAfter() int {
	seconds := int(1.0/l.rate) + 1
	if seconds < 1 {
		seconds = 1
	}
	if seconds > 60 {
		seconds = 60
	}
	return seconds
}

// Reject writes the standard 429 response headers.
func (l *Limiter) Reject(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(l.RetryAfter()))
}

// pruneLocked drops buckets idle long enough to have refilled completely; they
// carry no state worth keeping.
func (l *Limiter) pruneLocked(now time.Time) {
	idle := time.Duration(l.burst/l.rate*float64(time.Second)) + time.Second
	for key, b := range l.buckets {
		if now.Sub(b.lastSeen) > idle {
			delete(l.buckets, key)
		}
	}
}
