package httpmw

import (
	"testing"
	"time"
)

func TestKeyedLimiterLimitsEachKeySeparately(t *testing.T) {
	// Arrange: 2 requests at once, then one per second.
	k := NewKeyedLimiter(RateLimitConfig{RPS: 1, Burst: 2})
	clock := &fakeClock{t: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	k.l.now = clock.now

	// Act + Assert
	for i := range 2 {
		if ok, _ := k.Allow("staff-1"); !ok {
			t.Fatalf("request %d within burst rejected", i+1)
		}
	}
	ok, retry := k.Allow("staff-1")
	if ok || retry != time.Second {
		t.Fatalf("Allow beyond burst = (%v, %v), want (false, 1s)", ok, retry)
	}
	if ok, _ := k.Allow("staff-2"); !ok {
		t.Error("another key must have its own bucket")
	}
	clock.advance(time.Second)
	if ok, _ := k.Allow("staff-1"); !ok {
		t.Error("token should have refilled after 1s")
	}
}

func TestKeyedLimiterDisabledAllowsAll(t *testing.T) {
	k := NewKeyedLimiter(RateLimitConfig{RPS: 0})
	for range 100 {
		if ok, _ := k.Allow("x"); !ok {
			t.Fatal("a disabled limiter must allow everything")
		}
	}
}

func TestKeyedLimiterRefusesEmptyKey(t *testing.T) {
	// Arrange: an empty key would make every caller share one bucket.
	k := NewKeyedLimiter(RateLimitConfig{RPS: 1, Burst: 5})

	// Act
	ok, _ := k.Allow("")

	// Assert
	if ok {
		t.Fatal("an enabled limiter must refuse an empty key")
	}
}
