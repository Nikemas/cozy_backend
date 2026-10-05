package admin

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/audit"
	"github.com/Nikemas/cozy_backend/internal/banner"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/csrf"
	"github.com/Nikemas/cozy_backend/internal/media"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

type fakeBannerStore struct {
	current   *banner.Banner
	getErr    error
	updateErr error
	updated   *banner.Input
}

func (f *fakeBannerStore) Get(context.Context) (*banner.Banner, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.current, nil
}

func (f *fakeBannerStore) Update(_ context.Context, in banner.Input) (*banner.Banner, error) {
	in = in.Normalized()
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	f.updated = &in
	return &banner.Banner{Enabled: in.Enabled, TitleRU: in.TitleRU, BgColor: in.BgColor, TextColor: in.TextColor,
		ImageKey: in.ImageKey, BgImageKey: in.BgImageKey}, nil
}

type fakeBannerImages struct {
	stored  []media.BannerImageKind
	removed []string
	err     error
}

func (f *fakeBannerImages) StoreBannerImage(_ context.Context, data []byte, kind media.BannerImageKind) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.stored = append(f.stored, kind)
	if kind == media.BannerBackground {
		return "banners/new-bg.jpg", nil
	}
	return "banners/new.png", nil
}

func (f *fakeBannerImages) RemoveObject(_ context.Context, key string) error {
	f.removed = append(f.removed, key)
	return nil
}

func storedBanner() *banner.Banner {
	return &banner.Banner{
		Enabled: true, EyebrowRU: "Обувь для всей семьи", TitleRU: "Доставка по Бишкеку", ButtonRU: "Смотреть", ButtonKY: "Көрүү",
		BgColor: "#FFF3E9", TextColor: "#1A1A1A", ImageKey: strPtr("banners/old.png"),
		UpdatedAt: time.Date(2026, 10, 5, 6, 0, 0, 0, time.UTC),
	}
}

func newBannerPages(t *testing.T, store *fakeBannerStore, images *fakeBannerImages) *bannerPages {
	t.Helper()
	return &bannerPages{
		h:      &handlers{render: newTestRenderer(t)},
		store:  store,
		images: images,
		categoryTree: func(context.Context) ([]*catalog.Category, error) {
			return []*catalog.Category{{ID: "c1", NameRu: "Кроссовки", Slug: "sneakers"}}, nil
		},
		objectURL: func(key string) string {
			if key == "" {
				return ""
			}
			return "https://media.example/" + key
		},
	}
}

// bannerMultipart builds the form body; files maps field -> content.
func bannerMultipart(t *testing.T, fields url.Values, files map[string][]byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, vs := range fields {
		for _, v := range vs {
			if err := mw.WriteField(k, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	for field, data := range files {
		fw, err := mw.CreateFormFile(field, field+".png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

func postBanner(t *testing.T, p *bannerPages, st *staff.Staff, fields url.Values, files map[string][]byte) *httptest.ResponseRecorder {
	t.Helper()
	body, ct := bannerMultipart(t, fields, files)
	r := httptest.NewRequest(http.MethodPost, "/admin/banner", body)
	r.Header.Set("Content-Type", ct)
	r = r.WithContext(staff.NewContextWithStaff(r.Context(), st))
	w := httptest.NewRecorder()
	p.save(w, r)
	return w
}

func validBannerFields() url.Values {
	return url.Values{
		"enabled": {"1"}, "eyebrow_ru": {"Новинки"}, "title_ru": {" Осенняя коллекция "}, "title_ky": {""},
		"button_ru": {"Смотреть"}, "link_category_id": {"6f1c1f9e-7a43-4c55-9d0b-0a4f3c1d2e3f"},
		"bg_color": {"#102030"}, "text_color": {"#ffffff"},
	}
}

func TestBannerPageRendersStoredBanner(t *testing.T) {
	p := newBannerPages(t, &fakeBannerStore{current: storedBanner()}, &fakeBannerImages{})

	w := httptest.NewRecorder()
	p.page(w, requestAs(http.MethodGet, "/admin/banner?saved=1", ownerStaff(), nil))
	body := w.Body.String()

	for _, want := range []string{
		`enctype="multipart/form-data"`,
		`name="enabled" value="1" id="bn-enabled" checked`,
		`value="Доставка по Бишкеку"`,
		`name="button_ky" value="Көрүү"`,
		`<option value="c1" >Кроссовки</option>`,
		`name="bg_color" value="#FFF3E9"`,
		`type="color" value="#1A1A1A"`,
		`src="https://media.example/banners/old.png"`,
		`name="remove_image" value="1"`,
		"Баннер сохранён",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	if strings.Contains(body, `name="remove_bg_image"`) {
		t.Error("no background image stored, but a remove box is shown")
	}
}

func TestBannerPageLoadErrorIs500(t *testing.T) {
	p := newBannerPages(t, &fakeBannerStore{getErr: errors.New("db down")}, &fakeBannerImages{})

	w := httptest.NewRecorder()
	p.page(w, requestAs(http.MethodGet, "/admin/banner", ownerStaff(), nil))

	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "db down") {
		t.Errorf("status %d body %q", w.Code, w.Body.String())
	}
}

func TestBannerSaveUploadsPicturesAndRedirects(t *testing.T) {
	store := &fakeBannerStore{current: storedBanner()}
	images := &fakeBannerImages{}
	p := newBannerPages(t, store, images)

	w := postBanner(t, p, manager(), validBannerFields(), map[string][]byte{"image": []byte("png"), "bg_image": []byte("jpg")})

	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin/banner?saved=1" {
		t.Fatalf("status %d → %q: %s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	in := store.updated
	if in == nil || in.TitleRU != "Осенняя коллекция" || in.TextColor != "#FFFFFF" || !in.Enabled ||
		in.LinkCategoryID == nil || *in.LinkCategoryID != "6f1c1f9e-7a43-4c55-9d0b-0a4f3c1d2e3f" {
		t.Fatalf("saved input = %+v", in)
	}
	if deref(in.ImageKey) != "banners/new.png" || deref(in.BgImageKey) != "banners/new-bg.jpg" {
		t.Errorf("image keys = %q / %q", deref(in.ImageKey), deref(in.BgImageKey))
	}
	if len(images.stored) != 2 || images.stored[0] != media.BannerPicture || images.stored[1] != media.BannerBackground {
		t.Errorf("stored kinds = %v", images.stored)
	}
	if len(images.removed) != 0 {
		t.Errorf("replaced picture removed: %v", images.removed)
	}
}

func TestBannerSaveKeepsOrRemovesCurrentPicture(t *testing.T) {
	keep := &fakeBannerStore{current: storedBanner()}
	postBanner(t, newBannerPages(t, keep, &fakeBannerImages{}), ownerStaff(), validBannerFields(), nil)
	if deref(keep.updated.ImageKey) != "banners/old.png" {
		t.Errorf("kept image = %q", deref(keep.updated.ImageKey))
	}

	remove := &fakeBannerStore{current: storedBanner()}
	fields := validBannerFields()
	fields.Set("remove_image", "1")
	fields.Set("link_category_id", "")
	postBanner(t, newBannerPages(t, remove, &fakeBannerImages{}), ownerStaff(), fields, nil)
	if remove.updated.ImageKey != nil || remove.updated.LinkCategoryID != nil {
		t.Errorf("after remove: image=%v category=%v", remove.updated.ImageKey, remove.updated.LinkCategoryID)
	}
}

func TestBannerSaveValidationErrorIsStickyAndUploadsNothing(t *testing.T) {
	store := &fakeBannerStore{current: storedBanner()}
	images := &fakeBannerImages{}
	fields := validBannerFields()
	fields.Set("bg_color", "red")

	w := postBanner(t, newBannerPages(t, store, images), ownerStaff(), fields, map[string][]byte{"image": []byte("png")})

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "цвет фона баннера должен быть в формате #RRGGBB") || !strings.Contains(body, `value="Осенняя коллекция"`) {
		t.Error("error or sticky value missing")
	}
	if store.updated != nil || len(images.stored) != 0 {
		t.Errorf("saved=%v uploaded=%v, want nothing", store.updated, images.stored)
	}
}

func TestBannerSaveErrorInKyrgyz(t *testing.T) {
	fields := validBannerFields()
	fields.Set("title_ru", "")
	body, ct := bannerMultipart(t, fields, nil)
	r := httptest.NewRequest(http.MethodPost, "/admin/banner", body)
	r.Header.Set("Content-Type", ct)
	r = r.WithContext(contextWithLang(staff.NewContextWithStaff(r.Context(), ownerStaff()), "ky"))
	w := httptest.NewRecorder()

	newBannerPages(t, &fakeBannerStore{current: storedBanner()}, &fakeBannerImages{}).save(w, r)

	if !strings.Contains(w.Body.String(), "баннердин аталышын орусча жазыңыз") {
		t.Errorf("kyrgyz error missing: %s", w.Body.String())
	}
}

func TestBannerSaveRemovesUploadsWhenSaveFails(t *testing.T) {
	store := &fakeBannerStore{current: storedBanner(), updateErr: apperr.BadRequest("invalid_category_id", "категория не найдена")}
	images := &fakeBannerImages{}

	w := postBanner(t, newBannerPages(t, store, images), ownerStaff(), validBannerFields(), map[string][]byte{"image": []byte("png")})

	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "категория не найдена") {
		t.Fatalf("status %d", w.Code)
	}
	if len(images.removed) != 1 || images.removed[0] != "banners/new.png" {
		t.Errorf("removed = %v, want the fresh upload", images.removed)
	}
}

func TestBannerSaveShowsImageError(t *testing.T) {
	images := &fakeBannerImages{err: apperr.BadRequest("unsupported_image", "файл не является изображением JPEG, PNG или WEBP")}
	store := &fakeBannerStore{current: storedBanner()}

	w := postBanner(t, newBannerPages(t, store, images), ownerStaff(), validBannerFields(), map[string][]byte{"bg_image": []byte("gif")})

	if !strings.Contains(w.Body.String(), "файл не является изображением JPEG, PNG или WEBP") || store.updated != nil {
		t.Errorf("status %d, saved %v", w.Code, store.updated)
	}
}

func TestBannerRoutesRBACAndCSRF(t *testing.T) {
	for _, c := range []struct {
		name      string
		st        *staff.Staff
		wantCode  int
		wantSaved bool
	}{
		{"point_staff is redirected away", pointStaff(strPtr("pt1")), http.StatusSeeOther, false},
		{"manager may save", manager(), http.StatusSeeOther, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			store := &fakeBannerStore{current: storedBanner()}
			mux := http.NewServeMux()
			gate := requireStaffRole(&fakeResolver{st: c.st}, staff.RoleOwner, staff.RoleManager)
			registerBannerRoutes(mux, newBannerPages(t, store, &fakeBannerImages{}), gate)
			body, ct := bannerMultipart(t, validBannerFields(), nil)
			r := httptest.NewRequest(http.MethodPost, "http://cozy.kg/admin/banner", body)
			r.Header.Set("Content-Type", ct)
			r.Header.Set("Origin", "http://cozy.kg")
			w := httptest.NewRecorder()

			csrf.Protect(mux).ServeHTTP(w, r)

			if w.Code != c.wantCode || (store.updated != nil) != c.wantSaved {
				t.Errorf("status %d → %q, saved %v", w.Code, w.Header().Get("Location"), store.updated != nil)
			}
			if !c.wantSaved && w.Header().Get("Location") == "/admin/banner?saved=1" {
				t.Error("point_staff reached the save handler")
			}
		})
	}

	t.Run("cross-site post is blocked", func(t *testing.T) {
		store := &fakeBannerStore{current: storedBanner()}
		mux := http.NewServeMux()
		registerBannerRoutes(mux, newBannerPages(t, store, &fakeBannerImages{}),
			requireStaffRole(&fakeResolver{st: ownerStaff()}, staff.RoleOwner, staff.RoleManager))
		body, ct := bannerMultipart(t, validBannerFields(), nil)
		r := httptest.NewRequest(http.MethodPost, "http://cozy.kg/admin/banner", body)
		r.Header.Set("Content-Type", ct)
		r.Header.Set("Origin", "https://evil.example")
		w := httptest.NewRecorder()

		csrf.Protect(mux).ServeHTTP(w, r)

		if w.Code != http.StatusForbidden || store.updated != nil {
			t.Errorf("status %d, saved %v", w.Code, store.updated != nil)
		}
	})
}

func TestBannerAuditEntryListsChangedFields(t *testing.T) {
	before := storedBanner()
	after := *before
	after.TitleRU = "Новое"
	after.ImageKey = nil

	e := bannerAuditEntry(before, &after)

	if e.Action != audit.ActionBannerUpdate || e.EntityType != audit.EntityBanner || e.MsgKey != audit.MsgBannerUpdated {
		t.Errorf("entry = %+v", e)
	}
	if len(e.Details) != 2 || e.Details["banner_title_ru"] == nil || e.Details["banner_image"] == nil {
		t.Errorf("details = %v", e.Details)
	}
	if got := auditDetailsText(kyTr, e.Details); strings.Contains(got, "banner_title_ru") {
		t.Errorf("raw field code in journal text: %q", got)
	}
}
