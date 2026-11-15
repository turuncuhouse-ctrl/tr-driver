package ratelimit

import (
	"fmt"
	"testing"
	"time"
)

func TestLimiterBurstThenRefill(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := New(60, 3) // 1 token/second, burst 3
	l.SetClock(func() time.Time { return now })

	for i := 0; i < 3; i++ {
		if !l.Allow("u1") {
			t.Fatalf("request %d inside the burst was rejected", i)
		}
	}
	if l.Allow("u1") {
		t.Fatal("request beyond the burst must be rejected")
	}
	if !l.Allow("u2") {
		t.Fatal("keys must be limited independently")
	}
	now = now.Add(1500 * time.Millisecond)
	if !l.Allow("u1") {
		t.Fatal("a refilled token should allow one request")
	}
	if l.Allow("u1") {
		t.Fatal("only one token should have refilled")
	}
	now = now.Add(time.Hour)
	for i := 0; i < 3; i++ {
		if !l.Allow("u1") {
			t.Fatalf("bucket should refill to the burst, request %d rejected", i)
		}
	}
	if l.Allow("u1") {
		t.Fatal("refill must be capped at the burst")
	}
}

func TestLimiterBoundsMemoryAndFailsClosed(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := New(60, 1)
	l.SetClock(func() time.Time { return now })
	for i := 0; i < maxBuckets; i++ {
		l.Allow(fmt.Sprintf("k%d", i))
	}
	if l.Allow("one-too-many") {
		t.Fatal("a full table of active keys must reject new keys")
	}
	now = now.Add(time.Minute)
	if !l.Allow("after-prune") {
		t.Fatal("idle buckets should be pruned to make room")
	}
	if len(l.buckets) > 2 {
		t.Fatalf("expected idle buckets to be pruned, have %d", len(l.buckets))
	}
}
