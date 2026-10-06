//go:build integration

package integration

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/web"
)

// addProductImage attaches a photo row to productID (no MinIO object is
// needed: pages only build URLs from the key).
func addProductImage(t *testing.T, productID, key string) {
	t.Helper()
	if _, err := testDB.ExecContext(ctxT(t),
		`INSERT INTO product_images (product_id, object_key, sort_order) VALUES ($1, $2, 0)`, productID, key); err != nil {
		t.Fatal(err)
	}
}

func productName(t *testing.T, productID string) string {
	t.Helper()
	var name string
	if err := testDB.QueryRowContext(ctxT(t), `SELECT name_ru FROM products WHERE id = $1`, productID).Scan(&name); err != nil {
		t.Fatal(err)
	}
	return name
}

func categorySlug(t *testing.T, categoryID string) string {
	t.Helper()
	var slug string
	if err := testDB.QueryRowContext(ctxT(t), `SELECT slug FROM categories WHERE id = $1`, categoryID).Scan(&slug); err != nil {
		t.Fatal(err)
	}
	return slug
}

// TestWebShopListsFixtureProductUnderEveryFilter renders the home grid and
// the category screen with each query filter against the real catalog.
func TestWebShopListsFixtureProductUnderEveryFilter(t *testing.T) {
	t.Parallel()
	a := app(t)
	f := newFixture(t, 3, 0)
	addProductImage(t, f.ProductID, "products/"+f.ProductID+"/a.jpg")
	name := productName(t, f.ProductID)
	catPath := "/catalog/" + categorySlug(t, f.CategoryID)

	w := a.get(t, "/")
	wantStatus(t, w, http.StatusOK)

	tests := []struct {
		name    string
		query   string
		present bool
	}{
		{"plain", "", true},
		{"search hit", "q=" + url.QueryEscape(name), true},
		{"search miss", "q=zzz-no-such-shoe", false},
		{"size present", "size=40", true},
		{"size absent", "size=46", false},
		{"color", "color=black", true},
		{"in stock", "in_stock=1", true},
		{"price asc", "sort=price_asc", true},
		{"price desc", "sort=price_desc", true},
		{"popular", "sort=popular", true},
		{"price window", "price_min=3000&price_max=2000", true},
		{"price above", "price_min=9000", false},
		{"page 2 empty", "page=2", false},
		{"kyrgyz", "lang=ky", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := a.get(t, catPath+"?"+tc.query)
			wantStatus(t, w, http.StatusOK)
			if got := strings.Contains(w.Body.String(), f.ProductID); got != tc.present {
				t.Fatalf("product present = %v, want %v", got, tc.present)
			}
		})
	}

	// Unknown category: branded 404.
	wantStatus(t, a.get(t, "/catalog/no-such-category-xyz"), http.StatusNotFound)
}

func TestWebProductPageCanonicalRedirectAndVariants(t *testing.T) {
	t.Parallel()
	a := app(t)
	f := newFixture(t, 2, 0)
	addProductImage(t, f.ProductID, "products/"+f.ProductID+"/main.jpg")
	canonical := web.ProductPath(f.ProductID, productName(t, f.ProductID))

	// Bare id 301s to the slugged canonical path, query kept.
	w := a.get(t, "/product/"+f.ProductID+"?size=41")
	wantStatus(t, w, http.StatusMovedPermanently)
	if loc := w.Header().Get("Location"); loc != canonical+"?size=41" {
		t.Fatalf("Location = %q, want %q", loc, canonical+"?size=41")
	}

	for _, q := range []string{"", "?size=40&color=black", "?size=41&color=black", "?size=99", "?lang=ky"} {
		w = a.get(t, canonical+q)
		wantStatus(t, w, http.StatusOK)
		wantBody(t, w, "application/ld+json", "https://media.cozy.test/cozy/products/"+f.ProductID+"/main")
	}

	// Malformed and unknown ids are 404s.
	wantStatus(t, a.get(t, "/product/not-a-uuid"), http.StatusNotFound)
	wantStatus(t, a.get(t, "/product/00000000-0000-0000-0000-000000000000"), http.StatusNotFound)

	// The quick-buy fragment renders without the layout.
	w = a.get(t, "/quickbuy/"+f.ProductID+"?size=40&color=black")
	wantStatus(t, w, http.StatusOK)
	if strings.Contains(w.Body.String(), "<html") {
		t.Fatal("quick-buy must be a fragment, not a full page")
	}
	wantStatus(t, a.get(t, "/quickbuy/00000000-0000-0000-0000-000000000000"), http.StatusNotFound)
}

func TestWebSitemapListsActiveProductsAndCategories(t *testing.T) {
	t.Parallel()
	a := app(t)
	f := newFixture(t, 1, 1)

	w := a.get(t, "/sitemap.xml")
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, "<urlset", "https://cozy.test/product/"+f.ProductID, "/catalog/"+categorySlug(t, f.CategoryID))

	w = a.get(t, "/robots.txt")
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, "Sitemap: https://cozy.test/sitemap.xml")
}

func TestWebBranchesAndLangScreens(t *testing.T) {
	t.Parallel()
	a := app(t)
	f := newFixture(t, 1, 1)
	cookie := a.customerSession(t, f.CustomerID)

	w := a.get(t, "/branches")
	wantStatus(t, w, http.StatusOK)
	wantStatus(t, a.get(t, "/lang"), http.StatusOK)

	// POST /lang stores the cookie and, for a logged-in customer, the DB column.
	w = a.do(t, req{method: http.MethodPost, path: "/lang", form: url.Values{"lang": {"ky"}}, cookies: []*http.Cookie{cookie}})
	wantRedirect(t, w, "/lang")
	if !strings.Contains(w.Header().Get("Set-Cookie"), "cozy_lang=ky") {
		t.Fatalf("Set-Cookie = %q, want cozy_lang=ky", w.Header().Get("Set-Cookie"))
	}
	var lang string
	if err := testDB.QueryRowContext(ctxT(t), `SELECT lang FROM customers WHERE id = $1`, f.CustomerID).Scan(&lang); err != nil {
		t.Fatal(err)
	}
	if lang != "ky" {
		t.Fatalf("customers.lang = %q, want ky", lang)
	}

	// An unsupported value falls back to the default language.
	w = a.do(t, req{method: http.MethodPost, path: "/lang", form: url.Values{"lang": {"en"}}})
	if !strings.Contains(w.Header().Get("Set-Cookie"), "cozy_lang=ru") {
		t.Fatalf("Set-Cookie = %q, want cozy_lang=ru", w.Header().Get("Set-Cookie"))
	}
}
