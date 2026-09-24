package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/staff"
)

// fakeResolver is an in-memory staffResolver for tests, following the same
// fake-instead-of-live-database pattern as internal/staff's own
// fakeStaffGetter/fakeSessionStore (see fakes_test.go there).
type fakeResolver struct {
	st  *staff.Staff
	err error
}

func (f *fakeResolver) StaffFromRequest(*http.Request) (*staff.Staff, error) {
	return f.st, f.err
}

func TestRequireStaffRoleRedirectsWithoutSession(t *testing.T) {
	resolver := &fakeResolver{}
	called := false
	handler := requireStaffRole(resolver, staff.RoleOwner)(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/admin/staff", nil))

	if called {
		t.Fatal("handler should not run without a session")
	}
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/admin/login" {
		t.Fatalf("expected redirect to /admin/login, got %q", loc)
	}
}

func TestRequireStaffRoleRedirectsOnResolverError(t *testing.T) {
	resolver := &fakeResolver{err: errors.New("db unreachable")}
	called := false
	handler := requireStaffRole(resolver, staff.RoleOwner)(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/admin/staff", nil))

	if called {
		t.Fatal("handler should not run when session lookup errors")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected %d, got %d", http.StatusInternalServerError, rec.Code)
	}
}

func TestRequireStaffRoleRedirectsInsufficientRoleToFirstAllowedPage(t *testing.T) {
	resolver := &fakeResolver{st: &staff.Staff{ID: "s1", Role: staff.RoleManager, IsActive: true}}
	called := false
	handler := requireStaffRole(resolver, staff.RoleOwner)(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/admin/staff", nil))

	if called {
		t.Fatal("handler should not run for an insufficient role")
	}
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/admin/orders" {
		t.Fatalf("expected redirect to manager's first allowed page /admin/orders, got %q", loc)
	}
}

// point_staff hitting an owner/manager-only page (e.g. /admin/reports) is
// sent to its first allowed page — its own point's orders — not a dead end.
func TestRequireStaffRoleRedirectsPointStaffToOrders(t *testing.T) {
	resolver := &fakeResolver{st: &staff.Staff{ID: "s1", Role: staff.RolePointStaff, IsActive: true}}
	handler := requireStaffRole(resolver, staff.RoleOwner, staff.RoleManager)(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not run for point_staff on an owner/manager-only page")
	})

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/admin/reports", nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/admin/orders" {
		t.Fatalf("expected redirect to /admin/orders, got %q", loc)
	}
}

func TestFirstAllowedPathUnknownRoleIsNoAccess(t *testing.T) {
	if got := firstAllowedPath(staff.Role("auditor")); got != noAccessPath {
		t.Errorf("firstAllowedPath(unknown) = %q, want %q", got, noAccessPath)
	}
}

func TestRequireStaffRolePassesSufficientRoleAndAttachesStaffToContext(t *testing.T) {
	st := &staff.Staff{ID: "s1", Role: staff.RoleOwner, IsActive: true}
	resolver := &fakeResolver{st: st}
	called := false
	handler := requireStaffRole(resolver, staff.RoleOwner, staff.RoleManager)(func(w http.ResponseWriter, r *http.Request) {
		called = true
		got, ok := staff.FromContext(r.Context())
		if !ok || got.ID != st.ID {
			t.Errorf("expected staff %q in context, got %+v (ok=%v)", st.ID, got, ok)
		}
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/admin/orders", nil))

	if !called {
		t.Fatal("expected the wrapped handler to run for a sufficient role")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rec.Code)
	}
}

func TestFirstAllowedPath(t *testing.T) {
	cases := []struct {
		role staff.Role
		want string
	}{
		{staff.RoleOwner, "/admin/orders"},
		{staff.RoleManager, "/admin/orders"},
		{staff.RolePointStaff, "/admin/orders"},
	}
	for _, c := range cases {
		if got := firstAllowedPath(c.role); got != c.want {
			t.Errorf("firstAllowedPath(%q) = %q, want %q", c.role, got, c.want)
		}
	}
}

func TestNavItemsForRole(t *testing.T) {
	ownerKeys := navKeys(navItemsForRole(staff.RoleOwner, "orders"))
	wantOwner := []string{"orders", "products", "stock", "categories", "reports", "points", "staff", "audit"}
	if !equalStrings(ownerKeys, wantOwner) {
		t.Errorf("owner nav keys = %v, want %v", ownerKeys, wantOwner)
	}

	managerKeys := navKeys(navItemsForRole(staff.RoleManager, "orders"))
	wantManager := []string{"orders", "products", "stock", "categories", "reports"}
	if !equalStrings(managerKeys, wantManager) {
		t.Errorf("manager nav keys = %v, want %v", managerKeys, wantManager)
	}

	pointStaffKeys := navKeys(navItemsForRole(staff.RolePointStaff, "orders"))
	if want := []string{"orders", "stock"}; !equalStrings(pointStaffKeys, want) {
		t.Errorf("point_staff nav keys = %v, want %v", pointStaffKeys, want)
	}

	items := navItemsForRole(staff.RoleOwner, "products")
	for _, it := range items {
		if it.Active != (it.Key == "products") {
			t.Errorf("nav item %q Active = %v, want %v", it.Key, it.Active, it.Key == "products")
		}
	}
}

func navKeys(items []NavItem) []string {
	keys := make([]string, len(items))
	for i, it := range items {
		keys[i] = it.Key
	}
	return keys
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
