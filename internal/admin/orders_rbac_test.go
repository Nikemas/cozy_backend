package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// fakeOrdersSvc records what the handlers asked *orders.Service for.
type fakeOrdersSvc struct {
	listFilter  *orders.AdminListFilter
	list        []orders.Order
	order       *orders.Order
	updateCalls int
}

func (f *fakeOrdersSvc) AdminListOrders(_ context.Context, filter orders.AdminListFilter) ([]orders.Order, int, error) {
	f.listFilter = &filter
	return f.list, len(f.list), nil
}

func (f *fakeOrdersSvc) AdminGetOrder(_ context.Context, _ string) (*orders.Order, error) {
	if f.order == nil {
		return nil, apperr.NotFound("order_not_found", "заказ не найден")
	}
	return f.order, nil
}

func (f *fakeOrdersSvc) AdminUpdateStatus(_ context.Context, _ string, _ orders.OrderStatus) (*orders.Order, error) {
	f.updateCalls++
	return f.order, nil
}

func strPtr(s string) *string { return &s }

func pointStaff(pointID *string) *staff.Staff {
	return &staff.Staff{ID: "ps1", Name: "Нурлан Т.", Role: staff.RolePointStaff, PointID: pointID, IsActive: true}
}

func requestAs(method, target string, st *staff.Staff, body url.Values) *http.Request {
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, target, strings.NewReader(body.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	return r.WithContext(staff.NewContextWithStaff(r.Context(), st))
}

func TestOrdersListPointStaffIsScopedToOwnPoint(t *testing.T) {
	meta := &fakeOrderListMeta{}
	h := &handlers{render: newTestRenderer(t), orderMeta: meta}

	// Even an explicit ?point= for another point is overridden.
	w := httptest.NewRecorder()
	h.ordersListPage(w, requestAs(http.MethodGet, "/admin/orders?point=other", pointStaff(strPtr("mine")), nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if meta.searchFilter == nil || meta.searchFilter.PointID == nil || *meta.searchFilter.PointID != "mine" {
		t.Fatalf("filter = %+v, want PointID forced to the staff member's point", meta.searchFilter)
	}
	if strings.Contains(w.Body.String(), `id="orders-point"`) {
		t.Error("point_staff must not get the point selector")
	}
}

func TestOrdersListPointStaffWithoutPointFailsClosed(t *testing.T) {
	meta := &fakeOrderListMeta{}
	h := &handlers{render: newTestRenderer(t), orderMeta: meta}

	w := httptest.NewRecorder()
	h.ordersListPage(w, requestAs(http.MethodGet, "/admin/orders", pointStaff(nil), nil))

	if meta.searchFilter != nil {
		t.Fatal("orders were queried for a point_staff with no point")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestOrderDetailPointStaffOtherPointIs404(t *testing.T) {
	svc := &fakeOrdersSvc{order: &orders.Order{ID: "o1", OrderNumber: "COZY-1", PointID: strPtr("other")}}
	h := &handlers{render: newTestRenderer(t), ordersSvc: svc}

	w := httptest.NewRecorder()
	r := requestAs(http.MethodGet, "/admin/orders/o1", pointStaff(strPtr("mine")), nil)
	r.SetPathValue("id", "o1")
	h.orderDetailPage(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestOrderStatusUpdatePointStaffOtherPointIsRejected(t *testing.T) {
	svc := &fakeOrdersSvc{order: &orders.Order{ID: "o1", PointID: strPtr("other"), Status: orders.StatusPlaced}}
	h := &handlers{render: newTestRenderer(t), ordersSvc: svc}

	w := httptest.NewRecorder()
	r := requestAs(http.MethodPost, "/admin/orders/o1/status", pointStaff(strPtr("mine")), url.Values{"status": {"confirmed"}})
	r.SetPathValue("id", "o1")
	h.orderStatusUpdate(w, r)

	if svc.updateCalls != 0 {
		t.Fatal("AdminUpdateStatus called for another point's order")
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestStaffCanSeeOrder(t *testing.T) {
	o := &orders.Order{PointID: strPtr("p1")}
	cases := []struct {
		name string
		st   *staff.Staff
		o    *orders.Order
		want bool
	}{
		{"owner", &staff.Staff{Role: staff.RoleOwner}, o, true},
		{"manager", &staff.Staff{Role: staff.RoleManager}, o, true},
		{"point_staff own point", pointStaff(strPtr("p1")), o, true},
		{"point_staff other point", pointStaff(strPtr("p2")), o, false},
		{"point_staff no point", pointStaff(nil), o, false},
		{"order without point", pointStaff(strPtr("p1")), &orders.Order{}, false},
	}
	for _, c := range cases {
		if got := staffCanSeeOrder(c.st, c.o); got != c.want {
			t.Errorf("%s: staffCanSeeOrder = %v, want %v", c.name, got, c.want)
		}
	}
}
