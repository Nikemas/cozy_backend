package staff

import (
	"context"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

func TestLoginRateLimiterAllowsUpToLimit(t *testing.T) {
	var l loginRateLimiter

	for i := 0; i < loginAttemptLimit; i++ {
		if !l.allow("+996700000001") {
			t.Fatalf("attempt %d: expected allow, got blocked", i+1)
		}
	}
	if l.allow("+996700000001") {
		t.Fatalf("expected attempt %d to be blocked", loginAttemptLimit+1)
	}
}

func TestLoginRateLimiterTracksKeysIndependently(t *testing.T) {
	var l loginRateLimiter

	for i := 0; i < loginAttemptLimit; i++ {
		if !l.allow("+996700000001") {
			t.Fatalf("phone 1, attempt %d: expected allow", i+1)
		}
	}
	if l.allow("+996700000001") {
		t.Fatal("expected phone 1 to be blocked after exhausting its budget")
	}
	if !l.allow("+996700000002") {
		t.Fatal("expected a different phone number to have its own budget")
	}
}

func TestLoginRateLimiterResetClearsBudget(t *testing.T) {
	var l loginRateLimiter

	for i := 0; i < loginAttemptLimit; i++ {
		l.allow("+996700000001")
	}
	if l.allow("+996700000001") {
		t.Fatal("expected budget to be exhausted before reset")
	}

	l.reset("+996700000001")

	if !l.allow("+996700000001") {
		t.Fatal("expected allow right after reset")
	}
}

func TestLoginBlocksAfterRepeatedFailures(t *testing.T) {
	active := &Staff{ID: "s1", Phone: "+996700000001", PasswordHash: mustHash(t, "correct-horse"), Role: RoleManager, IsActive: true}
	svc, _ := newTestService(t, active)

	for i := 0; i < loginAttemptLimit; i++ {
		if _, err := svc.Login(context.Background(), "+996700000001", "wrong-password"); err == nil {
			t.Fatalf("attempt %d: expected wrong-password error", i+1)
		}
	}

	_, err := svc.Login(context.Background(), "+996700000001", "correct-horse")
	if err == nil {
		t.Fatal("expected rate limit error even with the correct password, got nil")
	}
	appErr, ok := err.(*apperr.AppError)
	if !ok {
		t.Fatalf("expected *apperr.AppError, got %T", err)
	}
	if appErr.Code != "too_many_attempts" {
		t.Fatalf("expected code %q, got %q", "too_many_attempts", appErr.Code)
	}
}
