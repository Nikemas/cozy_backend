//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/httpapi"
)

// TestCategoryImageLifecycle: SetImage stores/clears categories.image_key
// (migration 000040) and returns the replaced key, Update (which knows
// nothing about photos) keeps it, and GET /api/v1/categories exposes it
// as an absolute image_url — null without a photo.
func TestCategoryImageLifecycle(t *testing.T) {
	ctx := ctxT(t)
	repo := catalog.NewCategoryRepo(testDB)
	tag := uuid.NewString()[:8]

	parent, err := repo.Create(ctx, catalog.CategoryInput{NameRu: "Фото " + tag, NameKy: "Сүрөт " + tag, Slug: "it-photo-" + tag})
	if err != nil {
		t.Fatal(err)
	}
	child, err := repo.Create(ctx, catalog.CategoryInput{ParentID: &parent.ID, NameRu: "Дочь " + tag, NameKy: "Кыз " + tag, Slug: "it-photo-child-" + tag})
	if err != nil {
		t.Fatal(err)
	}
	if parent.ImageKey != nil {
		t.Fatalf("new category has a photo: %q", *parent.ImageKey)
	}

	first, second := "categories/"+tag+"-1.jpg", "categories/"+tag+"-2.png"
	prev, err := repo.SetImage(ctx, child.ID, &first)
	if err != nil || prev != nil {
		t.Fatalf("first SetImage: prev=%v err=%v", prev, err)
	}
	prev, err = repo.SetImage(ctx, child.ID, &second)
	if err != nil || prev == nil || *prev != first {
		t.Fatalf("replace: prev=%v err=%v", prev, err)
	}

	updated, err := repo.Update(ctx, child.ID, catalog.CategoryInput{ParentID: &parent.ID, NameRu: "Дочь2 " + tag, NameKy: "Кыз " + tag, Slug: "it-photo-child-" + tag})
	if err != nil || updated.ImageKey == nil || *updated.ImageKey != second {
		t.Fatalf("update dropped the photo: %+v err=%v", updated, err)
	}

	cfg := &config.Config{MinIOEndpoint: "minio:9000", MinIOPublicEndpoint: "media.cozy.kg", MinIOPublicUseSSL: true, MinIOBucket: "cozy-media"}
	mux := http.NewServeMux()
	httpapi.RegisterCatalogRoutes(mux, testDB, cfg)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/categories", nil).WithContext(ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var tree []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &tree); err != nil {
		t.Fatal(err)
	}
	var gotParent map[string]any
	for _, c := range tree {
		if c["id"] == parent.ID {
			gotParent = c
		}
	}
	if gotParent == nil {
		t.Fatal("parent category missing from the tree")
	}
	if v, ok := gotParent["image_url"]; !ok || v != nil {
		t.Errorf("parent image_url = %v (present %v), want null", v, ok)
	}
	gotChild := gotParent["children"].([]any)[0].(map[string]any)
	if want := "https://media.cozy.kg/cozy-media/" + second; gotChild["image_url"] != want {
		t.Errorf("child image_url = %v, want %s", gotChild["image_url"], want)
	}

	prev, err = repo.SetImage(ctx, child.ID, nil)
	if err != nil || prev == nil || *prev != second {
		t.Fatalf("clear: prev=%v err=%v", prev, err)
	}
	var stored *string
	if err := testDB.QueryRowContext(ctx, `SELECT image_key FROM categories WHERE id = $1`, child.ID).Scan(&stored); err != nil || stored != nil {
		t.Fatalf("after clear image_key = %v err=%v", stored, err)
	}

	_, err = repo.SetImage(ctx, uuid.NewString(), &first)
	var ae *apperr.AppError
	if !errors.As(err, &ae) || ae.Status != http.StatusNotFound {
		t.Errorf("unknown id: err = %v, want 404", err)
	}
}
