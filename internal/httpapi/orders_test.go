package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/auth"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/orders"
)

// errUnexpected is a plain, non-apperr error used to test that
// listCartHandler propagates unexpected errors instead of treating them as
// a dangling reference to skip.
var errUnexpected = errors.New("unexpected db error")

// --- fakes ---

// fakeOrderService is an in-memory orderService for handler tests, so they
// don't need a live database — mirrors fakeStockUpserter in
// admin_catalog_test.go.
type fakeOrderService struct {
	createCalled                     bool
	lastCustomerID                   string
	lastItems                        []orders.OrderItemInput
	lastAddressID, lastPickupPointID *string
	lastInput                        orders.PlaceOrderInput
	createResult                     *orders.Order
	createReplay                     bool
	createErr                        error
	listCalled                       bool
	listResult                       []orders.Order
	listErr                          error
	getCalled                        bool
	lastOrderID                      string
	getResult                        *orders.Order
	getErr                           error
	cancelResult                     *orders.Order
	cancelErr                        error
}

func (f *fakeOrderService) PlaceOrder(_ context.Context, in orders.PlaceOrderInput) (*orders.Order, bool, error) {
	f.createCalled = true
	f.lastInput = in
	f.lastCustomerID = in.CustomerID
	f.lastItems = in.Items
	f.lastAddressID = in.AddressID
	f.lastPickupPointID = in.PickupPointID
	return f.createResult, !f.createReplay, f.createErr
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

func (f *fakeOrderService) CancelByCustomer(_ context.Context, customerID, orderID string) (*orders.Order, error) {
	f.lastCustomerID = customerID
	f.lastOrderID = orderID
	return f.cancelResult, f.cancelErr
}

// fakeCartService is an in-memory cartService for handler tests.
type fakeCartService struct {
	listCalled                    bool
	listResult                    []orders.CartLine
	listErr                       error
	addCalled                     bool
	updateCalled                  bool
	removeCalled                  bool
	lastCustomerID, lastVariantID string
	lastQty                       int
	opErr                         error
}

func (f *fakeCartService) ListDetailed(_ context.Context, customerID string) ([]orders.CartLine, error) {
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

// fakeCartImageGetter is an in-memory cartImageGetter for listCartHandler
// tests. Records every call's productIDs argument so tests can assert
// PrimaryForProducts is called once with the full, de-duplicated list
// rather than once per cart line.
type fakeCartImageGetter struct {
	images      map[string]catalog.ProductImage
	calls       int
	lastCallIDs []string
}

func (f *fakeCartImageGetter) PrimaryForProducts(_ context.Context, productIDs []string) (map[string]catalog.ProductImage, error) {
	f.calls++
	f.lastCallIDs = productIDs
	out := make(map[string]catalog.ProductImage, len(productIDs))
	for _, id := range productIDs {
		if img, ok := f.images[id]; ok {
			out[id] = img
		}
	}
	return out, nil
}

// fakeOnlineCheckout is an in-memory onlineCheckout.
type fakeOnlineCheckout struct {
	in      orders.PlaceOrderInput
	order   *orders.Order
	url     string
	created bool
}

func (f *fakeOnlineCheckout) PlaceOnlineOrder(_ context.Context, in orders.PlaceOrderInput) (*orders.Order, string, bool, error) {
	f.in = in
	return f.order, f.url, f.created, nil
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
	handler := apperr.Wrap(createOrderHandler(fake, nil))

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
	handler := apperr.Wrap(createOrderHandler(fake, nil))

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
	handler := apperr.Wrap(createOrderHandler(fake, nil))

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
	handler := apperr.Wrap(createOrderHandler(fake, nil))

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
	handler := apperr.Wrap(createOrderHandler(fake, nil))

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

// --- POST /api/v1/orders: comment, Idempotency-Key, replay status ---

func TestCreateOrderHandlerPassesCommentAndIdempotencyKey(t *testing.T) {
	fake := &fakeOrderService{createResult: &orders.Order{ID: "order-1", DeliveryFee: 200, TotalAmount: 5200}}
	handler := apperr.Wrap(createOrderHandler(fake, nil))

	req := newCustomerRequest(http.MethodPost, "/api/v1/orders", "cust-1",
		`{"items":[{"variant_id":"v","quantity":1}],"address_id":"addr-1","comment":"домофон 12"}`)
	req.Header.Set("Idempotency-Key", "attempt-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if fake.lastInput.Comment != "домофон 12" || fake.lastInput.IdempotencyKey != "attempt-1" {
		t.Errorf("input = %+v", fake.lastInput)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["delivery_fee"] != 200.0 || body["total_amount"] != 5200.0 {
		t.Errorf("delivery_fee/total_amount = %v/%v", body["delivery_fee"], body["total_amount"])
	}
}

func TestCreateOrderHandlerReplayIs200(t *testing.T) {
	fake := &fakeOrderService{createResult: &orders.Order{ID: "order-1"}, createReplay: true}
	rec := httptest.NewRecorder()
	req := newCustomerRequest(http.MethodPost, "/api/v1/orders", "cust-1", `{"items":[],"pickup_point_id":"p"}`)
	req.Header.Set("Idempotency-Key", "attempt-1")
	apperr.Wrap(createOrderHandler(fake, nil)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("replay status = %d, want 200", rec.Code)
	}
}

func TestCreateOrderHandlerOnlineReplayKeepsPaymentURL(t *testing.T) {
	checkout := &fakeOnlineCheckout{order: &orders.Order{ID: "order-1"}, url: "https://pay/1", created: false}
	rec := httptest.NewRecorder()
	req := newCustomerRequest(http.MethodPost, "/api/v1/orders", "cust-1",
		`{"items":[],"pickup_point_id":"p","payment_method":"online_card"}`)
	req.Header.Set("Idempotency-Key", "attempt-1")
	apperr.Wrap(createOrderHandler(&fakeOrderService{}, checkout)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"payment_url":"https://pay/1"`) {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if checkout.in.IdempotencyKey != "attempt-1" {
		t.Errorf("key not passed to online checkout: %+v", checkout.in)
	}
}

// --- POST /api/v1/orders/{id}/cancel ---

func TestCancelOrderHandler(t *testing.T) {
	fake := &fakeOrderService{cancelResult: &orders.Order{ID: "order-1", Status: orders.StatusCancelled}}
	req := newCustomerRequest(http.MethodPost, "/api/v1/orders/COZY-1/cancel", "cust-1", "")
	req.SetPathValue("id", "COZY-1")
	rec := httptest.NewRecorder()
	apperr.Wrap(cancelOrderHandler(fake)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"cancelled"`) {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if fake.lastCustomerID != "cust-1" || fake.lastOrderID != "COZY-1" {
		t.Errorf("called with %q/%q", fake.lastCustomerID, fake.lastOrderID)
	}

	fake = &fakeOrderService{cancelErr: orders.ErrNotCancellable}
	rec = httptest.NewRecorder()
	apperr.Wrap(cancelOrderHandler(fake)).ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "order_not_cancellable") {
		t.Fatalf("not cancellable: status = %d body = %s", rec.Code, rec.Body.String())
	}
}

// --- cart ---

type cartHandlerFakes struct {
	cart   *fakeCartService
	images *fakeCartImageGetter
}

func newCartHandlerFakes() *cartHandlerFakes {
	return &cartHandlerFakes{
		cart:   &fakeCartService{},
		images: &fakeCartImageGetter{images: map[string]catalog.ProductImage{}},
	}
}

func (f *cartHandlerFakes) handler() http.Handler {
	cfg := &config.Config{MinIOEndpoint: "minio.local", MinIOBucket: "cozy-media"}
	return apperr.Wrap(listCartHandler(f.cart, f.images, cfg))
}

func cartLine(variantID, productID string, qty int) orders.CartLine {
	return orders.CartLine{VariantID: variantID, Qty: qty, ProductID: productID, ProductName: "Кроссовки",
		ProductNameKy: "Кроссовкалар", Size: "42", Color: "black", Price: 3000, ProductActive: true, InStock: 10}
}

func decodeCart(t *testing.T, rec *httptest.ResponseRecorder) []cartLineResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var lines []cartLineResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &lines); err != nil {
		t.Fatalf("unmarshal response: %v (%s)", err, rec.Body.String())
	}
	return lines
}

func TestListCartHandlerScopesToCustomer(t *testing.T) {
	f := newCartHandlerFakes()
	f.cart.listResult = []orders.CartLine{cartLine("var-1", "prod-1", 2)}

	rec := httptest.NewRecorder()
	f.handler().ServeHTTP(rec, newCustomerRequest(http.MethodGet, "/api/v1/cart", "cust-1", ""))
	decodeCart(t, rec)
	if !f.cart.listCalled || f.cart.lastCustomerID != "cust-1" {
		t.Fatalf("expected ListDetailed called with cust-1, got called=%v customerID=%q", f.cart.listCalled, f.cart.lastCustomerID)
	}
}

func TestListCartHandlerUnauthenticatedWithoutContext(t *testing.T) {
	f := newCartHandlerFakes()
	rec := httptest.NewRecorder()
	f.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/cart", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body.String())
	}
	if f.cart.listCalled {
		t.Fatal("expected ListDetailed NOT to be called without an authenticated customer")
	}
}

// TestListCartHandlerEnrichesLine checks the happy path: one full cart line
// with product/variant data and its primary image.
func TestListCartHandlerEnrichesLine(t *testing.T) {
	f := newCartHandlerFakes()
	f.cart.listResult = []orders.CartLine{cartLine("var-1", "prod-1", 2)}
	f.images.images["prod-1"] = catalog.ProductImage{ID: "img-1", ProductID: "prod-1", ObjectKey: "products/prod-1.jpg"}

	rec := httptest.NewRecorder()
	f.handler().ServeHTTP(rec, newCustomerRequest(http.MethodGet, "/api/v1/cart", "cust-1", ""))
	lines := decodeCart(t, rec)
	if len(lines) != 1 {
		t.Fatalf("len(lines) = %d, want 1: %+v", len(lines), lines)
	}
	got := lines[0]
	if got.VariantID != "var-1" || got.Quantity != 2 || got.ProductID != "prod-1" || got.ProductName != "Кроссовки" ||
		got.ProductNameKy != "Кроссовкалар" || got.Size != "42" || got.Color != "black" || got.Price != 3000 ||
		!got.IsActive || got.InStock != 10 || !got.Available {
		t.Errorf("line = %+v", got)
	}
	const wantPhotoURL = "http://minio.local/cozy-media/products/prod-1.jpg"
	if got.PhotoURL == nil || *got.PhotoURL != wantPhotoURL {
		t.Errorf("photo_url = %v, want %s", got.PhotoURL, wantPhotoURL)
	}
	// A legacy single-file key has no thumb variant: thumb_url == photo_url.
	if got.ThumbURL == nil || *got.ThumbURL != wantPhotoURL {
		t.Errorf("thumb_url = %v, want %s", got.ThumbURL, wantPhotoURL)
	}
}

// TestListCartHandlerThumbURLForNormalizedPhoto checks that a photo stored
// by POST /admin/api/media/upload (…/full.jpg) gets its …/thumb.jpg
// variant as thumb_url, while photo_url stays the full one.
func TestListCartHandlerThumbURLForNormalizedPhoto(t *testing.T) {
	f := newCartHandlerFakes()
	f.cart.listResult = []orders.CartLine{cartLine("var-1", "prod-1", 1)}
	f.images.images["prod-1"] = catalog.ProductImage{ID: "img-1", ProductID: "prod-1", ObjectKey: "products/abc/full.jpg"}

	rec := httptest.NewRecorder()
	f.handler().ServeHTTP(rec, newCustomerRequest(http.MethodGet, "/api/v1/cart", "cust-1", ""))
	lines := decodeCart(t, rec)
	if got := lines[0].PhotoURL; got == nil || *got != "http://minio.local/cozy-media/products/abc/full.jpg" {
		t.Errorf("photo_url = %v", got)
	}
	if got := lines[0].ThumbURL; got == nil || *got != "http://minio.local/cozy-media/products/abc/thumb.jpg" {
		t.Errorf("thumb_url = %v", got)
	}
}

// TestListCartHandlerMarksUnavailableLines: deactivated products and lines
// no store can cover stay in the response but with available=false.
func TestListCartHandlerMarksUnavailableLines(t *testing.T) {
	f := newCartHandlerFakes()
	inactive := cartLine("var-1", "prod-1", 1)
	inactive.ProductActive = false
	short := cartLine("var-2", "prod-2", 3)
	short.InStock = 1
	f.cart.listResult = []orders.CartLine{inactive, short}

	rec := httptest.NewRecorder()
	f.handler().ServeHTTP(rec, newCustomerRequest(http.MethodGet, "/api/v1/cart", "cust-1", ""))
	lines := decodeCart(t, rec)
	if len(lines) != 2 || lines[0].Available || lines[0].IsActive || lines[1].Available || lines[1].InStock != 1 {
		t.Fatalf("lines = %+v, want both unavailable", lines)
	}
}

func TestListCartHandlerPropagatesListError(t *testing.T) {
	f := newCartHandlerFakes()
	f.cart.listErr = errUnexpected
	rec := httptest.NewRecorder()
	f.handler().ServeHTTP(rec, newCustomerRequest(http.MethodGet, "/api/v1/cart", "cust-1", ""))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
	}
}

// TestListCartHandlerEmptyCartReturnsEmptyArray checks the response body is
// `[]`, not `null`, and that no image lookup runs for an empty cart.
func TestListCartHandlerEmptyCartReturnsEmptyArray(t *testing.T) {
	f := newCartHandlerFakes()
	f.cart.listResult = []orders.CartLine{}

	rec := httptest.NewRecorder()
	f.handler().ServeHTTP(rec, newCustomerRequest(http.MethodGet, "/api/v1/cart", "cust-1", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("body = %q, want []", got)
	}
	if f.images.calls != 0 {
		t.Errorf("image lookup ran for an empty cart")
	}
}

// TestListCartHandlerBatchesImageLookup checks PrimaryForProducts is called
// exactly once with the de-duplicated set of product IDs — not once per
// line, even when two lines share a product (two sizes of the same shoe).
func TestListCartHandlerBatchesImageLookup(t *testing.T) {
	f := newCartHandlerFakes()
	f.cart.listResult = []orders.CartLine{cartLine("var-1", "prod-1", 1), cartLine("var-2", "prod-1", 1)}

	rec := httptest.NewRecorder()
	f.handler().ServeHTTP(rec, newCustomerRequest(http.MethodGet, "/api/v1/cart", "cust-1", ""))
	decodeCart(t, rec)
	if f.images.calls != 1 {
		t.Fatalf("PrimaryForProducts called %d times, want 1", f.images.calls)
	}
	if len(f.images.lastCallIDs) != 1 || f.images.lastCallIDs[0] != "prod-1" {
		t.Errorf("PrimaryForProducts called with %v, want [prod-1] (de-duplicated)", f.images.lastCallIDs)
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
