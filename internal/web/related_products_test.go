package web

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/i18n"
)

// fakeLister returns byCategory for a category query and popular for a
// popular-sorted one, recording the calls.
type fakeLister struct {
	byCategory, popular []catalog.Product
	err                 error
	calls               []catalog.ListFilter
}

func (f *fakeLister) List(_ context.Context, filter catalog.ListFilter) ([]catalog.Product, int, error) {
	f.calls = append(f.calls, filter)
	if f.err != nil {
		return nil, 0, f.err
	}
	if filter.Sort == catalog.SortPopular {
		return f.popular, len(f.popular), nil
	}
	return f.byCategory, len(f.byCategory), nil
}

func products(ids ...string) []catalog.Product {
	out := make([]catalog.Product, len(ids))
	for i, id := range ids {
		out[i] = catalog.Product{ID: id}
	}
	return out
}

func ids(ps []catalog.Product) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.ID
	}
	return out
}

func TestPickRelatedProducts(t *testing.T) {
	cases := []struct {
		name        string
		byCategory  []string
		popular     []string
		want        []string
		wantPopular bool
	}{
		{"full category, no popular call", []string{"self", "a", "b", "c", "d", "e", "f"}, []string{"p1"},
			[]string{"a", "b", "c", "d", "e", "f"}, false},
		{"small category topped up with popular, no duplicates", []string{"self", "a", "b"}, []string{"b", "p1", "self", "p2", "p3", "p4", "p5"},
			[]string{"a", "b", "p1", "p2", "p3", "p4"}, true},
		{"only the product itself", []string{"self"}, nil, []string{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := &fakeLister{byCategory: products(c.byCategory...), popular: products(c.popular...)}
			got, err := pickRelatedProducts(context.Background(), repo, "self", "cat-1")
			if err != nil {
				t.Fatalf("pickRelatedProducts: %v", err)
			}
			if !reflect.DeepEqual(ids(got), c.want) {
				t.Errorf("got %v, want %v", ids(got), c.want)
			}
			if repo.calls[0].CategoryID != "cat-1" || repo.calls[0].PageSize != relatedProductsLimit+1 {
				t.Errorf("first query = %+v, want category cat-1, page size %d", repo.calls[0], relatedProductsLimit+1)
			}
			if gotPopular := len(repo.calls) == 2; gotPopular != c.wantPopular {
				t.Errorf("popular queried = %v, want %v", gotPopular, c.wantPopular)
			}
		})
	}
}

func TestPickRelatedProductsError(t *testing.T) {
	_, err := pickRelatedProducts(context.Background(), &fakeLister{err: errors.New("db down")}, "self", "cat-1")
	if err == nil {
		t.Fatal("want error from a failing repository")
	}
}

func TestRenderProductRelatedAndBrandLink(t *testing.T) {
	rr := newTestRenderer(t)
	pd := &ProductData{
		Name: "Челси", Brand: "Ricci & Co", PriceText: "7 490 сом", ProductPath: "/product/x",
		Related: []ProductCard{{ID: "r1", Name: "Броги", PriceText: "6 190 сом", DetailURL: "/product/r1-brogi"}},
	}
	for lang, title := range map[string]string{i18n.LangRU: "Может понравиться", i18n.LangKY: "Сизге жагышы мүмкүн"} {
		w := httptest.NewRecorder()
		if err := rr.Render(w, "product", PageData{Lang: lang, Screen: "product", Data: pd}); err != nil {
			t.Fatalf("Render %s: %v", lang, err)
		}
		body := w.Body.String()
		if !strings.Contains(body, title) || !strings.Contains(body, `href="/product/r1-brogi"`) {
			t.Errorf("%s: related block missing", lang)
		}
		if !strings.Contains(body, `href="/?q=Ricci%20%26%20Co"`) {
			t.Errorf("%s: brand link must search the escaped brand name", lang)
		}
	}

	pd.Related = nil
	w := httptest.NewRecorder()
	if err := rr.Render(w, "product", PageData{Lang: i18n.LangRU, Screen: "product", Data: pd}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(w.Body.String(), "product-related") {
		t.Error("related block rendered with no products")
	}
}
