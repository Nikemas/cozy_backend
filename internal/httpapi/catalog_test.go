package httpapi

import (
	"errors"
	"net/url"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/catalog"
)

func TestParseListFilterDefaults(t *testing.T) {
	filter, categoryParam, err := parseListFilter(url.Values{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if categoryParam != "" {
		t.Errorf("categoryParam = %q, want empty", categoryParam)
	}
	if filter.Page != 1 {
		t.Errorf("Page = %d, want 1", filter.Page)
	}
	if filter.PageSize != catalog.DefaultPageSize {
		t.Errorf("PageSize = %d, want %d", filter.PageSize, catalog.DefaultPageSize)
	}
	if filter.PriceMin != nil || filter.PriceMax != nil {
		t.Errorf("PriceMin/PriceMax = %v/%v, want nil/nil", filter.PriceMin, filter.PriceMax)
	}
	if filter.Sort != "" {
		t.Errorf("Sort = %q, want empty", filter.Sort)
	}
}

func TestParseListFilterFullySpecified(t *testing.T) {
	q := url.Values{
		"category":  {"sneakers"},
		"size":      {"42"},
		"color":     {"black"},
		"price_min": {"1000"},
		"price_max": {"5000"},
		"q":         {"nike"},
		"sort":      {catalog.SortPriceAsc},
		"page":      {"3"},
	}

	filter, categoryParam, err := parseListFilter(q)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if categoryParam != "sneakers" {
		t.Errorf("categoryParam = %q, want sneakers", categoryParam)
	}
	if filter.Size != "42" || filter.Color != "black" || filter.Query != "nike" {
		t.Errorf("filter = %+v, unexpected size/color/q", filter)
	}
	if filter.PriceMin == nil || *filter.PriceMin != 1000 {
		t.Errorf("PriceMin = %v, want 1000", filter.PriceMin)
	}
	if filter.PriceMax == nil || *filter.PriceMax != 5000 {
		t.Errorf("PriceMax = %v, want 5000", filter.PriceMax)
	}
	if filter.Sort != catalog.SortPriceAsc {
		t.Errorf("Sort = %q, want %q", filter.Sort, catalog.SortPriceAsc)
	}
	if filter.Page != 3 {
		t.Errorf("Page = %d, want 3", filter.Page)
	}
}

func TestParseListFilterInvalidValues(t *testing.T) {
	cases := []struct {
		name string
		q    url.Values
	}{
		{"bad price_min", url.Values{"price_min": {"cheap"}}},
		{"bad price_max", url.Values{"price_max": {"expensive"}}},
		{"bad sort", url.Values{"sort": {"random"}}},
		{"bad page not a number", url.Values{"page": {"abc"}}},
		{"bad page zero", url.Values{"page": {"0"}}},
		{"bad page negative", url.Values{"page": {"-1"}}},
		{"bad in_stock", url.Values{"in_stock": {"maybe"}}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := parseListFilter(c.q)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			var appErr *apperr.AppError
			if !errors.As(err, &appErr) {
				t.Fatalf("expected *apperr.AppError, got %T (%v)", err, err)
			}
			if appErr.Status != 400 {
				t.Errorf("Status = %d, want 400", appErr.Status)
			}
		})
	}
}

func TestParseListFilterAcceptsEverySortValue(t *testing.T) {
	for _, sort := range []string{"", catalog.SortNewest, catalog.SortPriceAsc, catalog.SortPriceDesc, catalog.SortPopular} {
		t.Run("sort="+sort, func(t *testing.T) {
			filter, _, err := parseListFilter(url.Values{"sort": {sort}})
			if err != nil {
				t.Fatalf("parseListFilter: %v", err)
			}
			if filter.Sort != sort {
				t.Errorf("Sort = %q, want %q", filter.Sort, sort)
			}
		})
	}
}

func TestParseListFilterInStock(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"1", true},
		{"true", true},
	}
	for _, c := range cases {
		t.Run(c.value, func(t *testing.T) {
			q := url.Values{}
			if c.value != "" {
				q.Set("in_stock", c.value)
			}
			filter, _, err := parseListFilter(q)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if filter.InStock != c.want {
				t.Errorf("InStock = %v, want %v", filter.InStock, c.want)
			}
		})
	}
}
