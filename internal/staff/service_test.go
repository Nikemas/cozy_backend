package staff

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

func mustHash(t *testing.T, password string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("bcrypt.GenerateFromPassword: %v", err)
	}
	return string(hash)
}

func newTestService(t *testing.T, staffers ...*Staff) (*Service, *fakeSessionStore) {
	t.Helper()
	sessions := newFakeSessionStore()
	svc := &Service{
		staff:    newFakeStaffGetter(staffers...),
		sessions: sessions,
	}
	return svc, sessions
}

func TestLoginSuccess(t *testing.T) {
	active := &Staff{ID: "s1", Phone: "+996700000001", PasswordHash: mustHash(t, "correct-horse"), Role: RoleManager, IsActive: true}
	svc, sessions := newTestService(t, active)

	token, err := svc.Login(context.Background(), "+996700000001", "correct-horse")
	if err != nil {
		t.Fatalf("Login() unexpected error: %v", err)
	}
	if token == "" {
		t.Fatal("Login() returned empty session token")
	}
	if len(sessions.byHash) != 1 {
		t.Fatalf("expected 1 session to be created, got %d", len(sessions.byHash))
	}
}

func TestLoginWrongPassword(t *testing.T) {
	active := &Staff{ID: "s1", Phone: "+996700000001", PasswordHash: mustHash(t, "correct-horse"), Role: RoleManager, IsActive: true}
	svc, _ := newTestService(t, active)

	_, err := svc.Login(context.Background(), "+996700000001", "wrong-password")
	assertUnauthorized(t, err)
}

func TestLoginUnknownPhone(t *testing.T) {
	svc, _ := newTestService(t)

	_, err := svc.Login(context.Background(), "+996700000099", "whatever")
	assertUnauthorized(t, err)
}

// TestLoginUnknownPhoneRunsBcryptCompare is a regression test for a timing
// side-channel: Login must run a bcrypt compare even when the phone isn't
// found (against a fixed dummy hash), so an unknown phone takes about as
// long as a known phone with a wrong password. Without it, an attacker
// could enumerate valid staff phone numbers by timing login responses,
// since a plain map/DB miss returns far faster than a bcrypt compare does.
//
// Comparing two measured durations against each other is flaky under load,
// so instead this asserts a floor: bcrypt.CompareHashAndPassword at
// bcrypt.DefaultCost takes on the order of tens of milliseconds on typical
// hardware, while a map lookup miss takes nanoseconds. Only an actual
// bcrypt call clears a 5ms floor.
func TestLoginUnknownPhoneRunsBcryptCompare(t *testing.T) {
	svc, _ := newTestService(t)

	start := time.Now()
	_, err := svc.Login(context.Background(), "+996700000099", "whatever")
	elapsed := time.Since(start)

	assertUnauthorized(t, err)

	const minExpectedBcryptTime = 5 * time.Millisecond
	if elapsed < minExpectedBcryptTime {
		t.Fatalf("Login() for an unknown phone returned in %s, expected at least %s — "+
			"looks like bcrypt.CompareHashAndPassword was skipped, reintroducing a timing side-channel",
			elapsed, minExpectedBcryptTime)
	}
}

func TestLoginInactiveStaff(t *testing.T) {
	inactive := &Staff{ID: "s1", Phone: "+996700000001", PasswordHash: mustHash(t, "correct-horse"), Role: RoleManager, IsActive: false}
	svc, _ := newTestService(t, inactive)

	_, err := svc.Login(context.Background(), "+996700000001", "correct-horse")
	assertUnauthorized(t, err)
}

func TestLogoutRevokesSession(t *testing.T) {
	active := &Staff{ID: "s1", Phone: "+996700000001", PasswordHash: mustHash(t, "correct-horse"), Role: RoleManager, IsActive: true}
	svc, _ := newTestService(t, active)

	token, err := svc.Login(context.Background(), "+996700000001", "correct-horse")
	if err != nil {
		t.Fatalf("Login() unexpected error: %v", err)
	}

	if err := svc.Logout(context.Background(), token); err != nil {
		t.Fatalf("Logout() unexpected error: %v", err)
	}

	st, err := svc.authenticatedStaff(context.Background(), token)
	if err != nil {
		t.Fatalf("authenticatedStaff() unexpected error: %v", err)
	}
	if st != nil {
		t.Fatal("authenticatedStaff() returned a staff member for a revoked session")
	}
}

func assertUnauthorized(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected *apperr.AppError, got %T: %v", err, err)
	}
	if appErr.Code != "invalid_credentials" {
		t.Fatalf("expected code invalid_credentials, got %q", appErr.Code)
	}
}
