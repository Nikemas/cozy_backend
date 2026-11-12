package web

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/i18n"
)

func railTree() []*catalog.Category {
	return []*catalog.Category{
		{ID: "men", NameRu: "Мужское", NameKy: "Эркектер", Slug: "muzhskoe", Children: []*catalog.Category{
			{ID: "men-classic", NameRu: "Классика", NameKy: "Классика", Slug: "muzhskoe-klassika"},
			{ID: "men-sport", NameRu: "Спортивная", NameKy: "Спорттук", Slug: "muzhskoe-sport", Children: []*catalog.Category{
				{ID: "men-run", NameRu: "Беговая", Slug: "muzhskoe-beg"},
			}},
		}},
		{ID: "kids", NameRu: "Детское", NameKy: "Балдар", Slug: "detskoe"},
	}
}

func TestRailSpecsUsesSubcategoriesOrLeafTopLevel(t *testing.T) {
	// Arrange
	tree := railTree()

	// Act
	specs := railSpecs(tree, i18n.LangKY)

	// Assert
	want := []railSpec{
		{Name: "Классика", Parent: "Эркектер", Href: "/catalog/muzhskoe-klassika", CategoryIDs: []string{"men-classic"}},
		{Name: "Спорттук", Parent: "Эркектер", Href: "/catalog/muzhskoe-sport", CategoryIDs: []string{"men-sport", "men-run"}},
		{Name: "Балдар", Href: "/catalog/detskoe", CategoryIDs: []string{"kids"}},
	}
	if !reflect.DeepEqual(specs, want) {
		t.Fatalf("railSpecs = %+v, want %+v", specs, want)
	}
}

func TestRailProductsCapsAtRailSizeAndDropsEmpty(t *testing.T) {
	// Arrange
	specs := railSpecs(railTree(), i18n.LangRU)
	counts := map[string]int{"men-classic": 7, "men-sport": 0, "kids": 2}
	var filters []catalog.ListFilter
	list := func(_ context.Context, f catalog.ListFilter) ([]catalog.Product, int, error) {
		filters = append(filters, f)
		n := counts[f.CategoryIDs[0]]
		out := make([]catalog.Product, n)
		for i := range out {
			out[i] = catalog.Product{ID: fmt.Sprintf("%s-%d", f.CategoryIDs[0], i)}
		}
		return out, n, nil
	}

	// Act
	kept, rows, err := railProducts(context.Background(), specs, list)

	// Assert
	if err != nil {
		t.Fatalf("railProducts: %v", err)
	}
	if len(kept) != 2 || kept[0].Name != "Классика" || kept[1].Name != "Детское" {
		t.Fatalf("kept specs = %+v, want Классика and Детское (empty Спортивная dropped)", kept)
	}
	if len(rows[0]) != railSize || len(rows[1]) != 2 {
		t.Fatalf("row sizes = %d/%d, want %d/2", len(rows[0]), len(rows[1]), railSize)
	}
	for _, f := range filters {
		if f.PageSize != railSize || f.Page != 1 || f.Sort != catalog.SortNewest {
			t.Errorf("filter = %+v, want page 1 of %d newest", f, railSize)
		}
	}
}

func TestRailProductsPropagatesListError(t *testing.T) {
	list := func(context.Context, catalog.ListFilter) ([]catalog.Product, int, error) {
		return nil, 0, errors.New("db down")
	}
	if _, _, err := railProducts(context.Background(), railSpecs(railTree(), i18n.LangRU), list); err == nil {
		t.Fatal("railProducts should return the lister's error")
	}
}

func TestAssembleRailsSplitsCardsInOrder(t *testing.T) {
	specs := []railSpec{{Name: "A", Href: "/catalog/a"}, {Name: "B", Href: "/catalog/b"}}
	rows := [][]catalog.Product{{{ID: "a1"}, {ID: "a2"}}, {{ID: "b1"}}}
	cards := []ProductCard{{ID: "a1"}, {ID: "a2"}, {ID: "b1"}}

	rails := assembleRails(specs, rows, cards)

	if len(rails) != 2 || len(rails[0].Products) != 2 || rails[1].Products[0].ID != "b1" || rails[1].Href != "/catalog/b" {
		t.Fatalf("assembleRails = %+v", rails)
	}
}

func railCards(prefix string, n int) []ProductCard {
	out := make([]ProductCard, n)
	for i := range out {
		id := fmt.Sprintf("%s%d", prefix, i)
		out[i] = ProductCard{ID: id, Name: "Shoe " + id, PriceText: "1 000 сом", DetailURL: "/product/" + id, HasPhoto: true, PhotoURL: "https://m/" + id + ".jpg"}
	}
	return out
}

func TestRenderShopBrowseModeShowsRails(t *testing.T) {
	for _, tc := range []struct {
		lang, seeAll string
	}{
		{i18n.LangRU, "Смотреть все"},
		{i18n.LangKY, "Баарын көрүү"},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			// Arrange
			rr := newTestRenderer(t)
			sd := &ShopData{
				BasePath: "/", Page: 1, TotalPages: 1, Total: 8,
				Products: railCards("g", 8),
				Rails: []CategoryRail{
					{Name: "Классика", Parent: "Мужское", Href: "/catalog/muzhskoe-klassika", Products: railCards("c", railSize)},
					{Name: "Детское", Href: "/catalog/detskoe", Products: railCards("k", 2)},
				},
			}

			// Act
			w := httptest.NewRecorder()
			if err := rr.Render(w, "shop", PageData{Lang: tc.lang, Screen: "shop", Data: sd}); err != nil {
				t.Fatalf("Render: %v", err)
			}
			body := w.Body.String()

			// Assert
			if got := strings.Count(body, `class="shop-rail"`); got != 2 {
				t.Errorf("rails = %d, want 2", got)
			}
			if got := strings.Count(body, `class="shop-rail__item"`); got != railSize+2 {
				t.Errorf("rail items = %d, want %d", got, railSize+2)
			}
			if got := strings.Count(body, tc.seeAll); got != 2 {
				t.Errorf("%q links = %d, want 2", tc.seeAll, got)
			}
			for _, want := range []string{
				`class="shop-rail__all" href="/catalog/muzhskoe-klassika"`,
				`class="shop-rail__all" href="/catalog/detskoe"`,
				"Мужское · </span>Классика",
				`/quickbuy/c0`,
			} {
				if !strings.Contains(body, want) {
					t.Errorf("browse page missing %q", want)
				}
			}
			if strings.Contains(body, `class="shop-grid"`) || strings.Contains(body, "/product/g0") {
				t.Error("browse page with rails should not render the grid")
			}
			if got := strings.Count(body, `fetchpriority="high"`); got != 1 {
				t.Errorf("fetchpriority=high = %d, want 1 (first rail's first card)", got)
			}
		})
	}
}

func TestRenderShopFilteredModeKeepsGrid(t *testing.T) {
	// Arrange
	rr := newTestRenderer(t)
	sd := &ShopData{
		BasePath: "/catalog/detskoe", CategoryName: "Детское", Page: 1, TotalPages: 2, HasNext: true, NextHref: "/catalog/detskoe?page=2",
		Products: railCards("g", 3), Total: 30,
	}

	// Act
	w := httptest.NewRecorder()
	if err := rr.Render(w, "shop", PageData{Lang: i18n.LangRU, Screen: "shop", Data: sd}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := w.Body.String()

	// Assert
	if !strings.Contains(body, `class="shop-grid"`) || strings.Count(body, `class="product-card"`) != 3 {
		t.Error("filtered page should render the 3-card grid")
	}
	if !strings.Contains(body, `class="shop-pager"`) {
		t.Error("filtered page should keep the pager")
	}
	if strings.Contains(body, "shop-rail") || strings.Contains(body, "Смотреть все") {
		t.Error("filtered page should not render rails")
	}
}
