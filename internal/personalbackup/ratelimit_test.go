package personalbackup

import (
	"testing"
	"time"
)

func TestRateLimiterAllowsBurstThenBlocks(t *testing.T) {
	limiter := NewRateLimiter(60, 3)
	clock := time.Unix(1_700_000_000, 0)
	limiter.now = func() time.Time { return clock }

	for i := 0; i < 3; i++ {
		if !limiter.Allow("user-1") {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	if limiter.Allow("user-1") {
		t.Fatal("burst exhausted, next request must be blocked")
	}
	// Other users have their own bucket.
	if !limiter.Allow("user-2") {
		t.Fatal("second user must not be affected")
	}
	// 60/min == one token per second.
	clock = clock.Add(1 * time.Second)
	if !limiter.Allow("user-1") {
		t.Fatal("refilled token should be allowed")
	}
	if limiter.Allow("user-1") {
		t.Fatal("only one token was refilled")
	}
}

func TestRateLimiterDoesNotExceedBurst(t *testing.T) {
	limiter := NewRateLimiter(60, 2)
	clock := time.Unix(1_700_000_000, 0)
	limiter.now = func() time.Time { return clock }
	_ = limiter.Allow("u")
	_ = limiter.Allow("u")
	clock = clock.Add(10 * time.Minute)
	for i := 0; i < 2; i++ {
		if !limiter.Allow("u") {
			t.Fatalf("refill %d should be allowed", i+1)
		}
	}
	if limiter.Allow("u") {
		t.Fatal("bucket must never exceed its burst size")
	}
}

func TestRateLimiterPrunesIdleBuckets(t *testing.T) {
	limiter := NewRateLimiter(60, 1)
	clock := time.Unix(1_700_000_000, 0)
	limiter.now = func() time.Time { return clock }
	for i := 0; i < 200; i++ {
		limiter.Allow(string(rune('a' + i%26)))
	}
	clock = clock.Add(11 * time.Minute)
	limiter.mu.Lock()
	limiter.pruneLocked(clock)
	size := len(limiter.buckets)
	limiter.mu.Unlock()
	if size != 0 {
		t.Fatalf("expected idle buckets to be pruned, %d left", size)
	}
}
