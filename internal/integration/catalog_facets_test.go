//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/httpapi"
)

// GET /api/v1/products/facets returns the sizes/colors/price range of the
// active products in scope (category by id or slug incl. subcategories,
// search q) — and the route is not shadowed by GET /api/v1/products/{id}.
func TestProductFacetsEndpoint(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	f := newFixture(t, 1, 1) // active, base 2500: 40/black, 41/black @3000
	tag := uuid.NewString()[:8]

	mustScan := func(q string, args ...any) string {
		t.Helper()
		var id string
		if err := testDB.QueryRowContext(ctx, q, args...).Scan(&id); err != nil {
			t.Fatalf("%v\nquery: %s", err, q)
		}
		return id
	}
	addProduct := func(categoryID, name string, active bool, size, color string, override any) {
		t.Helper()
		productID := mustScan(`INSERT INTO products (category_id, name_ru, name_ky, base_price, is_active)
			VALUES ($1, $2, $2, 2000, $3) RETURNING id`, categoryID, name, active)
		mustScan(`INSERT INTO product_variants (product_id, size, color, sku, price_override)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`, productID, size, color, "IT-FCT-"+uuid.NewString()[:8], override)
	}
	otherCategory := mustScan(`INSERT INTO categories (name_ru, name_ky, slug) VALUES ($1, $1, $2) RETURNING id`,
		"Другое "+tag, "it-other-"+tag)

	subCategory := mustScan(`INSERT INTO categories (parent_id, name_ru, name_ky, slug) VALUES ($1, $2, $2, $3) RETURNING id`,
		f.CategoryID, "Подраздел "+tag, "it-sub-"+tag)

	addProduct(f.CategoryID, "Ботинки фасет "+tag, true, "39,5", "Чёрный", 1800)
	addProduct(subCategory, "Детские фасет "+tag, true, "33", "белый", nil)
	addProduct(f.CategoryID, "Скрытый фасет "+tag, false, "45", "red", 100)
	addProduct(otherCategory, "Чужой фасет "+tag, true, "44", "green", nil)

	mux := http.NewServeMux()
	httpapi.RegisterCatalogRoutes(mux, testDB, &config.Config{})
	get := func(query url.Values) (int, map[string]any) {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/products/facets?"+query.Encode(), nil).WithContext(ctx))
		var body map[string]any
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code, body
	}

	slug := mustScan(`SELECT slug FROM categories WHERE id = $1`, f.CategoryID)
	code, body := get(url.Values{"category": {slug}})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	// Parent category by slug includes its subcategory's products.
	want := map[string]any{
		"sizes":     []any{"33", "39,5", "40", "41"},
		"colors":    []any{"black", "Чёрный", "белый"},
		"price_min": 1800.0,
		"price_max": 3000.0,
	}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("category facets = %v, want %v", body, want)
	}

	// The subcategory alone.
	_, body = get(url.Values{"category": {subCategory}})
	want = map[string]any{"sizes": []any{"33"}, "colors": []any{"белый"}, "price_min": 2000.0, "price_max": 2000.0}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("subcategory facets = %v, want %v", body, want)
	}

	// Category by id + search narrows to the fixture product only.
	_, body = get(url.Values{"category": {f.CategoryID}, "q": {"Кроссовки IT"}})
	want = map[string]any{
		"sizes":     []any{"40", "41"},
		"colors":    []any{"black"},
		"price_min": 2500.0,
		"price_max": 3000.0,
	}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("search facets = %v, want %v", body, want)
	}

	// Nothing matches: empty lists, null prices.
	_, body = get(url.Values{"category": {otherCategory}, "q": {"нет-такого-" + tag}})
	want = map[string]any{"sizes": []any{}, "colors": []any{}, "price_min": nil, "price_max": nil}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("empty facets = %v, want %v", body, want)
	}

	if code, _ := get(url.Values{"category": {"no-such-" + tag}}); code != http.StatusNotFound {
		t.Errorf("unknown category status = %d, want 404", code)
	}
}
