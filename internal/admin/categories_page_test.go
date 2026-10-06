package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/audit"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// fakeCategoryRepo records the category writes of one request.
type fakeCategoryRepo struct {
	tree      []*catalog.Category
	createErr error
	updateErr error
	imageErr  error

	created  *catalog.CategoryInput
	updated  *catalog.CategoryInput
	images   map[string]*string // id -> stored key
	setCalls int
}

func (f *fakeCategoryRepo) Tree(context.Context) ([]*catalog.Category, error) { return f.tree, nil }

func (f *fakeCategoryRepo) Create(_ context.Context, in catalog.CategoryInput) (*catalog.Category, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.created = &in
	return &catalog.Category{ID: "new-id", NameRu: in.NameRu, Slug: in.Slug}, nil
}

func (f *fakeCategoryRepo) Update(_ context.Context, id string, in catalog.CategoryInput) (*catalog.Category, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	f.updated = &in
	return &catalog.Category{ID: id, NameRu: in.NameRu, Slug: in.Slug, ImageKey: f.images[id]}, nil
}

func (f *fakeCategoryRepo) Delete(context.Context, string) error { return nil }

func (f *fakeCategoryRepo) SetImage(_ context.Context, id string, key *string) (*string, error) {
	f.setCalls++
	if f.imageErr != nil {
		return nil, f.imageErr
	}
	if f.images == nil {
		f.images = map[string]*string{}
	}
	prev := f.images[id]
	f.images[id] = key
	return prev, nil
}

type fakeCategoryImages struct {
	stored  int
	removed []string
	err     error
}

func (f *fakeCategoryImages) StoreCategoryImage(context.Context, []byte) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.stored++
	return "categories/new.jpg", nil
}

func (f *fakeCategoryImages) RemoveObject(_ context.Context, key string) error {
	f.removed = append(f.removed, key)
	return nil
}

func newCategoryHandlers(t *testing.T, repo *fakeCategoryRepo, images *fakeCategoryImages) *handlers {
	t.Helper()
	return &handlers{
		render:         newTestRenderer(t),
		categories:     repo,
		categoryImages: images,
		cfg:            &config.Config{MinIOEndpoint: "media.example", MinIOBucket: "cozy-media"},
	}
}

func postCategory(t *testing.T, handler http.HandlerFunc, target, id string, fields url.Values, files map[string][]byte) *httptest.ResponseRecorder {
	t.Helper()
	body, ct := bannerMultipart(t, fields, files)
	r := httptest.NewRequest(http.MethodPost, target, body)
	r.Header.Set("Content-Type", ct)
	if id != "" {
		r.SetPathValue("id", id)
	}
	r = r.WithContext(staff.NewContextWithStaff(r.Context(), ownerStaff()))
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}

func validCategoryFields() url.Values {
	return url.Values{"name_ru": {"Кеды"}, "name_ky": {"Кеддер"}, "slug": {"kedy"}, "sort_order": {"3"}}
}

func TestCategoriesPageShowsThumbnailsAndPhotoFields(t *testing.T) {
	repo := &fakeCategoryRepo{tree: []*catalog.Category{
		{ID: "c1", NameRu: "Кроссовки", NameKy: "Кроссовкалар", Slug: "krossovki", ImageKey: strPtr("categories/k.jpg"),
			Children: []*catalog.Category{{ID: "c2", ParentID: strPtr("c1"), NameRu: "Мужские", NameKy: "Эркектер", Slug: "m"}}},
	}}
	h := newCategoryHandlers(t, repo, &fakeCategoryImages{})

	w := httptest.NewRecorder()
	h.categoriesPage(w, requestAs(http.MethodGet, "/admin/categories", ownerStaff(), nil))
	body := w.Body.String()

	for _, want := range []string{
		`src="http://media.example/cozy-media/categories/k.jpg"`,
		`enctype="multipart/form-data"`,
		`name="image"`,
		`accept="image/jpeg,image/png,image/webp"`,
		`name="remove_image" value="1"`,
		"imageUrl: &#34;http://media.example/cozy-media/categories/k.jpg&#34;",
		"admin-thumb--empty",
		"Фото категории",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %s", want)
		}
	}
}

func TestCategoriesCreateUploadsPhotoAndStoresKey(t *testing.T) {
	repo := &fakeCategoryRepo{}
	images := &fakeCategoryImages{}
	h := newCategoryHandlers(t, repo, images)

	w := postCategory(t, h.categoriesCreate, "/admin/categories", "", validCategoryFields(), map[string][]byte{"image": []byte("jpg")})

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if repo.created == nil || repo.created.NameRu != "Кеды" || repo.created.SortOrder != 3 {
		t.Fatalf("created = %+v", repo.created)
	}
	if deref(repo.images["new-id"]) != "categories/new.jpg" || images.stored != 1 || len(images.removed) != 0 {
		t.Errorf("image = %q stored=%d removed=%v", deref(repo.images["new-id"]), images.stored, images.removed)
	}
}

func TestCategoriesCreateWithoutPhotoDoesNotTouchImage(t *testing.T) {
	repo := &fakeCategoryRepo{}
	images := &fakeCategoryImages{}
	h := newCategoryHandlers(t, repo, images)

	w := postCategory(t, h.categoriesCreate, "/admin/categories", "", validCategoryFields(), nil)

	if w.Code != http.StatusSeeOther || repo.created == nil || repo.setCalls != 0 || images.stored != 0 {
		t.Errorf("status %d created %v setCalls %d stored %d", w.Code, repo.created, repo.setCalls, images.stored)
	}
}

func TestCategoriesCreateValidationErrorUploadsNothing(t *testing.T) {
	repo := &fakeCategoryRepo{}
	images := &fakeCategoryImages{}
	fields := validCategoryFields()
	fields.Set("name_ru", " ")

	w := postCategory(t, newCategoryHandlers(t, repo, images).categoriesCreate, "/admin/categories", "", fields, map[string][]byte{"image": []byte("jpg")})

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "admin-form-error") {
		t.Fatalf("status %d", w.Code)
	}
	if repo.created != nil || images.stored != 0 {
		t.Errorf("created %v stored %d, want nothing", repo.created, images.stored)
	}
}

func TestCategoriesCreateRemovesUploadWhenSaveFails(t *testing.T) {
	repo := &fakeCategoryRepo{createErr: apperr.Conflict("category_slug_taken", "категория с таким slug уже существует")}
	images := &fakeCategoryImages{}

	w := postCategory(t, newCategoryHandlers(t, repo, images).categoriesCreate, "/admin/categories", "", validCategoryFields(), map[string][]byte{"image": []byte("jpg")})

	if !strings.Contains(w.Body.String(), "категория с таким slug уже существует") {
		t.Errorf("error not shown: %d", w.Code)
	}
	if len(images.removed) != 1 || images.removed[0] != "categories/new.jpg" {
		t.Errorf("removed = %v, want the fresh upload", images.removed)
	}
}

func TestCategoriesCreateShowsImageError(t *testing.T) {
	repo := &fakeCategoryRepo{}
	images := &fakeCategoryImages{err: apperr.BadRequest("unsupported_image", "файл не является изображением JPEG, PNG или WEBP")}

	w := postCategory(t, newCategoryHandlers(t, repo, images).categoriesCreate, "/admin/categories", "", validCategoryFields(), map[string][]byte{"image": []byte("gif")})

	if !strings.Contains(w.Body.String(), "файл не является изображением JPEG, PNG или WEBP") || repo.created != nil {
		t.Errorf("status %d created %v", w.Code, repo.created)
	}
}

func TestCategoriesCreateRejectsOversizedPhoto(t *testing.T) {
	repo := &fakeCategoryRepo{}
	images := &fakeCategoryImages{}
	big := make([]byte, categoryFormMaxBytes+1)

	w := postCategory(t, newCategoryHandlers(t, repo, images).categoriesCreate, "/admin/categories", "", validCategoryFields(), map[string][]byte{"image": big})

	if !strings.Contains(w.Body.String(), "файл больше 10 МБ") || repo.created != nil || images.stored != 0 {
		t.Errorf("status %d created %v stored %d", w.Code, repo.created, images.stored)
	}
}

func TestCategoriesUpdateReplacesPhotoKeepingOldObject(t *testing.T) {
	repo := &fakeCategoryRepo{images: map[string]*string{"c1": strPtr("categories/old.jpg")}}
	images := &fakeCategoryImages{}

	w := postCategory(t, newCategoryHandlers(t, repo, images).categoriesUpdate, "/admin/categories/c1", "c1", validCategoryFields(), map[string][]byte{"image": []byte("jpg")})

	if w.Code != http.StatusSeeOther || repo.updated == nil {
		t.Fatalf("status %d updated %v", w.Code, repo.updated)
	}
	if deref(repo.images["c1"]) != "categories/new.jpg" {
		t.Errorf("image = %q", deref(repo.images["c1"]))
	}
	if len(images.removed) != 0 {
		t.Errorf("replaced photo removed from the bucket: %v", images.removed)
	}
}

func TestCategoriesUpdateKeepsOrRemovesPhoto(t *testing.T) {
	keep := &fakeCategoryRepo{images: map[string]*string{"c1": strPtr("categories/old.jpg")}}
	postCategory(t, newCategoryHandlers(t, keep, &fakeCategoryImages{}).categoriesUpdate, "/admin/categories/c1", "c1", validCategoryFields(), nil)
	if keep.setCalls != 0 || deref(keep.images["c1"]) != "categories/old.jpg" {
		t.Errorf("kept: setCalls %d image %q", keep.setCalls, deref(keep.images["c1"]))
	}

	remove := &fakeCategoryRepo{images: map[string]*string{"c1": strPtr("categories/old.jpg")}}
	fields := validCategoryFields()
	fields.Set("remove_image", "1")
	w := postCategory(t, newCategoryHandlers(t, remove, &fakeCategoryImages{}).categoriesUpdate, "/admin/categories/c1", "c1", fields, nil)
	if w.Code != http.StatusSeeOther || remove.images["c1"] != nil {
		t.Errorf("after remove: status %d image %v", w.Code, remove.images["c1"])
	}
}

func TestCategoriesUpdateRemovesUploadWhenSaveFails(t *testing.T) {
	for _, c := range []struct {
		name string
		repo *fakeCategoryRepo
	}{
		{"update fails", &fakeCategoryRepo{updateErr: apperr.NotFound("category_not_found", "категория не найдена")}},
		{"set image fails", &fakeCategoryRepo{imageErr: errors.New("db down")}},
	} {
		t.Run(c.name, func(t *testing.T) {
			images := &fakeCategoryImages{}
			w := postCategory(t, newCategoryHandlers(t, c.repo, images).categoriesUpdate, "/admin/categories/c1", "c1", validCategoryFields(), map[string][]byte{"image": []byte("jpg")})

			if w.Code == http.StatusSeeOther || strings.Contains(w.Body.String(), "db down") {
				t.Fatalf("status %d", w.Code)
			}
			if len(images.removed) != 1 || images.removed[0] != "categories/new.jpg" {
				t.Errorf("removed = %v", images.removed)
			}
		})
	}
}

func TestCategoryImageAuditDetail(t *testing.T) {
	base := categoryAuditEntry(audit.ActionCategoryUpdate, "c1", "Кеды", &catalog.CategoryInput{NameRu: "Кеды"})

	changed := withCategoryImageChange(base, strPtr("categories/old.jpg"), strPtr("categories/new.jpg"))
	if got, ok := changed.Details["image"].(audit.Change); !ok || got.From != "categories/old.jpg" || got.To != "categories/new.jpg" {
		t.Errorf("image detail = %#v", changed.Details["image"])
	}
	if _, ok := base.Details["image"]; ok {
		t.Error("base entry was mutated")
	}

	same := withCategoryImageChange(base, strPtr("categories/a.jpg"), strPtr("categories/a.jpg"))
	if _, ok := same.Details["image"]; ok {
		t.Error("unchanged photo journaled")
	}

	deleted := withCategoryImageChange(categoryAuditEntry(audit.ActionCategoryDelete, "c1", "Кеды", nil), nil, strPtr("categories/a.jpg"))
	if deleted.Details["image"] == nil {
		t.Error("detail missing on an entry without details")
	}
}
