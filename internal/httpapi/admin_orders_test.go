package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// fakeAdminOrderService is an in-memory adminOrderService for tests,
// recording calls so a test can assert whether the handler ever reached
// it, and what filter/args it was called with — mirrors fakeStockUpserter
// in admin_catalog_test.go.
type fakeAdminOrderService struct {
	listCalled bool
	listFilter orders.AdminListFilter
	listItems  []orders.Order
	listTotal  int
	listErr    error

	getOrder *orders.Order
	getErr   error

	updateCalled bool
	updateID     string
	updateStatus orders.OrderStatus
	updateOrder  *orders.Order
	updateErr    error
}

func (f *fakeAdminOrderService) AdminListOrders(_ context.Context, filter orders.AdminListFilter) ([]orders.Order, int, error) {
	f.listCalled = true
	f.listFilter = filter
	if f.listErr != nil {
		return nil, 0, f.listErr
	}
	return f.listItems, f.listTotal, nil
}

func (f *fakeAdminOrderService) AdminGetOrder(_ context.Context, _ string) (*orders.Order, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.getOrder, nil
}

func (f *fakeAdminOrderService) AdminUpdateStatus(_ context.Context, id string, newStatus orders.OrderStatus) (*orders.Order, error) {
	f.updateCalled = true
	f.updateID = id
	f.updateStatus = newStatus
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return f.updateOrder, nil
}

func strPtr(s string) *string { return &s }

func newAdminOrdersRequest(t *testing.T, method, target string, st *staff.Staff, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	return r.WithContext(staff.NewContextWithStaff(r.Context(), st))
}

// --- GET /admin/api/orders (list) ---

func TestListAdminOrdersHandlerPointStaffForcesOwnPoint(t *testing.T) {
	st := &staff.Staff{ID: "s1", Role: staff.RolePointStaff, PointID: strPtr("point-1")}
	fake := &fakeAdminOrderService{}
	handler := apperr.Wrap(listAdminOrdersHandler(fake))

	// Query asks for point-2's orders; point_staff must be silently
	// restricted to their own point instead.
	req := newAdminOrdersRequest(t, http.MethodGet, "/admin/api/orders?point_id=point-2", st, "")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !fake.listCalled {
		t.Fatal("expected AdminListOrders to be called")
	}
	if fake.listFilter.PointID == nil || *fake.listFilter.PointID != "point-1" {
		t.Fatalf("expected filter.PointID overridden to point-1, got %+v", fake.listFilter.PointID)
	}
}

func TestListAdminOrdersHandlerPointStaffNoPointReturnsEmptyWithoutCallingService(t *testing.T) {
	st := &staff.Staff{ID: "s1", Role: staff.RolePointStaff, PointID: nil}
	fake := &fakeAdminOrderService{}
	handler := apperr.Wrap(listAdminOrdersHandler(fake))

	req := newAdminOrdersRequest(t, http.MethodGet, "/admin/api/orders", st, "")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if fake.listCalled {
		t.Fatal("expected AdminListOrders NOT to be called for a point_staff with no PointID")
	}
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("expected empty items in response, got %s", rec.Body.String())
	}
}

func TestListAdminOrdersHandlerOwnerRespectsPointIDParam(t *testing.T) {
	st := &staff.Staff{ID: "s1", Role: staff.RoleOwner}
	fake := &fakeAdminOrderService{}
	handler := apperr.Wrap(listAdminOrdersHandler(fake))

	req := newAdminOrdersRequest(t, http.MethodGet, "/admin/api/orders?point_id=point-5", st, "")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if fake.listFilter.PointID == nil || *fake.listFilter.PointID != "point-5" {
		t.Fatalf("expected owner's point_id param to pass through unmodified, got %+v", fake.listFilter.PointID)
	}
}

func TestListAdminOrdersHandlerInvalidStatusIsBadRequest(t *testing.T) {
	st := &staff.Staff{ID: "s1", Role: staff.RoleOwner}
	fake := &fakeAdminOrderService{}
	handler := apperr.Wrap(listAdminOrdersHandler(fake))

	req := newAdminOrdersRequest(t, http.MethodGet, "/admin/api/orders?status=not_a_status", st, "")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if fake.listCalled {
		t.Fatal("expected AdminListOrders NOT to be called for an invalid status")
	}
}

// --- GET /admin/api/orders/{id} (detail) ---

func newAdminOrderDetailRequest(t *testing.T, st *staff.Staff, id string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/admin/api/orders/"+id, nil)
	r.SetPathValue("id", id)
	return r.WithContext(staff.NewContextWithStaff(r.Context(), st))
}

func TestGetAdminOrderHandlerPointStaffOwnPointAllowed(t *testing.T) {
	st := &staff.Staff{ID: "s1", Role: staff.RolePointStaff, PointID: strPtr("point-1")}
	fake := &fakeAdminOrderService{getOrder: &orders.Order{ID: "o1", PointID: strPtr("point-1")}}
	handler := apperr.Wrap(getAdminOrderHandler(fake))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newAdminOrderDetailRequest(t, st, "o1"))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGetAdminOrderHandlerPointStaffOtherPointForbidden(t *testing.T) {
	st := &staff.Staff{ID: "s1", Role: staff.RolePointStaff, PointID: strPtr("point-1")}
	fake := &fakeAdminOrderService{getOrder: &orders.Order{ID: "o1", PointID: strPtr("point-2")}}
	handler := apperr.Wrap(getAdminOrderHandler(fake))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newAdminOrderDetailRequest(t, st, "o1"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGetAdminOrderHandlerPointStaffOrderWithNoPointForbidden(t *testing.T) {
	// An order not yet assigned to any point must NOT be visible to
	// point_staff, only to owner/manager.
	st := &staff.Staff{ID: "s1", Role: staff.RolePointStaff, PointID: strPtr("point-1")}
	fake := &fakeAdminOrderService{getOrder: &orders.Order{ID: "o1", PointID: nil}}
	handler := apperr.Wrap(getAdminOrderHandler(fake))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newAdminOrderDetailRequest(t, st, "o1"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGetAdminOrderHandlerOwnerCanSeeAnyPointIncludingUnassigned(t *testing.T) {
	st := &staff.Staff{ID: "s1", Role: staff.RoleOwner}
	fake := &fakeAdminOrderService{getOrder: &orders.Order{ID: "o1", PointID: nil}}
	handler := apperr.Wrap(getAdminOrderHandler(fake))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newAdminOrderDetailRequest(t, st, "o1"))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGetAdminOrderHandlerManagerCanSeeAnyPoint(t *testing.T) {
	st := &staff.Staff{ID: "s1", Role: staff.RoleManager}
	fake := &fakeAdminOrderService{getOrder: &orders.Order{ID: "o1", PointID: strPtr("point-99")}}
	handler := apperr.Wrap(getAdminOrderHandler(fake))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newAdminOrderDetailRequest(t, st, "o1"))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- PUT /admin/api/orders/{id}/status ---

func newAdminOrderStatusRequest(t *testing.T, st *staff.Staff, id, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPut, "/admin/api/orders/"+id+"/status", strings.NewReader(body))
	r.SetPathValue("id", id)
	return r.WithContext(staff.NewContextWithStaff(r.Context(), st))
}

func TestUpdateAdminOrderStatusHandlerPointStaffOwnPointAllowed(t *testing.T) {
	st := &staff.Staff{ID: "s1", Role: staff.RolePointStaff, PointID: strPtr("point-1")}
	fake := &fakeAdminOrderService{
		getOrder:    &orders.Order{ID: "o1", PointID: strPtr("point-1"), Status: orders.StatusPlaced},
		updateOrder: &orders.Order{ID: "o1", PointID: strPtr("point-1"), Status: orders.StatusConfirmed},
	}
	handler := apperr.Wrap(updateAdminOrderStatusHandler(fake))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newAdminOrderStatusRequest(t, st, "o1", `{"status":"confirmed"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !fake.updateCalled {
		t.Fatal("expected AdminUpdateStatus to be called for a point_staff acting on their own point")
	}
	if fake.updateStatus != orders.StatusConfirmed {
		t.Fatalf("expected update status confirmed, got %q", fake.updateStatus)
	}
}

func TestUpdateAdminOrderStatusHandlerPointStaffOtherPointForbidden(t *testing.T) {
	st := &staff.Staff{ID: "s1", Role: staff.RolePointStaff, PointID: strPtr("point-1")}
	fake := &fakeAdminOrderService{
		getOrder: &orders.Order{ID: "o1", PointID: strPtr("point-2"), Status: orders.StatusPlaced},
	}
	handler := apperr.Wrap(updateAdminOrderStatusHandler(fake))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newAdminOrderStatusRequest(t, st, "o1", `{"status":"confirmed"}`))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if fake.updateCalled {
		t.Fatal("expected AdminUpdateStatus NOT to be called for a point_staff acting on another point's order")
	}
}

func TestUpdateAdminOrderStatusHandlerOwnerCanActOnAnyPoint(t *testing.T) {
	st := &staff.Staff{ID: "s1", Role: staff.RoleOwner}
	fake := &fakeAdminOrderService{
		getOrder:    &orders.Order{ID: "o1", PointID: strPtr("point-99"), Status: orders.StatusPlaced},
		updateOrder: &orders.Order{ID: "o1", PointID: strPtr("point-99"), Status: orders.StatusCancelled},
	}
	handler := apperr.Wrap(updateAdminOrderStatusHandler(fake))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newAdminOrderStatusRequest(t, st, "o1", `{"status":"cancelled"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !fake.updateCalled {
		t.Fatal("expected AdminUpdateStatus to be called for an owner acting on any point")
	}
}
