package staff

import (
	"context"
	"errors"
	"testing"

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
