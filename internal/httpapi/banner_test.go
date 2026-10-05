package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/banner"
)

type fakeBannerGetter struct {
	b   *banner.Banner
	err error
}

func (f fakeBannerGetter) Get(context.Context) (*banner.Banner, error) { return f.b, f.err }

func strp(s string) *string { return &s }

func testObjectURL(key string) string { return "https://media.cozy.kg/cozy-media/" + key }

func seededBanner() *banner.Banner {
	return &banner.Banner{
		Enabled:   true,
		EyebrowRU: "Обувь для всей семьи", EyebrowKY: "Бүт үй-бүлө үчүн бут кийим",
		TitleRU: "Доставка по Бишкеку", TitleKY: "",
		ButtonRU: "Смотреть", ButtonKY: "Көрүү",
		BgColor: "#FFF3E9", TextColor: "#1A1A1A", EyebrowColor: "#B35400",
		UpdatedAt: time.Date(2026, 10, 5, 18, 0, 0, 0, time.FixedZone("KGT", 6*3600)),
	}
}

func serveBanner(t *testing.T, store bannerGetter, acceptLanguage string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/banner", nil)
	if acceptLanguage != "" {
		req.Header.Set("Accept-Language", acceptLanguage)
	}
	rec := httptest.NewRecorder()
	apperr.Wrap(getBannerHandler(store, testObjectURL)).ServeHTTP(rec, req)
	return rec
}

func TestGetBannerEnabledMatchesContract(t *testing.T) {
	rec := serveBanner(t, fakeBannerGetter{b: seededBanner()}, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=60" {
		t.Errorf("Cache-Control = %q", got)
	}
	if got := rec.Header().Get("Vary"); got != "Accept-Language" {
		t.Errorf("Vary = %q", got)
	}
	want := `{"enabled":true,"banner":{"eyebrow":"Обувь для всей семьи","title":"Доставка по Бишкеку",` +
		`"button_text":"Смотреть","category_slug":null,"category_id":null,"bg_color":"#FFF3E9","text_color":"#1A1A1A",` +
		`"eyebrow_color":"#B35400","image_url":null,"background_image_url":null,"updated_at":"2026-10-05T12:00:00Z"}}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("body =\n%s\nwant\n%s", got, want)
	}
}

func TestGetBannerKyrgyzFallsBackToRussianPerField(t *testing.T) {
	rec := serveBanner(t, fakeBannerGetter{b: seededBanner()}, "ky-KG,ru;q=0.8")

	var body bannerResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Banner.Eyebrow != "Бүт үй-бүлө үчүн бут кийим" || body.Banner.ButtonText != "Көрүү" {
		t.Errorf("ky texts = %+v", body.Banner)
	}
	if body.Banner.Title != "Доставка по Бишкеку" {
		t.Errorf("empty ky title should fall back to ru, got %q", body.Banner.Title)
	}
}

func TestGetBannerUnsupportedLanguageIsRussian(t *testing.T) {
	rec := serveBanner(t, fakeBannerGetter{b: seededBanner()}, "en-US")

	if !strings.Contains(rec.Body.String(), `"button_text":"Смотреть"`) {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestGetBannerBuildsAbsoluteImageURLsAndCategory(t *testing.T) {
	b := seededBanner()
	b.ImageKey, b.BgImageKey = strp("banners/a.png"), strp("banners/b.jpg")
	b.LinkCategoryID, b.LinkCategorySlug = strp("c1"), strp("sneakers")

	rec := serveBanner(t, fakeBannerGetter{b: b}, "ru")

	for _, want := range []string{
		`"image_url":"https://media.cozy.kg/cozy-media/banners/a.png"`,
		`"background_image_url":"https://media.cozy.kg/cozy-media/banners/b.jpg"`,
		`"category_slug":"sneakers"`, `"category_id":"c1"`,
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("body lacks %s: %s", want, rec.Body.String())
		}
	}
}

func TestGetBannerSendsCategoryIDAndSlugTogether(t *testing.T) {
	b := seededBanner()
	b.LinkCategoryID = strp("c1") // slug missing: inconsistent row

	rec := serveBanner(t, fakeBannerGetter{b: b}, "")

	if !strings.Contains(rec.Body.String(), `"category_slug":null,"category_id":null`) {
		t.Errorf("half a category link leaked: %s", rec.Body.String())
	}
}

func TestGetBannerDisabledHasNullBanner(t *testing.T) {
	b := seededBanner()
	b.Enabled = false

	rec := serveBanner(t, fakeBannerGetter{b: b}, "")

	if got := strings.TrimSpace(rec.Body.String()); got != `{"enabled":false,"banner":null}` {
		t.Errorf("body = %s", got)
	}
	if rec.Header().Get("Cache-Control") != "public, max-age=60" {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
}

func TestGetBannerStoreErrorIs500WithoutCache(t *testing.T) {
	rec := serveBanner(t, fakeBannerGetter{err: errors.New("db down")}, "")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d", rec.Code)
	}
	if rec.Header().Get("Cache-Control") == "public, max-age=60" {
		t.Error("an error response must not be publicly cached")
	}
}
