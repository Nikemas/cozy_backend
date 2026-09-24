package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/auth"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/config"
)

// fakeFavoriteRepo is an in-memory favoriteLister+favoriteWriter for tests,
// recording calls so a test can assert what the handler did without a live
// database.
type fakeFavoriteRepo struct {
	ids []string

	addedCustomerID, addedProductID     string
	removedCustomerID, removedProductID string
}

func (f *fakeFavoriteRepo) ListProductIDs(_ context.Context, _ string) ([]string, error) {
	return f.ids, nil
}

func (f *fakeFavoriteRepo) Add(_ context.Context, customerID, productID string) error {
	f.addedCustomerID, f.addedProductID = customerID, productID
	return nil
}

func (f *fakeFavoriteRepo) Remove(_ context.Context, customerID, productID string) error {
	f.removedCustomerID, f.removedProductID = customerID, productID
	return nil
}

// fakeProductGetter is an in-memory favoriteProductGetter, returning only
// the known IDs (as GetActiveByIDs omits missing/inactive ones) — so tests
// can exercise the "skip products that no longer resolve" behavior. calls
// counts batch lookups, to pin the handler to a single query.
type fakeProductGetter struct {
	products map[string]*catalog.Product
	err      error
	calls    int
}

func (f *fakeProductGetter) GetActiveByIDs(_ context.Context, ids []string) (map[string]catalog.Product, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]catalog.Product{}
	for _, id := range ids {
		if p, ok := f.products[id]; ok {
			out[id] = *p
		}
	}
	return out, nil
}

// fakeDetailSources is an in-memory productDetailSources counting calls,
// so tests can pin the favorites list to a constant number of queries.
type fakeDetailSources struct {
	variants map[string][]catalog.Variant
	stock    []catalog.StockEntry
	images   map[string][]catalog.ProductImage
	calls    int
}

func (f *fakeDetailSources) ListByProductIDs(_ context.Context, ids []string) (map[string][]catalog.Variant, error) {
	f.calls++
	out := map[string][]catalog.Variant{}
	for _, id := range ids {
		if v, ok := f.variants[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

func (f *fakeDetailSources) ByVariantIDs(_ context.Context, _ []string) ([]catalog.StockEntry, error) {
	f.calls++
	return f.stock, nil
}

func (f *fakeDetailSources) ImagesByProductIDs(_ context.Context, ids []string) (map[string][]catalog.ProductImage, error) {
	f.calls++
	out := map[string][]catalog.ProductImage{}
	for _, id := range ids {
		if v, ok := f.images[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

var testCfg = &config.Config{MinIOEndpoint: "media.test", MinIOBucket: "cozy-media"}

func withCustomer(r *http.Request, customerID string) *http.Request {
	return r.WithContext(auth.NewContextWithCustomerID(r.Context(), customerID))
}

func TestListFavoritesHandlerReturnsFullProducts(t *testing.T) {
	favorites := &fakeFavoriteRepo{ids: []string{"p1", "p2", "gone"}}
	products := &fakeProductGetter{products: map[string]*catalog.Product{
		"p1": {ID: "p1", NameRu: "Кроссовки"},
		"p2": {ID: "p2", NameRu: "Ботинки"},
	}}
	details := &fakeDetailSources{
		variants: map[string][]catalog.Variant{"p1": {{ID: "v1", ProductID: "p1", Size: "40", Color: "black"}}},
		stock:    []catalog.StockEntry{{VariantID: "v1", PointID: "pt1", Quantity: 3}},
		images:   map[string][]catalog.ProductImage{"p1": {{ID: "i1", ProductID: "p1", ObjectKey: "products/a.jpg"}}},
	}
	handler := apperr.Wrap(listFavoritesHandler(favorites, products, details, testCfg))

	req := withCustomer(httptest.NewRequest(http.MethodGet, "/api/v1/favorites", nil), "customer-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"id":"p1"`) || !strings.Contains(body, `"id":"p2"`) {
		t.Errorf("body = %s, want both resolvable products", body)
	}
	if strings.Contains(body, `"gone"`) {
		t.Errorf("body = %s, want the unresolvable id skipped entirely", body)
	}
	if strings.Index(body, `"id":"p1"`) > strings.Index(body, `"id":"p2"`) {
		t.Errorf("body = %s, want favorites order (p1 before p2) preserved", body)
	}
	if products.calls != 1 {
		t.Errorf("GetActiveByIDs called %d times, want exactly 1 (no N+1)", products.calls)
	}
	if details.calls != 3 {
		t.Errorf("detail sources called %d times, want 3 batch queries (variants, stock, images)", details.calls)
	}
	// Same shape as GET /api/v1/products/{id}: images + variants with stock.
	for _, want := range []string{
		`"variants":[{"id":"v1"`, `"stock":[{"point_id":"pt1","quantity":3}]`,
		`"images":[{"url":"http://media.test/cozy-media/products/a.jpg"`,
		`"variants":[]`, `"images":[]`, // p2 has neither — empty arrays, not null
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %s: %s", want, body)
		}
	}
}

func TestBuildProductDetailsSingleProductMatchesDetailShape(t *testing.T) {
	src := &fakeDetailSources{variants: map[string][]catalog.Variant{"p1": {{ID: "v1", ProductID: "p1"}}}}
	got, err := buildProductDetails(context.Background(), src, testCfg, []catalog.Product{{ID: "p1"}})
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v, %v", got, err)
	}
	if len(got[0].Variants) != 1 || got[0].Variants[0].Stock == nil || got[0].Images == nil {
		t.Errorf("detail = %+v, want non-nil stock/images slices", got[0])
	}
}

func TestListFavoritesHandlerPropagatesLookupError(t *testing.T) {
	favorites := &fakeFavoriteRepo{ids: []string{"p1"}}
	handler := apperr.Wrap(listFavoritesHandler(favorites, &fakeProductGetter{err: errors.New("db down")}, &fakeDetailSources{}, testCfg))

	req := withCustomer(httptest.NewRequest(http.MethodGet, "/api/v1/favorites", nil), "customer-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 on a real DB error (not a silently empty list)", rec.Code)
	}
}

func TestListFavoritesHandlerRequiresAuth(t *testing.T) {
	handler := apperr.Wrap(listFavoritesHandler(&fakeFavoriteRepo{}, &fakeProductGetter{}, &fakeDetailSources{}, testCfg))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/favorites", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAddFavoriteHandlerAddsForAuthenticatedCustomer(t *testing.T) {
	fake := &fakeFavoriteRepo{}
	handler := apperr.Wrap(addFavoriteHandler(fake))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/favorites/prod-1", nil)
	req.SetPathValue("productId", "prod-1")
	req = withCustomer(req, "customer-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if fake.addedCustomerID != "customer-1" || fake.addedProductID != "prod-1" {
		t.Errorf("Add called with (%q, %q), want (customer-1, prod-1)", fake.addedCustomerID, fake.addedProductID)
	}
}

func TestRemoveFavoriteHandlerRemovesForAuthenticatedCustomer(t *testing.T) {
	fake := &fakeFavoriteRepo{}
	handler := apperr.Wrap(removeFavoriteHandler(fake))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/favorites/prod-1", nil)
	req.SetPathValue("productId", "prod-1")
	req = withCustomer(req, "customer-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if fake.removedCustomerID != "customer-1" || fake.removedProductID != "prod-1" {
		t.Errorf("Remove called with (%q, %q), want (customer-1, prod-1)", fake.removedCustomerID, fake.removedProductID)
	}
}
