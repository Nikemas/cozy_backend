package staff

import (
	"context"
	"errors"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/httpmw"
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

func TestLoginRateLimitedPerIPAcrossPhones(t *testing.T) {
	svc, _ := newTestService(t)
	svc.SetLoginIPLimit(3)
	ctx := httpmw.WithClientIP(context.Background(), "198.51.100.7")

	for i, phone := range []string{"+996700000001", "+996700000002", "+996700000003"} {
		_, err := svc.Login(ctx, phone, "wrong-password")
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != "invalid_credentials" {
			t.Fatalf("attempt %d: err = %v, want invalid_credentials", i+1, err)
		}
	}
	_, err := svc.Login(ctx, "+996700000004", "wrong-password")
	var ae *apperr.AppError
	if !errors.As(err, &ae) || ae.Code != "too_many_attempts" {
		t.Fatalf("4th attempt from same IP: err = %v, want too_many_attempts", err)
	}

	// Another IP still has its budget.
	_, err = svc.Login(httpmw.WithClientIP(context.Background(), "203.0.113.1"), "+996700000004", "wrong-password")
	if !errors.As(err, &ae) || ae.Code != "invalid_credentials" {
		t.Fatalf("other IP: err = %v", err)
	}

	svc.SetLoginIPLimit(0)
	if svc.ipLimiter != nil {
		t.Fatal("limit 0 must disable the per-IP limiter")
	}
}
