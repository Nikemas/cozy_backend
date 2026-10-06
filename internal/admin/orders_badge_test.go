package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/staff"
)

// runBadgeEndpoint calls GET /admin/orders/badge behind withNavBadges as st.
func runBadgeEndpoint(t *testing.T, counter *fakeNewOrders, st *staff.Staff) *httptest.ResponseRecorder {
	t.Helper()
	h := &handlers{render: newTestRenderer(t)}
	req := httptest.NewRequest(http.MethodGet, "/admin/orders/badge", nil)
	req.Header.Set("HX-Request", "true")
	req = req.WithContext(staff.NewContextWithStaff(req.Context(), st))
	rec := httptest.NewRecorder()
	withNavBadges(counter)(h.ordersBadge)(withLang(rec, req), req)
	return rec
}

func TestOrdersBadgeEndpointReturnsFragment(t *testing.T) {
	owner := &staff.Staff{ID: "s1", Name: "Owner", Role: staff.RoleOwner, IsActive: true}
	w := runBadgeEndpoint(t, &fakeNewOrders{n: 4}, owner)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		`id="admin-orders-badge"`, `data-count="4"`, `>4</span>`,
		`hx-get="/admin/orders/badge"`, `every 60s`, `document.visibilityState`,
		`id="admin-hamburger-badge"`, `hx-swap-oob="outerHTML"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("fragment missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<html") || strings.Contains(body, "admin-sidebar") {
		t.Error("fragment must not carry the page layout")
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q", cc)
	}
}

func TestOrdersBadgeEndpointZeroIsHidden(t *testing.T) {
	owner := &staff.Staff{ID: "s1", Name: "Owner", Role: staff.RoleOwner, IsActive: true}
	body := runBadgeEndpoint(t, &fakeNewOrders{n: 0}, owner).Body.String()
	if !regexp.MustCompile(`id="admin-orders-badge"[^>]*\bhidden\b`).MatchString(body) {
		t.Errorf("zero badge must stay in the DOM (it polls) but hidden:\n%s", body)
	}
}

func TestOrdersBadgeEndpointScopedToPoint(t *testing.T) {
	pt := "pt-1"
	counter := &fakeNewOrders{n: 2}
	runBadgeEndpoint(t, counter, &staff.Staff{ID: "s2", Role: staff.RolePointStaff, PointID: &pt, IsActive: true})
	if counter.pointID == nil || *counter.pointID != pt {
		t.Fatalf("point_staff badge not scoped: %v", counter.pointID)
	}
}

func TestSidebarBadgePolls(t *testing.T) {
	owner := &staff.Staff{ID: "s1", Name: "Owner", Role: staff.RoleOwner, IsActive: true}
	body := runWithBadges(t, &fakeNewOrders{n: 3}, owner, http.MethodGet).Body.String()
	if !strings.Contains(body, `hx-get="/admin/orders/badge"`) {
		t.Error("sidebar badge does not refresh itself")
	}
}

// Unpaid online-card orders aren't actionable yet: the badge leaves them out.
func TestOrderBadgeRepoSkipsUnpaidOnlineOrders(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectQuery(regexp.QuoteMeta(`status = 'placed' AND (payment_method <> 'online_card' OR payment_status = 'paid')`)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))

	n, err := orderBadgeRepo{db: db}.CountNewOrders(context.Background(), nil)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
