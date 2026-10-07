package importguard

import (
	"context"
	"testing"
	"time"
)

const testSlotWait = 20 * time.Millisecond

// fakeLimiter allows n requests, recording the keys it was asked about.
type fakeLimiter struct {
	n    int
	keys []string
}

func (f *fakeLimiter) Allow(key string) (bool, time.Duration) {
	f.keys = append(f.keys, key)
	if f.n <= 0 {
		return false, 7 * time.Second
	}
	f.n--
	return true, 0
}

func TestGateLimitsConcurrentHolders(t *testing.T) {
	// Arrange
	g := NewGate(2, testSlotWait)
	ctx := context.Background()

	// Act
	rel1, ok1 := g.Acquire(ctx)
	rel2, ok2 := g.Acquire(ctx)
	_, ok3 := g.Acquire(ctx)

	// Assert
	if !ok1 || !ok2 {
		t.Fatal("the first two holders must get a slot")
	}
	if ok3 {
		t.Fatal("a third concurrent holder must be refused")
	}
	rel1()
	rel1() // releasing twice must not free a second slot
	rel4, ok4 := g.Acquire(ctx)
	if !ok4 {
		t.Fatal("a released slot must be reusable")
	}
	if _, ok := g.Acquire(ctx); ok {
		t.Fatal("a double release must not free an extra slot")
	}
	rel2()
	rel4()
	if n := g.Held(); n != 0 {
		t.Errorf("slots held after all releases = %d, want 0", n)
	}
	if g.Cap() != 2 {
		t.Errorf("Cap = %d, want 2", g.Cap())
	}
}

func TestGateGivesUpWhenRequestEnds(t *testing.T) {
	// Arrange: the only slot is taken and the wait is long.
	g := NewGate(1, time.Hour)
	rel, _ := g.Acquire(context.Background())
	defer rel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Act
	start := time.Now()
	_, ok := g.Acquire(ctx)

	// Assert
	if ok || time.Since(start) > time.Second {
		t.Fatalf("Acquire on a cancelled request = %v after %v, want an immediate refusal", ok, time.Since(start))
	}
}

func TestNilGateIsUnlimited(t *testing.T) {
	var g *Gate
	rel, ok := g.Acquire(context.Background())
	if !ok {
		t.Fatal("a nil gate must not limit")
	}
	rel()
	if g.Held() != 0 || g.Cap() != 0 {
		t.Error("a nil gate holds nothing")
	}
}

func TestBeginAllowsAndHoldsSlot(t *testing.T) {
	// Arrange
	lim := &fakeLimiter{n: 1}
	g := &Guard{Limiter: lim, Gate: NewGate(1, testSlotWait)}

	// Act
	d := g.Begin(context.Background(), "staff-1")

	// Assert
	if d.Outcome != Allowed {
		t.Fatalf("Outcome = %v, want Allowed", d.Outcome)
	}
	if g.Gate.Held() != 1 {
		t.Fatalf("held = %d, want 1 while the import runs", g.Gate.Held())
	}
	d.Release()
	if g.Gate.Held() != 0 {
		t.Fatalf("held after Release = %d, want 0", g.Gate.Held())
	}
	if len(lim.keys) != 1 || lim.keys[0] != "staff-1" {
		t.Errorf("limiter keys = %v, want [staff-1]", lim.keys)
	}
}

func TestBeginRateLimitedBeforeTakingSlot(t *testing.T) {
	// Arrange
	g := &Guard{Limiter: &fakeLimiter{n: 0}, Gate: NewGate(1, testSlotWait)}

	// Act
	d := g.Begin(context.Background(), "staff-1")

	// Assert
	if d.Outcome != RateLimited {
		t.Fatalf("Outcome = %v, want RateLimited", d.Outcome)
	}
	if d.RetryAfter != 7*time.Second {
		t.Errorf("RetryAfter = %v, want the limiter's 7s", d.RetryAfter)
	}
	if g.Gate.Held() != 0 {
		t.Errorf("a rate-limited request held a slot")
	}
	d.Release() // must be safe to call on a refusal
}

func TestBeginBusyWhenNoSlotFrees(t *testing.T) {
	// Arrange
	g := &Guard{Limiter: &fakeLimiter{n: 10}, Gate: NewGate(1, testSlotWait)}
	holder := g.Begin(context.Background(), "a")
	defer holder.Release()

	// Act
	d := g.Begin(context.Background(), "b")

	// Assert
	if d.Outcome != Busy {
		t.Fatalf("Outcome = %v, want Busy", d.Outcome)
	}
	if d.RetryAfter != BusyRetryAfter {
		t.Errorf("RetryAfter = %v, want %v", d.RetryAfter, BusyRetryAfter)
	}
}

func TestBeginOnNilGuardOrPartsAllows(t *testing.T) {
	for name, g := range map[string]*Guard{"nil guard": nil, "empty guard": {}} {
		t.Run(name, func(t *testing.T) {
			d := g.Begin(context.Background(), "staff-1")
			if d.Outcome != Allowed {
				t.Fatalf("Outcome = %v, want Allowed", d.Outcome)
			}
			d.Release()
		})
	}
}

func TestNewSharesOneLimiterAndGate(t *testing.T) {
	// Arrange
	g := New()

	// Act: burn the per-staff burst.
	for i := range RateBurst {
		d := g.Begin(context.Background(), "staff-1")
		if d.Outcome != Allowed {
			t.Fatalf("request %d: Outcome = %v, want Allowed", i, d.Outcome)
		}
		d.Release()
	}
	over := g.Begin(context.Background(), "staff-1")
	other := g.Begin(context.Background(), "staff-2")
	defer other.Release()

	// Assert
	if over.Outcome != RateLimited || over.RetryAfter <= 0 {
		t.Errorf("over the burst: %+v, want RateLimited with a retry hint", over)
	}
	if other.Outcome != Allowed {
		t.Errorf("another staff member must have their own bucket: %v", other.Outcome)
	}
	if g.Gate.Cap() != MaxConcurrent {
		t.Errorf("gate cap = %d, want %d", g.Gate.Cap(), MaxConcurrent)
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	cases := map[time.Duration]string{0: "1", 300 * time.Millisecond: "1", 5 * time.Second: "5", 5100 * time.Millisecond: "6"}
	for d, want := range cases {
		if got := RetryAfterSeconds(d); got != want {
			t.Errorf("RetryAfterSeconds(%v) = %q, want %q", d, got, want)
		}
	}
}
