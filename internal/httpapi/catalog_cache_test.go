package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/config"
)

func TestPublicCatalogRoutesSendCacheControlOnlyOnSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mux := http.NewServeMux()
	RegisterCatalogRoutes(mux, db, &config.Config{})

	mock.ExpectQuery("FROM categories").WillReturnRows(
		sqlmock.NewRows([]string{"id", "parent_id", "name_ru", "name_ky", "slug", "sort_order"}).
			AddRow("c1", nil, "Обувь", "Бут кийим", "shoes", 0),
	)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/categories", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); !strings.HasPrefix(got, "public, max-age=60") {
		t.Fatalf("Cache-Control = %q, want public max-age=60", got)
	}

	mock.ExpectQuery("FROM categories").WillReturnError(errors.New("db down"))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/categories", nil))
	if rec.Code != http.StatusInternalServerError || rec.Header().Get("Cache-Control") != "" {
		t.Fatalf("status=%d Cache-Control=%q, want an uncached 500", rec.Code, rec.Header().Get("Cache-Control"))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
