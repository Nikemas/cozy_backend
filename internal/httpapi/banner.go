package httpapi

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/banner"
	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/i18n"
)

// bannerCacheControl is GET /api/v1/banner's caching policy — exactly
// "public, max-age=60" per the contract the mobile app is built against
// (so not httpmw.PublicCache, which adds stale-while-revalidate). The
// response varies by Accept-Language, hence the Vary header next to it.
const bannerCacheControl = "public, max-age=60"

// bannerGetter is the subset of *banner.Store this handler depends on, so
// the handler test can inject a fake — mirrors branchLister in points.go.
type bannerGetter interface {
	Get(ctx context.Context) (*banner.Banner, error)
}

// RegisterPublicBannerRoutes mounts GET /api/v1/banner: the home-screen
// promo banner the owner edits at /admin/banner. Public and
// unauthenticated, like /api/v1/points. cfg builds the absolute image
// URLs (MINIO_PUBLIC_ENDPOINT), the same way product photo_url is built.
func RegisterPublicBannerRoutes(mux *http.ServeMux, db *sql.DB, cfg *config.Config) {
	mux.Handle("GET /api/v1/banner", apperr.Wrap(getBannerHandler(banner.NewStore(db), cfg.PublicObjectURL)))
}

// bannerResponse is the body of GET /api/v1/banner. Banner is null when
// Enabled is false.
type bannerResponse struct {
	Enabled bool          `json:"enabled"`
	Banner  *bannerFields `json:"banner"`
}

// bannerFields are the banner's texts in the request's language plus its
// presentation. Nullable fields are pointers so they serialize as null.
type bannerFields struct {
	Eyebrow            string    `json:"eyebrow"`
	Title              string    `json:"title"`
	ButtonText         string    `json:"button_text"`
	CategorySlug       *string   `json:"category_slug"`
	CategoryID         *string   `json:"category_id"`
	BgColor            string    `json:"bg_color"`
	TextColor          string    `json:"text_color"`
	EyebrowColor       string    `json:"eyebrow_color"`
	ImageURL           *string   `json:"image_url"`
	BackgroundImageURL *string   `json:"background_image_url"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func newBannerResponse(b *banner.Banner, lang string, objectURL func(string) string) bannerResponse {
	if b == nil || !b.Enabled {
		return bannerResponse{Enabled: false}
	}
	texts := b.TextsFor(lang)
	categoryID, categorySlug := linkedCategory(b)
	return bannerResponse{Enabled: true, Banner: &bannerFields{
		Eyebrow:            texts.Eyebrow,
		Title:              texts.Title,
		ButtonText:         texts.Button,
		CategorySlug:       categorySlug,
		CategoryID:         categoryID,
		BgColor:            b.BgColor,
		TextColor:          b.TextColor,
		EyebrowColor:       b.EyebrowColor,
		ImageURL:           objectURLPtr(b.ImageKey, objectURL),
		BackgroundImageURL: objectURLPtr(b.BgImageKey, objectURL),
		UpdatedAt:          b.UpdatedAt.UTC(),
	}}
}

// linkedCategory returns the linked category's id and slug — both or
// neither: the app navigates by id, the site by slug, so a half-filled
// pair (which the FK + join shouldn't produce anyway) is sent as no link.
func linkedCategory(b *banner.Banner) (id, slug *string) {
	if b.LinkCategoryID == nil || *b.LinkCategoryID == "" || b.LinkCategorySlug == nil || *b.LinkCategorySlug == "" {
		return nil, nil
	}
	return b.LinkCategoryID, b.LinkCategorySlug
}

func objectURLPtr(key *string, objectURL func(string) string) *string {
	if key == nil || *key == "" {
		return nil
	}
	u := objectURL(*key)
	return &u
}

// bannerLang is the response language: Accept-Language (ky or ru), else
// Russian.
func bannerLang(r *http.Request) string {
	if lang, ok := i18n.FromAcceptLanguage(r.Header.Get("Accept-Language")); ok {
		return lang
	}
	return i18n.DefaultLang
}

func getBannerHandler(store bannerGetter, objectURL func(string) string) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		b, err := store.Get(r.Context())
		if err != nil {
			return err
		}
		lang := bannerLang(r)
		w.Header().Set("Cache-Control", bannerCacheControl)
		w.Header().Add("Vary", "Accept-Language")
		return writeJSON(w, http.StatusOK, newBannerResponse(b, lang, objectURL))
	}
}
