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

// fakeCartVariantGetter is an in-memory cartVariantGetter for listCartHandler
// tests: variants keyed by id, missing/errID entries model a dangling or
// broken lookup.
type fakeCartVariantGetter struct {
	variants map[string]catalog.Variant
	errByID  map[string]error // takes precedence over "not found" for id
}

func (f *fakeCartVariantGetter) GetByID(_ context.Context, id string) (*catalog.Variant, error) {
	if err, ok := f.errByID[id]; ok {
		return nil, err
	}
	if v, ok := f.variants[id]; ok {
		return &v, nil
	}
	return nil, apperr.NotFound("variant_not_found", "вариация не найдена")
}

// fakeCartProductGetter is an in-memory cartProductGetter for listCartHandler
// tests.
type fakeCartProductGetter struct {
	products map[string]catalog.Product
	errByID  map[string]error
}

func (f *fakeCartProductGetter) GetByIDAny(_ context.Context, id string) (*catalog.Product, error) {
	if err, ok := f.errByID[id]; ok {
		return nil, err
	}
	if p, ok := f.products[id]; ok {
		return &p, nil
	}
	return nil, apperr.NotFound("product_not_found", "товар не найден")
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

// cartHandlerFakes bundles the four fakes listCartHandler depends on, with
// convenience zero-value maps so a test only has to populate what it needs.
type cartHandlerFakes struct {
	cart     *fakeCartService
	variants *fakeCartVariantGetter
	products *fakeCartProductGetter
	images   *fakeCartImageGetter
}

func newCartHandlerFakes() *cartHandlerFakes {
	return &cartHandlerFakes{
		cart:     &fakeCartService{},
		variants: &fakeCartVariantGetter{variants: map[string]catalog.Variant{}, errByID: map[string]error{}},
		products: &fakeCartProductGetter{products: map[string]catalog.Product{}, errByID: map[string]error{}},
		images:   &fakeCartImageGetter{images: map[string]catalog.ProductImage{}},
	}
}

func (f *cartHandlerFakes) handler() http.Handler {
	cfg := &config.Config{MinIOEndpoint: "minio.local", MinIOBucket: "cozy-media"}
	return apperr.Wrap(listCartHandler(f.cart, f.variants, f.products, f.images, cfg))
}

func TestListCartHandlerScopesToCustomer(t *testing.T) {
	f := newCartHandlerFakes()
	f.cart.listResult = []orders.CartItem{{CustomerID: "cust-1", VariantID: "var-1", Qty: 2}}
	f.variants.variants["var-1"] = catalog.Variant{ID: "var-1", ProductID: "prod-1", Size: "42", Color: "black"}
	f.products.products["prod-1"] = catalog.Product{ID: "prod-1", NameRu: "Кроссовки", NameKy: "Кроссовкалар", BasePrice: 3000}
	handler := f.handler()

	req := newCustomerRequest(http.MethodGet, "/api/v1/cart", "cust-1", "")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !f.cart.listCalled || f.cart.lastCustomerID != "cust-1" {
		t.Fatalf("expected List called with cust-1, got called=%v customerID=%q", f.cart.listCalled, f.cart.lastCustomerID)
	}
}

func TestListCartHandlerUnauthenticatedWithoutContext(t *testing.T) {
	f := newCartHandlerFakes()
	handler := f.handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cart", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body.String())
	}
	if f.cart.listCalled {
		t.Fatal("expected List NOT to be called without an authenticated customer")
	}
}

// TestListCartHandlerEnrichesLine checks the happy path: one full cart line
// enriched with variant size/color, product name/base_price and its
// primary image's object_key.
func TestListCartHandlerEnrichesLine(t *testing.T) {
	f := newCartHandlerFakes()
	f.cart.listResult = []orders.CartItem{{CustomerID: "cust-1", VariantID: "var-1", Qty: 2}}
	f.variants.variants["var-1"] = catalog.Variant{ID: "var-1", ProductID: "prod-1", Size: "42", Color: "black"}
	f.products.products["prod-1"] = catalog.Product{ID: "prod-1", NameRu: "Кроссовки", NameKy: "Кроссовкалар", BasePrice: 3000}
	f.images.images["prod-1"] = catalog.ProductImage{ID: "img-1", ProductID: "prod-1", ObjectKey: "products/prod-1.jpg"}
	handler := f.handler()

	req := newCustomerRequest(http.MethodGet, "/api/v1/cart", "cust-1", "")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var lines []cartLineResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &lines); err != nil {
		t.Fatalf("unmarshal response: %v (%s)", err, rec.Body.String())
	}
	if len(lines) != 1 {
		t.Fatalf("len(lines) = %d, want 1: %+v", len(lines), lines)
	}
	got := lines[0]
	want := cartLineResponse{
		VariantID: "var-1", Quantity: 2, ProductID: "prod-1",
		ProductName: "Кроссовки", ProductNameKy: "Кроссовкалар",
		Size: "42", Color: "black", Price: 3000,
	}
	if got.VariantID != want.VariantID || got.Quantity != want.Quantity || got.ProductID != want.ProductID ||
		got.ProductName != want.ProductName || got.ProductNameKy != want.ProductNameKy ||
		got.Size != want.Size || got.Color != want.Color || got.Price != want.Price {
		t.Errorf("line = %+v, want %+v (photo_url aside)", got, want)
	}
	const wantPhotoURL = "http://minio.local/cozy-media/products/prod-1.jpg"
	if got.PhotoURL == nil || *got.PhotoURL != wantPhotoURL {
		t.Errorf("photo_url = %v, want %s", got.PhotoURL, wantPhotoURL)
	}
}

// TestListCartHandlerUsesPriceOverride checks that a variant's
// price_override wins over the product's base_price, mirroring
// loadVariantSnapshots in internal/orders/order.go.
func TestListCartHandlerUsesPriceOverride(t *testing.T) {
	f := newCartHandlerFakes()
	override := 2500.0
	f.cart.listResult = []orders.CartItem{{CustomerID: "cust-1", VariantID: "var-1", Qty: 1}}
	f.variants.variants["var-1"] = catalog.Variant{ID: "var-1", ProductID: "prod-1", Size: "42", Color: "black", PriceOverride: &override}
	f.products.products["prod-1"] = catalog.Product{ID: "prod-1", NameRu: "Кроссовки", NameKy: "Кроссовкалар", BasePrice: 3000}
	handler := f.handler()

	req := newCustomerRequest(http.MethodGet, "/api/v1/cart", "cust-1", "")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	var lines []cartLineResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &lines); err != nil {
		t.Fatalf("unmarshal response: %v (%s)", err, rec.Body.String())
	}
	if len(lines) != 1 || lines[0].Price != 2500 {
		t.Fatalf("lines = %+v, want single line with price 2500", lines)
	}
}

// TestListCartHandlerSkipsDanglingVariant checks that a cart line whose
// variant has been hard-deleted since being added is silently skipped, not
// a whole-request failure — see the doc comment on listCartHandler.
func TestListCartHandlerSkipsDanglingVariant(t *testing.T) {
	f := newCartHandlerFakes()
	f.cart.listResult = []orders.CartItem{
		{CustomerID: "cust-1", VariantID: "var-gone", Qty: 1},
		{CustomerID: "cust-1", VariantID: "var-1", Qty: 2},
	}
	f.variants.variants["var-1"] = catalog.Variant{ID: "var-1", ProductID: "prod-1", Size: "42", Color: "black"}
	f.products.products["prod-1"] = catalog.Product{ID: "prod-1", NameRu: "Кроссовки", NameKy: "Кроссовкалар", BasePrice: 3000}
	// var-gone has no entry in f.variants.variants, so fakeCartVariantGetter
	// returns apperr.NotFound for it, as VariantRepo.GetByID would for a
	// hard-deleted variant.
	handler := f.handler()

	req := newCustomerRequest(http.MethodGet, "/api/v1/cart", "cust-1", "")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (dangling line skipped, not a failure): %s", rec.Code, rec.Body.String())
	}
	var lines []cartLineResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &lines); err != nil {
		t.Fatalf("unmarshal response: %v (%s)", err, rec.Body.String())
	}
	if len(lines) != 1 || lines[0].VariantID != "var-1" {
		t.Fatalf("lines = %+v, want only the var-1 line to survive", lines)
	}
}

// TestListCartHandlerSkipsDanglingProduct is the same skip behavior, but
// for a variant whose product row is gone (e.g. products.GetByIDAny
// returns NotFound) rather than the variant itself.
func TestListCartHandlerSkipsDanglingProduct(t *testing.T) {
	f := newCartHandlerFakes()
	f.cart.listResult = []orders.CartItem{{CustomerID: "cust-1", VariantID: "var-1", Qty: 1}}
	f.variants.variants["var-1"] = catalog.Variant{ID: "var-1", ProductID: "prod-gone", Size: "42", Color: "black"}
	// prod-gone has no entry in f.products.products.
	handler := f.handler()

	req := newCustomerRequest(http.MethodGet, "/api/v1/cart", "cust-1", "")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var lines []cartLineResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &lines); err != nil {
		t.Fatalf("unmarshal response: %v (%s)", err, rec.Body.String())
	}
	if len(lines) != 0 {
		t.Fatalf("lines = %+v, want empty (only line's product is dangling)", lines)
	}
}

// TestListCartHandlerPropagatesUnexpectedVariantError checks that a
// non-NotFound error from the variant lookup fails the whole request
// instead of being swallowed as "skip" — unlike listFavoritesHandler, which
// treats any lookup error as skip.
func TestListCartHandlerPropagatesUnexpectedVariantError(t *testing.T) {
	f := newCartHandlerFakes()
	f.cart.listResult = []orders.CartItem{{CustomerID: "cust-1", VariantID: "var-1", Qty: 1}}
	f.variants.errByID["var-1"] = apperr.Internal(errUnexpected)
	handler := f.handler()

	req := newCustomerRequest(http.MethodGet, "/api/v1/cart", "cust-1", "")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
	}
}

// TestListCartHandlerEmptyCartReturnsEmptyArray checks the response body is
// `[]`, not `null`, for a customer with no cart lines — matters to any
// client that does a straight JSON-array decode without a null check.
func TestListCartHandlerEmptyCartReturnsEmptyArray(t *testing.T) {
	f := newCartHandlerFakes()
	f.cart.listResult = []orders.CartItem{}
	handler := f.handler()

	req := newCustomerRequest(http.MethodGet, "/api/v1/cart", "cust-1", "")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("body = %q, want []", got)
	}
}

// TestListCartHandlerBatchesImageLookup checks PrimaryForProducts is called
// exactly once with the full, de-duplicated set of product IDs in the
// cart — not once per line, even when two lines share a product (two
// variants of the same shoe).
func TestListCartHandlerBatchesImageLookup(t *testing.T) {
	f := newCartHandlerFakes()
	f.cart.listResult = []orders.CartItem{
		{CustomerID: "cust-1", VariantID: "var-1", Qty: 1},
		{CustomerID: "cust-1", VariantID: "var-2", Qty: 1},
	}
	f.variants.variants["var-1"] = catalog.Variant{ID: "var-1", ProductID: "prod-1", Size: "41", Color: "black"}
	f.variants.variants["var-2"] = catalog.Variant{ID: "var-2", ProductID: "prod-1", Size: "42", Color: "black"}
	f.products.products["prod-1"] = catalog.Product{ID: "prod-1", NameRu: "Кроссовки", NameKy: "Кроссовкалар", BasePrice: 3000}
	handler := f.handler()

	req := newCustomerRequest(http.MethodGet, "/api/v1/cart", "cust-1", "")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
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
