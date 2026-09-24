package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/i18n"
)

func TestRobotsDisallowsPrivatePagesAndUsesAbsoluteSitemap(t *testing.T) {
	h := newTestHandlers(t)
	w := httptest.NewRecorder()
	h.robots(w, httptest.NewRequest(http.MethodGet, "/robots.txt", nil))

	body := w.Body.String()
	for _, want := range []string{"Disallow: /cart", "Disallow: /checkout", "Disallow: /profile", "Disallow: /api/", "Sitemap: https://cozy.test/sitemap.xml"} {
		if !strings.Contains(body, want) {
			t.Errorf("robots.txt missing %q:\n%s", want, body)
		}
	}
}

func TestBuildSitemapIncludesHomeLastmodAndStaticPages(t *testing.T) {
	t1 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	tree := []*catalog.Category{{Slug: "krossovki", Children: []*catalog.Category{{Slug: "begovye"}}}}
	products := []catalog.Product{
		{ID: "11111111-1111-1111-1111-111111111111", NameRu: "Кроссовки Air", UpdatedAt: t1},
		{ID: "22222222-2222-2222-2222-222222222222", NameRu: "Ботинки", UpdatedAt: t2},
	}

	set := buildSitemap("https://cozy.kg", tree, products)
	if set.URLs[0].Loc != "https://cozy.kg/" || set.URLs[0].LastMod != "2026-09-20" {
		t.Fatalf("first entry = %+v, want home with the newest lastmod", set.URLs[0])
	}
	locs := map[string]string{}
	for _, u := range set.URLs {
		if !strings.HasPrefix(u.Loc, "https://cozy.kg/") {
			t.Errorf("non-absolute loc %q", u.Loc)
		}
		locs[u.Loc] = u.LastMod
	}
	for _, want := range []string{
		"https://cozy.kg/catalog/krossovki",
		"https://cozy.kg/catalog/begovye",
		"https://cozy.kg/privacy",
		"https://cozy.kg/delivery",
	} {
		if _, ok := locs[want]; !ok {
			t.Errorf("sitemap missing %s", want)
		}
	}
	if got := locs["https://cozy.kg/product/11111111-1111-1111-1111-111111111111-krossovki-air"]; got != "2026-09-01" {
		t.Errorf("product lastmod = %q, want 2026-09-01", got)
	}
}

func TestCanonicalRedirect(t *testing.T) {
	canonical := "/product/11111111-1111-1111-1111-111111111111-krossovki-air"

	r := httptest.NewRequest(http.MethodGet, "/product/11111111-1111-1111-1111-111111111111-old-name?size=42", nil)
	if got, ok := canonicalRedirect(r, canonical); !ok || got != canonical+"?size=42" {
		t.Fatalf("stale slug: got %q %v, want redirect to canonical with query", got, ok)
	}

	r = httptest.NewRequest(http.MethodGet, canonical, nil)
	if _, ok := canonicalRedirect(r, canonical); ok {
		t.Fatal("canonical path must not redirect")
	}

	r = httptest.NewRequest(http.MethodGet, "/product/11111111-1111-1111-1111-111111111111", nil)
	r.Header.Set("HX-Request", "true")
	if _, ok := canonicalRedirect(r, canonical); ok {
		t.Fatal("HTMX requests must not be redirected")
	}
}

func TestProductJSONLD(t *testing.T) {
	pd := &ProductData{Name: "Air </script>", Brand: "Nike", InStock: true, Photos: []ProductPhoto{{URL: "https://m/1/full.jpg"}}}
	raw := string(productJSONLD(pd, "https://cozy.kg/product/x", 4990))
	if strings.Contains(raw, "</script>") {
		t.Fatal("JSON-LD must escape </script>")
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("invalid JSON-LD: %v", err)
	}
	offer := doc["offers"].(map[string]any)
	if doc["@type"] != "Product" || offer["price"] != "4990" || offer["priceCurrency"] != "KGS" || offer["availability"] != "https://schema.org/InStock" {
		t.Fatalf("JSON-LD = %s", raw)
	}
}

func TestStaticPageHeadHasSEOTags(t *testing.T) {
	mux := newTestMux(t)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/terms", nil))
	body := w.Body.String()
	for _, want := range []string{
		"<title>Публичная оферта | Cozy</title>",
		`<meta name="description" content="Публичная оферта интернет-магазина`,
		`<link rel="canonical" href="https://cozy.test/terms">`,
		`hreflang="ky" href="https://cozy.test/terms?lang=ky"`,
		`<meta property="og:image" content="https://cozy.test/static/img/og.png">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/terms head missing %q", want)
		}
	}
	if strings.Contains(body, `name="robots"`) {
		t.Error("/terms must be indexable")
	}
}

func TestLangParamSwitchesLanguageAndPersistsIt(t *testing.T) {
	mux := newTestMux(t)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/about?lang=ky", nil))

	body := w.Body.String()
	if !strings.Contains(body, `<html lang="ky">`) || !strings.Contains(body, "Cozy дүкөнү жөнүндө") {
		t.Fatal("?lang=ky should render the Kyrgyz page")
	}
	if !strings.Contains(body, `<link rel="canonical" href="https://cozy.test/about?lang=ky">`) {
		t.Error("Kyrgyz alternate should be canonical to itself")
	}
	var found bool
	for _, c := range w.Result().Cookies() {
		if c.Name == langCookieName && c.Value == i18n.LangKY {
			found = true
		}
	}
	if !found {
		t.Error("?lang=ky should set the language cookie")
	}
}

func TestPrivateScreensAreNoIndexWithoutCanonical(t *testing.T) {
	h := newTestHandlers(t)
	data := h.base(httptest.NewRequest(http.MethodGet, "/cart", nil), "cart")
	if !data.NoIndex || data.SEO.Canonical != "" {
		t.Fatalf("cart: NoIndex=%v Canonical=%q, want noindex and no canonical", data.NoIndex, data.SEO.Canonical)
	}
	data = h.base(httptest.NewRequest(http.MethodGet, "/branches", nil), "branches")
	if data.NoIndex || data.SEO.Canonical != "https://cozy.test/branches" {
		t.Fatalf("branches: NoIndex=%v Canonical=%q", data.NoIndex, data.SEO.Canonical)
	}
}

func TestTruncateText(t *testing.T) {
	if got := truncateText("  коротко  ", 160); got != "коротко" {
		t.Errorf("short = %q", got)
	}
	long := strings.Repeat("слово ", 60)
	got := truncateText(long, 50)
	if n := len([]rune(got)); n > 50 || !strings.HasSuffix(got, "…") {
		t.Errorf("long = %q (%d runes), want ≤50 ending with …", got, n)
	}
}
