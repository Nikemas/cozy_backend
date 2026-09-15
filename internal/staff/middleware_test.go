package staff

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func requestWithSessionCookie(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/admin/api/whatever", nil)
	if token != "" {
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	}
	return r
}

func farFuture() time.Time {
	return time.Now().Add(24 * time.Hour)
}

func TestRequireRoleAllowsSufficientRole(t *testing.T) {
	staffer := &Staff{ID: "s1", Phone: "+996700000001", Role: RoleOwner, IsActive: true}
	svc, sessions := newTestService(t, staffer)
	_ = sessions.create(context.Background(), staffer.ID, hashSessionToken("good-token"), farFuture())

	called := false
	handler := svc.RequireRole(RoleOwner, RoleManager)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		st, ok := FromContext(r.Context())
		if !ok || st.ID != staffer.ID {
			t.Errorf("expected staff %q in context, got %+v (ok=%v)", staffer.ID, st, ok)
		}
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, requestWithSessionCookie("good-token"))

	if !called {
		t.Fatal("expected the wrapped handler to run for a sufficient role")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestRequireRoleBlocksInsufficientRole(t *testing.T) {
	staffer := &Staff{ID: "s1", Phone: "+996700000001", Role: RolePointStaff, IsActive: true}
	svc, sessions := newTestService(t, staffer)
	_ = sessions.create(context.Background(), staffer.ID, hashSessionToken("good-token"), farFuture())

	called := false
	handler := svc.RequireRole(RoleOwner, RoleManager)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, requestWithSessionCookie("good-token"))

	if called {
		t.Fatal("expected the wrapped handler NOT to run for an insufficient role")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestRequireRoleRejectsMissingCookie(t *testing.T) {
	svc, _ := newTestService(t)

	handler := svc.RequireRole(RoleOwner)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not run without a session cookie")
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, requestWithSessionCookie(""))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestRequireRoleRejectsUnknownSession(t *testing.T) {
	svc, _ := newTestService(t)

	handler := svc.RequireRole(RoleOwner)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not run for an unknown session token")
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, requestWithSessionCookie("no-such-token"))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestRequireRoleRejectsDeactivatedStaff(t *testing.T) {
	staffer := &Staff{ID: "s1", Phone: "+996700000001", Role: RoleOwner, IsActive: false}
	svc, sessions := newTestService(t, staffer)
	_ = sessions.create(context.Background(), staffer.ID, hashSessionToken("good-token"), farFuture())

	handler := svc.RequireRole(RoleOwner)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not run once the staff account is deactivated")
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, requestWithSessionCookie("good-token"))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}
