package httpapi

import (
	"database/sql/driver"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/config"
)

// stringSliceConverter lets sqlmock accept the []string arg the facets query
// binds for category_id = ANY($1) (lib/pq handles it in production).
type stringSliceConverter struct{}

func (stringSliceConverter) ConvertValue(v any) (driver.Value, error) {
	if s, ok := v.([]string); ok {
		return s, nil
	}
	return driver.DefaultParameterConverter.ConvertValue(v)
}

func newFacetsMux(t *testing.T) (*http.ServeMux, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.ValueConverterOption(stringSliceConverter{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mux := http.NewServeMux()
	RegisterCatalogRoutes(mux, db, &config.Config{})
	return mux, mock
}

func TestParseFacetFilterKeepsOnlyScopeParams(t *testing.T) {
	got := parseFacetFilter(url.Values{
		"category": {"sneakers"}, "q": {"nike"},
		"size": {"42"}, "color": {"black"}, "price_min": {"oops"}, "in_stock": {"1"},
	})
	want := catalog.FacetFilter{CategoryID: "sneakers", Query: "nike"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseFacetFilter = %+v, want %+v", got, want)
	}
}

// "facets" must not be swallowed by GET /api/v1/products/{id}: the more
// specific pattern wins, so no products-by-id lookup happens.
func TestProductFacetsRouteWinsOverProductID(t *testing.T) {
	mux, mock := newFacetsMux(t)

	mock.ExpectQuery(`SELECT id FROM categories WHERE slug = \$1`).WithArgs("sneakers").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("11111111-1111-1111-1111-111111111111"))
	mock.ExpectQuery(`FROM categories\s+ORDER BY sort_order`).WillReturnRows(
		sqlmock.NewRows([]string{"id", "parent_id", "name_ru", "name_ky", "slug", "sort_order", "image_key"}).
			AddRow("11111111-1111-1111-1111-111111111111", nil, "Кроссовки", "Кроссовки", "sneakers", 0, nil).
			AddRow("22222222-2222-2222-2222-222222222222", "11111111-1111-1111-1111-111111111111", "Беговые", "Беговые", "running", 0, nil))
	subtree := []string{"11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"}
	mock.ExpectQuery(`SELECT DISTINCT pv.size, pv.color.*category_id = ANY\(\$1\)`).
		WithArgs(subtree, "%nike%").
		WillReturnRows(sqlmock.NewRows([]string{"size", "color"}).
			AddRow("42", "Черный").AddRow("38,5", "белый").AddRow("38,5", "Черный"))
	mock.ExpectQuery(`SELECT MIN\(`).
		WithArgs(subtree, "%nike%").
		WillReturnRows(sqlmock.NewRows([]string{"min", "max"}).AddRow(2500.0, 9000.0))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/products/facets?category=sneakers&q=nike&size=40", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); !strings.HasPrefix(got, "public, max-age=30") {
		t.Errorf("Cache-Control = %q, want public max-age=30", got)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"sizes":     []any{"38,5", "42"},
		"colors":    []any{"Черный", "белый"},
		"price_min": 2500.0,
		"price_max": 9000.0,
	}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("body = %v, want %v", body, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestProductFacetsEmptyScopeReturnsEmptyArraysAndNullPrices(t *testing.T) {
	mux, mock := newFacetsMux(t)
	mock.ExpectQuery(`SELECT DISTINCT`).WillReturnRows(sqlmock.NewRows([]string{"size", "color"}))
	mock.ExpectQuery(`SELECT MIN\(`).WillReturnRows(sqlmock.NewRows([]string{"min", "max"}).AddRow(nil, nil))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/products/facets", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if got, want := strings.TrimSpace(rec.Body.String()), `{"sizes":[],"colors":[],"price_min":null,"price_max":null}`; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func TestProductFacetsUnknownCategoryIs404Uncached(t *testing.T) {
	mux, mock := newFacetsMux(t)
	mock.ExpectQuery(`SELECT id FROM categories WHERE slug = \$1`).WithArgs("nope").
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/products/facets?category=nope", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "" {
		t.Errorf("Cache-Control = %q, want none on errors", rec.Header().Get("Cache-Control"))
	}
}

// A real product id still reaches the detail handler.
func TestProductIDRouteStillMatchesOtherSegments(t *testing.T) {
	mux, mock := newFacetsMux(t)
	mock.ExpectQuery(`FROM products`).WithArgs("not-facets").
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/products/not-facets", nil))

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("detail handler not reached: %v (status %d)", err, rec.Code)
	}
}
