package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/config"
)

// TestGetCategoriesExposesAbsoluteImageURL: every node of the tree,
// children included, carries image_url — the public MinIO URL built from
// MINIO_PUBLIC_ENDPOINT like product photo URLs — or null without a photo;
// the raw object key never leaves the server.
func TestGetCategoriesExposesAbsoluteImageURL(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	cfg := &config.Config{MinIOEndpoint: "minio:9000", MinIOPublicEndpoint: "media.cozy.kg", MinIOPublicUseSSL: true, MinIOBucket: "cozy-media"}
	mux := http.NewServeMux()
	RegisterCatalogRoutes(mux, db, cfg)

	mock.ExpectQuery("SELECT (.+) image_key FROM categories").WillReturnRows(
		sqlmock.NewRows([]string{"id", "parent_id", "name_ru", "name_ky", "slug", "sort_order", "image_key"}).
			AddRow("c1", nil, "Обувь", "Бут кийим", "shoes", 0, nil).
			AddRow("c2", "c1", "Кеды", "Кеддер", "kedy", 1, "categories/k.jpg"),
	)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/categories", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	var tree []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &tree); err != nil {
		t.Fatal(err)
	}
	root := tree[0]
	if v, ok := root["image_url"]; !ok || v != nil {
		t.Errorf("root image_url = %v (present %v), want null", v, ok)
	}
	if _, leaked := root["image_key"]; leaked {
		t.Error("image_key leaked into the response")
	}
	child := root["children"].([]any)[0].(map[string]any)
	if got := child["image_url"]; got != "https://media.cozy.kg/cozy-media/categories/k.jpg" {
		t.Errorf("child image_url = %v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
