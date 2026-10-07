package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/config"
)

const (
	listTestCategoryID = "11111111-1111-1111-1111-111111111111"
	listTestProductA   = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	listTestProductB   = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
)

var productListColumns = []string{
	"id", "category_id", "name_ru", "name_ky", "description_ru", "description_ky",
	"brand", "base_price", "is_active", "created_at", "updated_at",
}

func newProductListMux(t *testing.T) (*http.ServeMux, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.ValueConverterOption(stringSliceConverter{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mux := http.NewServeMux()
	RegisterCatalogRoutes(mux, db, &config.Config{MinIOEndpoint: "cdn.test", MinIOBucket: "media"})
	return mux, mock
}

func serveProductList(mux *http.ServeMux, query string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/products"+query, nil))
	return rec
}

func TestProductListResolvesCategorySlugAndAttachesPrimaryPhoto(t *testing.T) {
	mux, mock := newProductListMux(t)
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`SELECT id FROM categories WHERE slug = \$1`).WithArgs("sneakers").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(listTestCategoryID))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM products`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(`SELECT id, category_id, name_ru`).
		WillReturnRows(sqlmock.NewRows(productListColumns).
			AddRow(listTestProductA, listTestCategoryID, "Кеды", "Кеды", "", "", "Nike", 4500.0, true, now, now).
			AddRow(listTestProductB, listTestCategoryID, "Туфли", "Туфли", "", "", "Ecco", 7000.0, true, now, now))
	mock.ExpectQuery(`FROM product_images\s+WHERE product_id = ANY\(\$1\)`).
		WithArgs([]string{listTestProductA, listTestProductB}).
		WillReturnRows(sqlmock.NewRows([]string{"id", "product_id", "object_key", "sort_order", "color"}).
			AddRow("img-1", listTestProductA, "products/x/full.jpg", 0, nil))

	rec := serveProductList(mux, "?category=sneakers&page=1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []struct {
			ID       string `json:"id"`
			PhotoURL string `json:"photo_url"`
			ThumbURL string `json:"thumb_url"`
		} `json:"items"`
		Page  int `json:"page"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 2 || body.Page != 1 || len(body.Items) != 2 {
		t.Fatalf("body = %+v, want 2 items on page 1 of total 2", body)
	}
	if got := body.Items[0].PhotoURL; got != "http://cdn.test/media/products/x/full.jpg" {
		t.Errorf("photo_url = %q", got)
	}
	if got := body.Items[0].ThumbURL; got != "http://cdn.test/media/products/x/thumb.jpg" {
		t.Errorf("thumb_url = %q", got)
	}
	if body.Items[1].PhotoURL != "" || body.Items[1].ThumbURL != "" {
		t.Errorf("product without photo got urls %+v", body.Items[1])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestProductListErrors(t *testing.T) {
	dbErr := errors.New("db down")
	tests := []struct {
		name   string
		query  string
		setup  func(sqlmock.Sqlmock)
		status int
	}{
		{
			name:   "invalid filter is a bad request",
			query:  "?price_min=oops",
			setup:  func(sqlmock.Sqlmock) {},
			status: http.StatusBadRequest,
		},
		{
			name:  "unknown category slug is not found",
			query: "?category=nope",
			setup: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(`SELECT id FROM categories WHERE slug`).WithArgs("nope").
					WillReturnRows(sqlmock.NewRows([]string{"id"}))
			},
			status: http.StatusNotFound,
		},
		{
			name:  "count query failure",
			query: "",
			setup: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(`SELECT COUNT\(\*\) FROM products`).WillReturnError(dbErr)
			},
			status: http.StatusInternalServerError,
		},
		{
			name:  "primary images failure",
			query: "?category=" + listTestCategoryID,
			setup: func(m sqlmock.Sqlmock) {
				now := time.Now()
				m.ExpectQuery(`SELECT COUNT\(\*\) FROM products`).
					WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
				m.ExpectQuery(`SELECT id, category_id, name_ru`).
					WillReturnRows(sqlmock.NewRows(productListColumns).
						AddRow(listTestProductA, listTestCategoryID, "Кеды", "Кеды", "", "", "Nike", 4500.0, true, now, now))
				m.ExpectQuery(`FROM product_images`).WillReturnError(dbErr)
			},
			status: http.StatusInternalServerError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux, mock := newProductListMux(t)
			tt.setup(mock)

			rec := serveProductList(mux, tt.query)

			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.status, rec.Body.String())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestProductFacetsUnknownCategoryIsNotFound(t *testing.T) {
	mux, mock := newFacetsMux(t)
	mock.ExpectQuery(`SELECT id FROM categories WHERE slug`).WithArgs("nope").
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/products/facets?category=nope", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}
