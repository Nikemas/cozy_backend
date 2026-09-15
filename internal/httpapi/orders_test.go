package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/auth"
	"github.com/Nikemas/cozy_backend/internal/orders"
)

// --- fakes ---

// fakeOrderService is an in-memory orderService for handler tests, so they
// don't need a live database — mirrors fakeStockUpserter in
// admin_catalog_test.go.
type fakeOrderService struct {
	createCalled                     bool
	lastCustomerID                   string
	lastItems                        []orders.OrderItemInput
	lastAddressID, lastPickupPointID *string
	createResult                     *orders.Order
	createErr                        error
	listCalled                       bool
	listResult                       []orders.Order
	listErr                          error
	getCalled                        bool
	lastOrderID                      string
	getResult                        *orders.Order
	getErr                           error
}

func (f *fakeOrderService) CreateOrder(_ context.Context, customerID string, items []orders.OrderItemInput, addressID, pickupPointID *string) (*orders.Order, error) {
	f.createCalled = true
	f.lastCustomerID = customerID
	f.lastItems = items
	f.lastAddressID = addressID
	f.lastPickupPointID = pickupPointID
	return f.createResult, f.createErr
}

func (f *fakeOrderService) ListOrders(_ context.Context, customerID string) ([]orders.Order, error) {
	f.listCalled = true
	f.lastCustomerID = customerID
	return f.listResult, f.listErr
}

func (f *fakeOrderService) GetOrder(_ context.Context, customerID, orderID string) (*orders.Order, error) {
	f.getCalled = true
	f.lastCustomerID = customerID
	f.lastOrderID = orderID
	return f.getResult, f.getErr
}

// fakeCartService is an in-memory cartService for handler tests.
type fakeCartService struct {
	listCalled                    bool
	listResult                    []orders.CartItem
	listErr                       error
	addCalled                     bool
	updateCalled                  bool
	removeCalled                  bool
	lastCustomerID, lastVariantID string
	lastQty                       int
	opErr                         error
}

func (f *fakeCartService) List(_ context.Context, customerID string) ([]orders.CartItem, error) {
	f.listCalled = true
	f.lastCustomerID = customerID
	return f.listResult, f.listErr
}

func (f *fakeCartService) Add(_ context.Context, customerID, variantID string, qty int) error {
	f.addCalled = true
	f.lastCustomerID, f.lastVariantID, f.lastQty = customerID, variantID, qty
	return f.opErr
}

func (f *fakeCartService) UpdateQty(_ context.Context, customerID, variantID string, qty int) error {
	f.updateCalled = true
	f.lastCustomerID, f.lastVariantID, f.lastQty = customerID, variantID, qty
	return f.opErr
}

func (f *fakeCartService) Remove(_ context.Context, customerID, variantID string) error {
	f.removeCalled = true
	f.lastCustomerID, f.lastVariantID = customerID, variantID
	return f.opErr
}

// newCustomerRequest builds a request carrying an authenticated customer ID
// in context the same way auth.Service.RequireCustomer would, via
// auth.NewContextWithCustomerID — so handler tests can exercise a
// CustomerIDFromContext-gated handler directly without a real JWT/login
// flow.
func newCustomerRequest(method, target, customerID, body string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	return r.WithContext(auth.NewContextWithCustomerID(r.Context(), customerID))
}

// --- POST /api/v1/orders ---

func TestCreateOrderHandlerMapsItemsAndFulfillment(t *testing.T) {
	fake := &fakeOrderService{createResult: &orders.Order{ID: "order-1", OrderNumber: "COZY-20260915-001"}}
	handler := apperr.Wrap(createOrderHandler(fake))

	body := `{"items":[{"variant_id":"var-1","quantity":2},{"variant_id":"var-2","quantity":1}],"address_id":"addr-1"}`
	req := newCustomerRequest(http.MethodPost, "/api/v1/orders", "cust-1", body)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if !fake.createCalled {
		t.Fatal("expected CreateOrder to be called")
	}
	if fake.lastCustomerID != "cust-1" {
		t.Errorf("customerID = %q, want cust-1", fake.lastCustomerID)
	}
	want := []orders.OrderItemInput{{VariantID: "var-1", Quantity: 2}, {VariantID: "var-2", Quantity: 1}}
	if len(fake.lastItems) != len(want) || fake.lastItems[0] != want[0] || fake.lastItems[1] != want[1] {
		t.Errorf("items = %+v, want %+v", fake.lastItems, want)
	}
	if fake.lastAddressID == nil || *fake.lastAddressID != "addr-1" {
		t.Errorf("addressID = %v, want addr-1", fake.lastAddressID)
	}
	if fake.lastPickupPointID != nil {
		t.Errorf("pickupPointID = %v, want nil", fake.lastPickupPointID)
	}
}

// TestCreateOrderHandlerPassesPickupThrough checks the pickup_point_id
// field maps through untouched too — the handler doesn't itself enforce
// "exactly one of address/pickup", that's orders.Service.CreateOrder's job
// (see TestCreateOrderHandlerPropagatesServiceValidationError below).
func TestCreateOrderHandlerPassesPickupThrough(t *testing.T) {
	fake := &fakeOrderService{createResult: &orders.Order{ID: "order-1"}}
	handler := apperr.Wrap(createOrderHandler(fake))

	body := `{"items":[{"variant_id":"var-1","quantity":1}],"pickup_point_id":"point-1"}`
	req := newCustomerRequest(http.MethodPost, "/api/v1/orders", "cust-1", body)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if fake.lastAddressID != nil {
		t.Errorf("addressID = %v, want nil", fake.lastAddressID)
	}
	if fake.lastPickupPointID == nil || *fake.lastPickupPointID != "point-1" {
		t.Errorf("pickupPointID = %v, want point-1", fake.lastPickupPointID)
	}
}

// TestCreateOrderHandlerPropagatesServiceValidationError checks that when
// neither/both of address/pickup are supplied, the handler doesn't try to
// pre-validate — it just forwards whatever the body had and lets
// orders.Service.CreateOrder's own apperr.BadRequest("invalid_fulfillment")
// surface as-is.
func TestCreateOrderHandlerPropagatesServiceValidationError(t *testing.T) {
	fake := &fakeOrderService{createErr: apperr.BadRequest("invalid_fulfillment", "укажите ровно один способ получения")}
	handler := apperr.Wrap(createOrderHandler(fake))

	body := `{"items":[{"variant_id":"var-1","quantity":1}]}`
	req := newCustomerRequest(http.MethodPost, "/api/v1/orders", "cust-1", body)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !fake.createCalled {
		t.Fatal("expected CreateOrder to still be called (handler doesn't duplicate the validation)")
	}
}

func TestCreateOrderHandlerRejectsMalformedBody(t *testing.T) {
	fake := &fakeOrderService{}
	handler := apperr.Wrap(createOrderHandler(fake))

	req := newCustomerRequest(http.MethodPost, "/api/v1/orders", "cust-1", `not json`)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if fake.createCalled {
		t.Fatal("expected CreateOrder NOT to be called for a malformed body")
	}
}

func TestCreateOrderHandlerUnauthenticatedWithoutContext(t *testing.T) {
	fake := &fakeOrderService{}
	handler := apperr.Wrap(createOrderHandler(fake))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body.String())
	}
	if fake.createCalled {
		t.Fatal("expected CreateOrder NOT to be called without an authenticated customer")
	}
}

// --- GET /api/v1/orders ---

func TestListOrdersHandlerScopesToCustomer(t *testing.T) {
	fake := &fakeOrderService{listResult: []orders.Order{{ID: "o1"}, {ID: "o2"}}}
	handler := apperr.Wrap(listOrdersHandler(fake))

	req := newCustomerRequest(http.MethodGet, "/api/v1/orders", "cust-1", "")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !fake.listCalled || fake.lastCustomerID != "cust-1" {
		t.Fatalf("expected ListOrders called with cust-1, got called=%v customerID=%q", fake.listCalled, fake.lastCustomerID)
	}
}

// --- GET /api/v1/orders/{id} ---

func TestGetOrderHandlerPassesPathValueThrough(t *testing.T) {
	fake := &fakeOrderService{getResult: &orders.Order{ID: "order-uuid", OrderNumber: "COZY-20260915-001"}}
	handler := apperr.Wrap(getOrderHandler(fake))

	for _, id := range []string{"order-uuid", "COZY-20260915-001"} {
		req := newCustomerRequest(http.MethodGet, "/api/v1/orders/"+id, "cust-1", "")
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("id=%q: status = %d, want 200: %s", id, rec.Code, rec.Body.String())
		}
		if fake.lastOrderID != id {
			t.Errorf("id=%q: GetOrder called with orderID=%q", id, fake.lastOrderID)
		}
		if fake.lastCustomerID != "cust-1" {
			t.Errorf("id=%q: GetOrder called with customerID=%q, want cust-1", id, fake.lastCustomerID)
		}
	}
}

func TestGetOrderHandlerPropagatesNotFound(t *testing.T) {
	fake := &fakeOrderService{getErr: apperr.NotFound("order_not_found", "заказ не найден")}
	handler := apperr.Wrap(getOrderHandler(fake))

	req := newCustomerRequest(http.MethodGet, "/api/v1/orders/missing", "cust-1", "")
	req.SetPathValue("id", "missing")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// --- cart ---

func TestListCartHandlerScopesToCustomer(t *testing.T) {
	fake := &fakeCartService{listResult: []orders.CartItem{{CustomerID: "cust-1", VariantID: "var-1", Qty: 2}}}
	handler := apperr.Wrap(listCartHandler(fake))

	req := newCustomerRequest(http.MethodGet, "/api/v1/cart", "cust-1", "")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !fake.listCalled || fake.lastCustomerID != "cust-1" {
		t.Fatalf("expected List called with cust-1, got called=%v customerID=%q", fake.listCalled, fake.lastCustomerID)
	}
}

func TestAddCartItemHandlerPassesVariantAndQty(t *testing.T) {
	fake := &fakeCartService{}
	handler := apperr.Wrap(addCartItemHandler(fake))

	req := newCustomerRequest(http.MethodPost, "/api/v1/cart/var-1", "cust-1", `{"quantity":3}`)
	req.SetPathValue("variantId", "var-1")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if !fake.addCalled {
		t.Fatal("expected Add to be called")
	}
	if fake.lastCustomerID != "cust-1" || fake.lastVariantID != "var-1" || fake.lastQty != 3 {
		t.Errorf("Add called with customerID=%q variantID=%q qty=%d, want cust-1/var-1/3", fake.lastCustomerID, fake.lastVariantID, fake.lastQty)
	}
}

func TestAddCartItemHandlerRejectsMalformedBody(t *testing.T) {
	fake := &fakeCartService{}
	handler := apperr.Wrap(addCartItemHandler(fake))

	req := newCustomerRequest(http.MethodPost, "/api/v1/cart/var-1", "cust-1", `not json`)
	req.SetPathValue("variantId", "var-1")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if fake.addCalled {
		t.Fatal("expected Add NOT to be called for a malformed body")
	}
}

func TestUpdateCartItemHandlerPassesVariantAndQty(t *testing.T) {
	fake := &fakeCartService{}
	handler := apperr.Wrap(updateCartItemHandler(fake))

	req := newCustomerRequest(http.MethodPut, "/api/v1/cart/var-1", "cust-1", `{"quantity":0}`)
	req.SetPathValue("variantId", "var-1")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if !fake.updateCalled {
		t.Fatal("expected UpdateQty to be called")
	}
	if fake.lastVariantID != "var-1" || fake.lastQty != 0 {
		t.Errorf("UpdateQty called with variantID=%q qty=%d, want var-1/0", fake.lastVariantID, fake.lastQty)
	}
}

func TestRemoveCartItemHandlerScopesToCustomerAndVariant(t *testing.T) {
	fake := &fakeCartService{}
	handler := apperr.Wrap(removeCartItemHandler(fake))

	req := newCustomerRequest(http.MethodDelete, "/api/v1/cart/var-1", "cust-1", "")
	req.SetPathValue("variantId", "var-1")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if !fake.removeCalled || fake.lastCustomerID != "cust-1" || fake.lastVariantID != "var-1" {
		t.Fatalf("expected Remove called with cust-1/var-1, got called=%v customerID=%q variantID=%q", fake.removeCalled, fake.lastCustomerID, fake.lastVariantID)
	}
}

func TestRemoveCartItemHandlerUnauthenticatedWithoutContext(t *testing.T) {
	fake := &fakeCartService{}
	handler := apperr.Wrap(removeCartItemHandler(fake))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/cart/var-1", nil)
	req.SetPathValue("variantId", "var-1")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body.String())
	}
	if fake.removeCalled {
		t.Fatal("expected Remove NOT to be called without an authenticated customer")
	}
}
