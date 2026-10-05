package web

import (
	"context"
	"log/slog"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/banner"
)

// bannerGetter is the subset of *banner.Store the shop screen reads — an
// interface so tests can fake it.
type bannerGetter interface {
	Get(ctx context.Context) (*banner.Banner, error)
}

// BannerView backs the promo banner on the home page (shop.gohtml's
// .shop-banner), editable at /admin/banner. Empty color/image fields mean
// "the stylesheet's default" — that is also what the static fallback
// (banner not loadable) looks like.
type BannerView struct {
	Eyebrow    string
	Title      string
	ButtonText string // "" = no button
	ButtonHref string
	// BgColor/TextColor are validated "#RRGGBB" (banner.Input.Validate +
	// a CHECK in migration 000039), so they are safe in a style attribute.
	BgColor   string
	TextColor string
	// CustomInk: the owner picked a text color other than the default, so
	// the eyebrow follows it instead of the accent orange (which may not
	// read on the chosen background).
	CustomInk  bool
	ImageURL   string // picture on the right; "" keeps the decorative SVG
	BgImageURL string // full background, covered
}

// catalogAnchor is where a banner without a linked category points: the
// product grid right below it (the home page already is the whole
// catalog).
const catalogAnchor = "#catalog"

// homeBanner loads the banner for the home page in lang. nil means the
// owner switched it off. A load failure never fails the page: it is
// logged and the static locale texts are shown instead (the look the
// banner had before it became editable).
func (h *handlers) homeBanner(ctx context.Context, lang string) *BannerView {
	if h.banners == nil {
		return h.staticBanner(lang)
	}
	b, err := h.banners.Get(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "web: loading home banner failed, showing static texts", "err", err)
		return h.staticBanner(lang)
	}
	return newBannerView(b, lang, h.photoURL)
}

func (h *handlers) staticBanner(lang string) *BannerView {
	return &BannerView{
		Eyebrow: h.t(lang, "shop.banner.eyebrow"),
		Title:   h.t(lang, "shop.banner.title"),
	}
}

// newBannerView shapes a stored banner for the template; nil when it is
// disabled. objectURL turns a MinIO object key into a public URL.
func newBannerView(b *banner.Banner, lang string, objectURL func(string) string) *BannerView {
	if b == nil || !b.Enabled {
		return nil
	}
	texts := b.TextsFor(lang)
	v := &BannerView{
		Eyebrow:    texts.Eyebrow,
		Title:      texts.Title,
		ButtonText: texts.Button,
		ButtonHref: catalogAnchor,
		BgColor:    b.BgColor,
		TextColor:  b.TextColor,
		CustomInk:  !strings.EqualFold(b.TextColor, banner.DefaultTextColor),
	}
	if b.LinkCategorySlug != nil && *b.LinkCategorySlug != "" {
		v.ButtonHref = "/catalog/" + *b.LinkCategorySlug // same URL as the category chips
	}
	if b.ImageKey != nil && *b.ImageKey != "" {
		v.ImageURL = objectURL(*b.ImageKey)
	}
	if b.BgImageKey != nil && *b.BgImageKey != "" {
		v.BgImageURL = objectURL(*b.BgImageKey)
	}
	return v
}
