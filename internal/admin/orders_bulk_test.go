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

// bulkOrdersSvc is a per-order fake of adminOrdersService.
type bulkOrdersSvc struct {
	orders  map[string]*orders.Order
	failOn  map[string]error
	updated []string
}

func (f *bulkOrdersSvc) AdminListOrders(context.Context, orders.AdminListFilter) ([]orders.Order, int, error) {
	return nil, 0, nil
}

func (f *bulkOrdersSvc) AdminGetOrder(_ context.Context, id string) (*orders.Order, error) {
	if o, ok := f.orders[id]; ok {
		return o, nil
	}
	return nil, apperr.NotFound("order_not_found", "заказ не найден")
}

func (f *bulkOrdersSvc) AdminUpdateStatus(_ context.Context, id string, s orders.OrderStatus) (*orders.Order, error) {
	if err := f.failOn[id]; err != nil {
		return nil, err
	}
	f.updated = append(f.updated, id)
	o := *f.orders[id]
	o.Status = s
	return &o, nil
}

func newBulkOrdersSvc() *bulkOrdersSvc {
	pA, pB := "pA", "pB"
	return &bulkOrdersSvc{
		orders: map[string]*orders.Order{
			uuid1: {ID: uuid1, OrderNumber: "COZY-1", Status: orders.StatusPlaced, PointID: &pA},
			uuid2: {ID: uuid2, OrderNumber: "COZY-2", Status: orders.StatusDelivered, PointID: &pB},
		},
		failOn: map[string]error{uuid2: apperr.Conflict("invalid_status_transition", "недопустимый переход статуса")},
	}
}

func postOrdersBulk(h *handlers, st *staff.Staff, form url.Values) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.orderBulkStatus(w, requestAs(http.MethodPost, "/admin/orders/bulk-status", st, form))
	return w
}

// Every order goes through AdminUpdateStatus; a rejected one is reported
// by number, the others still change.
func TestOrderBulkStatusReportsPerOrderFailures(t *testing.T) {
	svc := newBulkOrdersSvc()
	h := &handlers{ordersSvc: svc}
	owner := &staff.Staff{ID: "s1", Role: staff.RoleOwner}

	w := postOrdersBulk(h, owner, url.Values{"status": {"confirmed"}, "id": {uuid1, uuid2}, "back": {"/admin/orders?status=placed"}})
	if len(svc.updated) != 1 || svc.updated[0] != uuid1 {
		t.Fatalf("updated = %v, want only %s", svc.updated, uuid1)
	}
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil || loc.Path != "/admin/orders" {
		t.Fatalf("Location = %q", w.Header().Get("Location"))
	}
	q := loc.Query()
	if q.Get("status") != "placed" || !strings.Contains(q.Get("toast"), "1 из 2") {
		t.Errorf("query = %v", q)
	}
	if fails := q["bulk_fail"]; len(fails) != 1 || !strings.Contains(fails[0], "COZY-2") || !strings.Contains(fails[0], "недопустимый") {
		t.Errorf("bulk_fail = %v", fails)
	}
}

func TestOrderBulkStatusManagerCannotCancel(t *testing.T) {
	svc := newBulkOrdersSvc()
	h := &handlers{ordersSvc: svc}
	w := postOrdersBulk(h, &staff.Staff{ID: "m1", Role: staff.RoleManager}, url.Values{"status": {"cancelled"}, "id": {uuid1}})
	if len(svc.updated) != 0 {
		t.Fatal("manager cancelled orders in bulk")
	}
	if !strings.Contains(w.Header().Get("Location"), url.QueryEscape("только владелец")) {
		t.Errorf("Location = %q", w.Header().Get("Location"))
	}
}

// point_staff can bulk-update only its own point's orders; others read
// as not found.
func TestOrderBulkStatusPointStaffScoped(t *testing.T) {
	svc := newBulkOrdersSvc()
	svc.failOn = nil
	svc.orders[uuid2].Status = orders.StatusPlaced
	h := &handlers{ordersSvc: svc}
	w := postOrdersBulk(h, pointStaff(strPtr("pA")), url.Values{"status": {"confirmed"}, "id": {uuid1, uuid2}})
	if len(svc.updated) != 1 || svc.updated[0] != uuid1 {
		t.Fatalf("updated = %v", svc.updated)
	}
	loc, _ := url.Parse(w.Header().Get("Location"))
	if fails := loc.Query()["bulk_fail"]; len(fails) != 1 || !strings.Contains(fails[0], "не найден") || strings.Contains(fails[0], "COZY-2") {
		t.Errorf("bulk_fail = %v (must not leak the other point's order number)", fails)
	}
}

func TestBulkStatusOptionsByRole(t *testing.T) {
	has := func(opts []BulkStatusOption, v string) bool {
		for _, o := range opts {
			if o.Value == v {
				return true
			}
		}
		return false
	}
	if !has(bulkStatusOptions(ruTr, staff.RoleOwner), "cancelled") || has(bulkStatusOptions(ruTr, staff.RoleManager), "cancelled") {
		t.Error("cancel must be owner-only")
	}
}

func TestRenderOrdersListWithBulkBar(t *testing.T) {
	rr := newTestRenderer(t)
	h := &handlers{}
	st := &staff.Staff{ID: "s1", Name: "Айгерим Б.", Role: staff.RoleOwner}
	data := h.buildOrdersListView(context.Background(), []orders.Order{{ID: uuid1, OrderNumber: "COZY-1", Status: orders.StatusPlaced}}, 1, "", "", 1)
	data.BulkStatuses = bulkStatusOptions(ruTr, st.Role)
	data.BulkURL = "/admin/orders/bulk-status"
	data.ReturnURL = "/admin/orders?status=placed"
	data.Notes = []string{"№ COZY-9: недопустимый переход"}
	pd := h.shellPageData("orders", "Заказы", st)
	pd.Toast = "Статус «Подтверждён» установлен: 1 из 2"
	pd.Data = data
	w := httptest.NewRecorder()
	if err := rr.Render(w, "orders", pd); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := w.Body.String()
	for _, want := range []string{
		`id="orders-bulk"`, `form="orders-bulk" name="id" value="` + uuid1 + `"`, `<option value="cancelled">`,
		`№ COZY-9: недопустимый переход`, `установлен: 1 из 2`, `name="back" value="/admin/orders?status=placed"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q", want)
		}
	}
}
