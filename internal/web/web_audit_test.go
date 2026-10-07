package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/i18n"
)

func TestLayoutLinksContentHashedAssetsAndPreconnects(t *testing.T) {
	rr := newTestRenderer(t)
	rr.SetMediaOrigin("https://media.cozy.test")
	w := httptest.NewRecorder()
	if err := rr.Render(w, "lang", PageData{Lang: i18n.LangRU, Screen: "lang"}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := w.Body.String()

	for _, want := range []string{
		`href="/static/css/site.css?v=` + rr.assets["css/site.css"] + `"`,
		`src="/static/img/logo-64.png?v=` + rr.assets["img/logo-64.png"] + `"`,
		`<link rel="preconnect" href="https://media.cozy.test">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("layout missing %q", want)
		}
	}
	if len(rr.assets["css/site.css"]) != assetHashLen {
		t.Errorf("site.css hash = %q, want %d hex chars", rr.assets["css/site.css"], assetHashLen)
	}
}

func TestAssetURLFallsBackToPlainPathForUnknownFile(t *testing.T) {
	v := assetVersions{"css/site.css": "abc"}
	if got := v.url("css/site.css"); got != "/static/css/site.css?v=abc" {
		t.Errorf("known asset = %q", got)
	}
	if got := v.url("img/missing.png"); got != "/static/img/missing.png" {
		t.Errorf("unknown asset = %q", got)
	}
}

func TestStaticHandlerCachesLongOnlyForCurrentHash(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "css"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "css", "a.css"), []byte("body{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	versions, err := loadAssetVersions(dir)
	if err != nil {
		t.Fatalf("loadAssetVersions: %v", err)
	}
	h := http.StripPrefix("/static/", staticHandler(dir, versions))

	cases := []struct {
		name, url, wantCache string
	}{
		{"current hash", "/static/css/a.css?v=" + versions["css/a.css"], "public, max-age=31536000"},
		{"stale hash", "/static/css/a.css?v=0000000000", "public, max-age=600"},
		{"no hash", "/static/css/a.css", "public, max-age=600"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.url, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d", w.Code)
			}
			if got := w.Header().Get("Cache-Control"); got != tc.wantCache {
				t.Errorf("Cache-Control = %q, want %q", got, tc.wantCache)
			}
		})
	}
}

func TestOriginOf(t *testing.T) {
	cases := map[string]string{
		"https://media.cozy.kg/cozy/_": "https://media.cozy.kg",
		"http://localhost:9000/b/_":    "http://localhost:9000",
		"not a url":                    "",
	}
	for in, want := range cases {
		if got := originOf(in); got != want {
			t.Errorf("originOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShopGridLazyLoadsOnlyBelowTheFold(t *testing.T) {
	rr := newTestRenderer(t)
	products := make([]ProductCard, eagerCardCount+2)
	for i := range products {
		products[i] = ProductCard{ID: "p", Name: "Air", PriceText: "1 сом", DetailURL: "/product/p", HasPhoto: true, PhotoURL: "https://m/thumb.jpg"}
	}
	sd := &ShopData{BasePath: "/", Page: 1, TotalPages: 1, Products: products, Total: len(products)}
	w := httptest.NewRecorder()
	if err := rr.Render(w, "shop", PageData{Lang: i18n.LangRU, Screen: "shop", Data: sd}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := w.Body.String()
	if got := strings.Count(body, `loading="lazy"`); got != 2 {
		t.Errorf("lazy images = %d, want 2 (only cards after the first %d)", got, eagerCardCount)
	}
	if got := strings.Count(body, `fetchpriority="high"`); got != 1 {
		t.Errorf("fetchpriority=high = %d, want 1 (first card)", got)
	}
}

func TestQuickBuyModalIsFocusableDialog(t *testing.T) {
	rr := newTestRenderer(t)
	w := httptest.NewRecorder()
	data := PageData{Lang: i18n.LangRU, Screen: "shop", Data: &QuickBuyData{Name: "Air", InStock: true}}
	if err := rr.RenderPartial(w, "shop", "_quickbuy_modal", data); err != nil {
		t.Fatalf("RenderPartial: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, `role="dialog" aria-modal="true" aria-label="Air" tabindex="-1"`) {
		t.Error("quick-buy dialog must be programmatically focusable")
	}
	if strings.Contains(body, "innerHTML=") || !strings.Contains(body, "cozyCloseQuickbuy()") {
		t.Error("every close path must go through cozyCloseQuickbuy (restores focus)")
	}
}

func TestCategoryTrail(t *testing.T) {
	tree := []*catalog.Category{
		{ID: "m", NameRu: "Мужское", NameKy: "Эркектер", Slug: "men", Children: []*catalog.Category{
			{ID: "mc", NameRu: "Классика", NameKy: "Классика", Slug: "men-classic"},
		}},
		{ID: "w", NameRu: "Женское", Slug: "women"},
	}
	got := categoryTrail(tree, "mc", i18n.LangKY)
	want := []Crumb{{Name: "Эркектер", Path: "/catalog/men"}, {Name: "Классика", Path: "/catalog/men-classic"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("trail = %+v, want %+v", got, want)
	}
	if categoryTrail(tree, "nope", i18n.LangRU) != nil {
		t.Error("unknown category must give no trail")
	}
}

func TestBreadcrumbJSONLDKeepsLanguageVersion(t *testing.T) {
	h := newTestHandlers(t)
	r := httptest.NewRequest(http.MethodGet, "/catalog/men?lang=ky", nil)
	raw := string(h.breadcrumbJSONLD(r, i18n.LangKY, []Crumb{{Name: "Эркектер", Path: "/catalog/men"}}))

	var doc struct {
		Type  string `json:"@type"`
		Items []struct {
			Position int    `json:"position"`
			Name     string `json:"name"`
			Item     string `json:"item"`
		} `json:"itemListElement"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("invalid JSON-LD: %v\n%s", err, raw)
	}
	if doc.Type != "BreadcrumbList" || len(doc.Items) != 2 {
		t.Fatalf("breadcrumb = %s", raw)
	}
	if doc.Items[0].Position != 1 || doc.Items[0].Item != "https://cozy.test/?lang=ky" {
		t.Errorf("home crumb = %+v", doc.Items[0])
	}
	if doc.Items[1].Position != 2 || doc.Items[1].Item != "https://cozy.test/catalog/men?lang=ky" || doc.Items[1].Name != "Эркектер" {
		t.Errorf("category crumb = %+v", doc.Items[1])
	}
}

func TestShopSEOEmitsBreadcrumbOnCategoryPagesOnly(t *testing.T) {
	h := newTestHandlers(t)
	r := httptest.NewRequest(http.MethodGet, "/catalog/men", nil)

	cat := PageData{Lang: i18n.LangRU}
	h.shopSEO(r, &cat, &ShopData{BasePath: "/catalog/men", CategoryName: "Мужское", Page: 1, Trail: []Crumb{{Name: "Мужское", Path: "/catalog/men"}}})
	if !strings.Contains(string(cat.SEO.BreadcrumbJSONLD), `"BreadcrumbList"`) {
		t.Error("category page should carry a BreadcrumbList")
	}

	home := PageData{Lang: i18n.LangRU}
	h.shopSEO(httptest.NewRequest(http.MethodGet, "/", nil), &home, &ShopData{BasePath: "/", Page: 1})
	if home.SEO.BreadcrumbJSONLD != "" {
		t.Error("home page should not carry a BreadcrumbList")
	}

	w := httptest.NewRecorder()
	cat.Screen = "shop"
	cat.Data = &ShopData{BasePath: "/catalog/men", Page: 1, TotalPages: 1}
	if err := h.render.Render(w, "shop", cat); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Count(w.Body.String(), `<script type="application/ld+json">`) != 1 {
		t.Error("layout should render the breadcrumb JSON-LD script")
	}
}
