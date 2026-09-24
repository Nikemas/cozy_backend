package auth

import (
	"testing"
	"time"
)

func TestWindowLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := newWindowLimiter(2, time.Minute)
	l.now = func() time.Time { return now }

	for i := 0; i < 2; i++ {
		if !l.allow("a") {
			t.Fatalf("event %d should be allowed", i+1)
		}
	}
	if l.allow("a") {
		t.Fatal("want exactly 2 allowed per window")
	}
	if !l.allow("b") {
		t.Fatal("keys are independent")
	}
	if !l.blocked("a") || l.blocked("b") {
		t.Fatal("blocked state wrong")
	}
	now = now.Add(time.Minute)
	if l.blocked("a") || !l.allow("a") {
		t.Fatal("window should reset")
	}

	l.hit("c")
	l.hit("c")
	if !l.blocked("c") {
		t.Fatal("hit should count toward the limit")
	}
}

func TestWindowLimiterDisabled(t *testing.T) {
	l := newWindowLimiter(0, time.Minute)
	for i := 0; i < 100; i++ {
		l.hit("a")
		if !l.allow("a") || l.blocked("a") {
			t.Fatal("limit 0 must disable the limiter")
		}
	}
	var nilL *windowLimiter
	if !nilL.allow("a") || nilL.blocked("a") {
		t.Fatal("nil limiter must allow")
	}
	if !newWindowLimiter(1, time.Minute).allow("") {
		t.Fatal("empty key (no client IP) is never limited")
	}
}
