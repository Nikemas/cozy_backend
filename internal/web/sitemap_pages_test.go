package web

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/catalog"
)

func fakeProductPages(total int) (func(ctx context.Context, page, size int) ([]catalog.Product, int, error), *int) {
	calls := 0
	return func(_ context.Context, page, size int) ([]catalog.Product, int, error) {
		calls++
		start := (page - 1) * size
		var out []catalog.Product
		for i := start; i < total && i < start+size; i++ {
			out = append(out, catalog.Product{ID: fmt.Sprintf("p%d", i)})
		}
		return out, total, nil
	}, &calls
}

func TestAllSitemapProductsCollectsEveryPage(t *testing.T) {
	cases := []struct {
		name      string
		total     int
		wantCalls int
	}{
		{"empty catalog", 0, 1},
		{"one partial page", 3, 1},
		{"exact page boundary", 2 * sitemapPageSize, 2},
		{"beyond the old 5000 cap", 2*sitemapPageSize + 7, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			list, calls := fakeProductPages(c.total)

			got, err := allSitemapProducts(context.Background(), list)

			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if len(got) != c.total {
				t.Errorf("got %d products, want %d", len(got), c.total)
			}
			if *calls != c.wantCalls {
				t.Errorf("list called %d times, want %d", *calls, c.wantCalls)
			}
		})
	}
}

func TestAllSitemapProductsPropagatesErrors(t *testing.T) {
	boom := errors.New("db down")
	list := func(context.Context, int, int) ([]catalog.Product, int, error) { return nil, 0, boom }

	if _, err := allSitemapProducts(context.Background(), list); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped %v", err, boom)
	}
}
