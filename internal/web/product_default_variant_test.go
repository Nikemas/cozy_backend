package web

import (
	"context"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/storefront"
)

func newProductPageHandlers(t *testing.T, stockRows [][2]any) *handlers {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now()
	mock.ExpectQuery(regexp.QuoteMeta("FROM products")).WithArgs("p1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "category_id", "name_ru", "name_ky", "description_ru", "description_ky", "brand", "base_price", "is_active", "created_at", "updated_at"}).
			AddRow("p1", "c1", "Сандалии", "Сандалии", nil, nil, nil, 1000, true, now, now))
	mock.ExpectQuery(regexp.QuoteMeta("FROM product_variants")).WithArgs("p1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "product_id", "size", "color", "sku", "price_override"}).
			AddRow("v1", "p1", "40", "Синий", "S1", nil).
			AddRow("v2", "p1", "40", "Чёрный", "S2", nil).
			AddRow("v3", "p1", "41", "Синий", "S3", nil))
	stock := sqlmock.NewRows([]string{"variant_id", "point_id", "quantity", "updated_at"})
	for _, s := range stockRows {
		stock.AddRow(s[0], "pt1", s[1], now)
	}
	mock.ExpectQuery(regexp.QuoteMeta("FROM stock")).WillReturnRows(stock)
	mock.ExpectQuery(regexp.QuoteMeta("FROM product_images")).WithArgs("p1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "product_id", "object_key", "sort_order", "color"}))
	mock.ExpectQuery(regexp.QuoteMeta("FROM points_of_sale")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "city", "address", "working_hours", "latitude", "longitude"}))
	rr := newTestRenderer(t)
	return &handlers{
		bundle:     rr.bundle,
		products:   catalog.NewProductRepo(db),
		variants:   catalog.NewVariantRepo(db),
		stock:      catalog.NewStockRepo(db),
		images:     catalog.NewImageRepo(db),
		branchRepo: storefront.NewBranchRepo(db),
	}
}

func TestBuildProductDataDefaultsToFirstInStockVariant(t *testing.T) {
	tests := []struct {
		name                string
		stock               [][2]any
		query               url.Values
		wantSize, wantColor string
		wantVariant         string
	}{
		{"skips out-of-stock leading variants", [][2]any{{"v2", 3}}, url.Values{}, "40", "Чёрный", "v2"},
		{"later size in stock", [][2]any{{"v3", 1}}, url.Values{}, "41", "Синий", "v3"},
		{"nothing in stock falls back to first", nil, url.Values{}, "40", "Синий", "v1"},
		{"explicit query wins", [][2]any{{"v2", 3}}, url.Values{"size": {"41"}, "color": {"Синий"}}, "41", "Синий", "v3"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newProductPageHandlers(t, tc.stock)
			pd, err := h.buildProductData(context.Background(), tc.query, "ru", "p1")
			if err != nil {
				t.Fatalf("buildProductData: %v", err)
			}
			if pd.SelectedSize != tc.wantSize || pd.SelectedColor != tc.wantColor || pd.SelectedVariantID != tc.wantVariant {
				t.Fatalf("got %q/%q/%q, want %q/%q/%q", pd.SelectedSize, pd.SelectedColor, pd.SelectedVariantID,
					tc.wantSize, tc.wantColor, tc.wantVariant)
			}
		})
	}
}
