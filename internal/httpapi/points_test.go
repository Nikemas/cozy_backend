package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/storefront"
)

// fakeBranchLister is an in-memory branchLister for tests, so the handler
// test doesn't need a live database — mirrors fakeFavoriteRepo in
// favorites_test.go.
type fakeBranchLister struct {
	branches []storefront.Branch
}

func (f *fakeBranchLister) List(_ context.Context) ([]storefront.Branch, error) {
	return f.branches, nil
}

func TestListPointsHandlerReturnsResults(t *testing.T) {
	fake := &fakeBranchLister{branches: []storefront.Branch{
		{ID: "b1", Name: "ЦУМ", Address: "ул. Советская, 1"},
		{ID: "b2", Name: "Vefa", Address: "пр. Чуй, 10"},
	}}
	handler := apperr.Wrap(listPointsHandler(fake))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/points", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"id":"b1"`) || !strings.Contains(body, `"name":"ЦУМ"`) || !strings.Contains(body, `"address":"ул. Советская, 1"`) {
		t.Errorf("body = %s, want branch b1 fields", body)
	}
	if !strings.Contains(body, `"id":"b2"`) {
		t.Errorf("body = %s, want branch b2", body)
	}
}

func TestListPointsHandlerEmptyListReturnsEmptyArrayNotNull(t *testing.T) {
	handler := apperr.Wrap(listPointsHandler(&fakeBranchLister{}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/points", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := strings.TrimSpace(rec.Body.String())
	want := `{"items":[]}`
	if body != want {
		t.Errorf("body = %s, want %s (empty array, not null)", body, want)
	}
}

func TestListPointsHandlerNoAuthRequired(t *testing.T) {
	handler := apperr.Wrap(listPointsHandler(&fakeBranchLister{}))

	// No customer in context at all — this endpoint must still succeed,
	// unlike favorites/addresses which require authSvc.RequireCustomer.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/points", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (public, no auth needed): %s", rec.Code, rec.Body.String())
	}
}
