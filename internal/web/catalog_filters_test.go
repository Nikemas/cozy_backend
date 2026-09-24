package web

import (
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/i18n"
)

func TestParseShopParams(t *testing.T) {
	q, _ := url.ParseQuery("q=+nike+&size=42&color=Чёрный&in_stock=1&sort=price_desc&price_min=5000&price_max=2000&page=3")
	p := parseShopParams(q)
	want := shopParams{Query: "nike", Size: "42", Color: "Чёрный", InStock: true, Sort: catalog.SortPriceDesc, PriceMin: 2000, PriceMax: 5000, Page: 3}
	if p != want {
		t.Fatalf("parseShopParams = %+v, want %+v (min/max swapped when reversed)", p, want)
	}

	bad, _ := url.ParseQuery("sort=DROP&price_min=-5&price_max=abc&page=0&in_stock=maybe")
	p = parseShopParams(bad)
	if p.Sort != "" || p.PriceMin != 0 || p.PriceMax != 0 || p.Page != 1 || p.InStock {
		t.Fatalf("invalid params should be ignored, got %+v", p)
	}

	long := parseShopParams(url.Values{"q": {strings.Repeat("я", 500)}})
	if n := len([]rune(long.Query)); n != maxQueryLen {
		t.Fatalf("query length = %d, want capped at %d", n, maxQueryLen)
	}
}

func TestShopParamsHrefRoundTrips(t *testing.T) {
	p := shopParams{Query: "air", Size: "42", InStock: true, Sort: catalog.SortPriceAsc, PriceMax: 6000, Page: 1}
	href := p.href("/catalog/krossovki", 2)
	u, err := url.Parse(href)
	if err != nil || u.Path != "/catalog/krossovki" {
		t.Fatalf("href = %q", href)
	}
	back := parseShopParams(u.Query())
	p.Page = 2
	if back != p {
		t.Fatalf("round trip = %+v, want %+v", back, p)
	}
	if got := (shopParams{Page: 1}).href("/", 1); got != "/" {
		t.Fatalf("no filters, page 1 = %q, want /", got)
	}
}

func TestShopParamsListFilter(t *testing.T) {
	p := shopParams{Size: "42", Color: "Белый", InStock: true, PriceMin: 1000, Page: 2}
	f := p.listFilter([]string{"c1", "c2"}, 20)
	if f.Sort != catalog.SortNewest || !f.InStock || f.Size != "42" || f.Color != "Белый" || f.Page != 2 || f.PageSize != 20 {
		t.Fatalf("listFilter = %+v", f)
	}
	if f.PriceMin == nil || *f.PriceMin != 1000 || f.PriceMax != nil {
		t.Fatalf("price bounds = %v/%v", f.PriceMin, f.PriceMax)
	}
	if !reflect.DeepEqual(f.CategoryIDs, []string{"c1", "c2"}) {
		t.Fatalf("CategoryIDs = %v", f.CategoryIDs)
	}
}

func TestFindCategoryIncludesDescendants(t *testing.T) {
	child := &catalog.Category{ID: "c2", Slug: "begovye"}
	tree := []*catalog.Category{
		{ID: "c1", Slug: "krossovki", Children: []*catalog.Category{child}},
		{ID: "c3", Slug: "botinki"},
	}
	cat, ids := findCategory(tree, "krossovki")
	if cat == nil || !reflect.DeepEqual(ids, []string{"c1", "c2"}) {
		t.Fatalf("findCategory(krossovki) = %v %v", cat, ids)
	}
	cat, ids = findCategory(tree, "begovye")
	if cat != child || !reflect.DeepEqual(ids, []string{"c2"}) {
		t.Fatalf("findCategory(begovye) = %v %v", cat, ids)
	}
	if cat, _ := findCategory(tree, "nope"); cat != nil {
		t.Fatal("unknown slug should not match")
	}
}

func TestSortedSizesNumericFirst(t *testing.T) {
	got := sortedSizes(map[string]bool{"42": true, "36,5": true, "M": true, "39": true, "L": true, "": true})
	want := []string{"36,5", "39", "42", "L", "M"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sortedSizes = %v, want %v", got, want)
	}
}

func TestFilterOptionsKeepsStaleSelection(t *testing.T) {
	got := filterOptions([]string{"41", "42"}, "45")
	if len(got) != 3 || !got[2].Selected || got[2].Value != "45" {
		t.Fatalf("filterOptions = %+v, want the stale selection appended and selected", got)
	}
}

func TestRenderShopWithFilters(t *testing.T) {
	rr := newTestRenderer(t)
	sd := &ShopData{
		BasePath:     "/catalog/krossovki",
		CategoryName: "Кроссовки",
		Size:         "42",
		InStock:      true,
		SizeOptions:  []FilterOption{{Value: "41", Label: "41"}, {Value: "42", Label: "42", Selected: true}},
		ColorOptions: []FilterOption{{Value: "Белый", Label: "Белый"}},
		SortOptions:  []FilterOption{{Value: "", Label: "Сначала новые"}, {Value: "price_asc", Label: "Сначала дешевле", Selected: true}},
		HasFilters:   true,
		Page:         1,
		TotalPages:   1,
		Categories:   []CategoryChip{{Label: "Все", Href: "/"}, {Label: "Кроссовки", Href: "/catalog/krossovki", Active: true}},
		Products:     []ProductCard{{ID: "p1", Name: "Air", PriceText: "4 500 сом", PriceFrom: true, DetailURL: "/product/p1-air", HasPhoto: true, PhotoURL: "https://m/p1/thumb.jpg"}},
		Total:        1,
	}
	w := httptest.NewRecorder()
	if err := rr.Render(w, "shop", PageData{Lang: i18n.LangRU, Screen: "shop", Data: sd}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := w.Body.String()
	for _, want := range []string{
		`id="shop-filters"`,
		`<h1 class="shop-title">Кроссовки</h1>`,
		`name="size" value="42" checked`,
		`name="in_stock" value="1" checked`,
		`<option value="price_asc" selected>`,
		"от 4 500 сом",
		`loading="lazy"`,
		`href="/catalog/krossovki">Сбросить фильтры`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("shop page missing %q", want)
		}
	}
	for _, fake := range []string{"Nike", "Adidas", "Sneakers"} {
		if strings.Contains(body, fake) {
			t.Errorf("shop page still contains fake filter %q", fake)
		}
	}
}
