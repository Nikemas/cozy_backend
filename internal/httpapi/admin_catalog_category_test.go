package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/catalog"
)

// TestAdminUpdateCategoryResponseCarriesImageURL: the JSON admin API
// returns the same Category shape as the public tree, so image_url is
// filled there too — and a PUT (which knows nothing about photos) keeps
// the stored photo instead of clearing it.
func TestAdminUpdateCategoryResponseCarriesImageURL(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(`UPDATE categories\s+SET parent_id = \$2, name_ru = \$3, name_ky = \$4, slug = \$5, sort_order = \$6\s+WHERE`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "parent_id", "name_ru", "name_ky", "slug", "sort_order", "image_key"}).
			AddRow("c1", nil, "Кеды", "Кеддер", "kedy", 2, "categories/k.jpg"))

	objectURL := func(key string) string { return "https://media.example/cozy-media/" + key }
	h := updateCategoryHandler(catalog.NewCategoryRepo(db), nil, objectURL)
	r := httptest.NewRequest(http.MethodPut, "/admin/api/categories/c1",
		strings.NewReader(`{"name_ru":"Кеды","name_ky":"Кеддер","slug":"kedy","sort_order":2}`))
	r.SetPathValue("id", "c1")
	w := httptest.NewRecorder()

	if err := h(w, r); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["image_url"] != "https://media.example/cozy-media/categories/k.jpg" {
		t.Errorf("image_url = %v", got["image_url"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
