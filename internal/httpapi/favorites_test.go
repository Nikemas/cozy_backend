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

func withCustomer(r *http.Request, customerID string) *http.Request {
	return r.WithContext(auth.NewContextWithCustomerID(r.Context(), customerID))
}

func TestListFavoritesHandlerReturnsFullProducts(t *testing.T) {
	favorites := &fakeFavoriteRepo{ids: []string{"p1", "p2", "gone"}}
	products := &fakeProductGetter{products: map[string]*catalog.Product{
		"p1": {ID: "p1", NameRu: "Кроссовки"},
		"p2": {ID: "p2", NameRu: "Ботинки"},
	}}
	handler := apperr.Wrap(listFavoritesHandler(favorites, products))

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
}

func TestListFavoritesHandlerPropagatesLookupError(t *testing.T) {
	favorites := &fakeFavoriteRepo{ids: []string{"p1"}}
	handler := apperr.Wrap(listFavoritesHandler(favorites, &fakeProductGetter{err: errors.New("db down")}))

	req := withCustomer(httptest.NewRequest(http.MethodGet, "/api/v1/favorites", nil), "customer-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 on a real DB error (not a silently empty list)", rec.Code)
	}
}

func TestListFavoritesHandlerRequiresAuth(t *testing.T) {
	handler := apperr.Wrap(listFavoritesHandler(&fakeFavoriteRepo{}, &fakeProductGetter{}))

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
