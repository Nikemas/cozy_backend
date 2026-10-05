package web

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/banner"
	"github.com/Nikemas/cozy_backend/internal/i18n"
)

type fakeBanners struct {
	b   *banner.Banner
	err error
}

func (f fakeBanners) Get(context.Context) (*banner.Banner, error) { return f.b, f.err }

func strp(s string) *string { return &s }

func objURL(key string) string { return "https://media.example/cozy-media/" + key }

func newBannerHandlers(t *testing.T, getter bannerGetter) *handlers {
	t.Helper()
	restore := chdir(t, repoRoot(t))
	defer restore()
	bundle, err := i18n.Load("locales")
	if err != nil {
		t.Fatal(err)
	}
	return &handlers{bundle: bundle, banners: getter}
}

func TestNewBannerViewDisabledIsHidden(t *testing.T) {
	if v := newBannerView(&banner.Banner{Enabled: false, TitleRU: "T"}, i18n.LangRU, objURL); v != nil {
		t.Errorf("view = %+v, want nil", v)
	}
}

func TestNewBannerView(t *testing.T) {
	b := &banner.Banner{
		Enabled: true, EyebrowRU: "Обувь", TitleRU: "Доставка", TitleKY: "Жеткирүү", ButtonRU: "Смотреть",
		BgColor: "#FFF3E9", TextColor: "#1A1A1A",
	}
	plain := newBannerView(b, i18n.LangKY, objURL)
	if plain.Title != "Жеткирүү" || plain.Eyebrow != "Обувь" || plain.ButtonText != "Смотреть" {
		t.Errorf("ky texts with ru fallback = %+v", plain)
	}
	if plain.ButtonHref != "#catalog" || plain.CustomInk || plain.ImageURL != "" || plain.BgImageURL != "" {
		t.Errorf("defaults = %+v", plain)
	}

	linked := *b
	linked.LinkCategorySlug = strp("sneakers")
	linked.ImageKey, linked.BgImageKey = strp("banners/a.png"), strp("banners/b.jpg")
	linked.TextColor = "#FFFFFF"
	v := newBannerView(&linked, i18n.LangRU, objURL)
	if v.ButtonHref != "/catalog/sneakers" || !v.CustomInk ||
		v.ImageURL != "https://media.example/cozy-media/banners/a.png" || v.BgImageURL != "https://media.example/cozy-media/banners/b.jpg" {
		t.Errorf("view = %+v", v)
	}
}

func TestHomeBannerFallsBackToStaticTextsOnError(t *testing.T) {
	h := newBannerHandlers(t, fakeBanners{err: errors.New("db down")})

	v := h.homeBanner(context.Background(), i18n.LangKY)

	if v == nil || v.Eyebrow != h.t(i18n.LangKY, "shop.banner.eyebrow") || v.Title != h.t(i18n.LangKY, "shop.banner.title") {
		t.Fatalf("fallback = %+v", v)
	}
	if v.BgColor != "" || v.TextColor != "" || v.ButtonText != "" {
		t.Errorf("fallback must use stylesheet defaults and no button: %+v", v)
	}
}

func TestHomeBannerHiddenWhenDisabled(t *testing.T) {
	h := newBannerHandlers(t, fakeBanners{b: &banner.Banner{Enabled: false}})

	if v := h.homeBanner(context.Background(), i18n.LangRU); v != nil {
		t.Errorf("view = %+v, want nil", v)
	}
}

func renderShopBanner(t *testing.T, v *BannerView) string {
	t.Helper()
	rr := newTestRenderer(t)
	sd := &ShopData{BasePath: "/", Page: 1, TotalPages: 1, ShowBanner: true, Banner: v,
		Categories: []CategoryChip{{Label: "Все", Href: "/", Active: true}}}
	w := httptest.NewRecorder()
	if err := rr.Render(w, "shop", PageData{Lang: i18n.LangRU, Screen: "shop", Data: sd}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	return w.Body.String()
}

func TestRenderShopBannerFromAdmin(t *testing.T) {
	body := renderShopBanner(t, &BannerView{
		Eyebrow: "Новинки", Title: "Осень <2026>", ButtonText: "Смотреть", ButtonHref: "/catalog/sneakers",
		BgColor: "#102030", TextColor: "#FFFFFF", CustomInk: true,
		ImageURL: "https://media.example/cozy-media/banners/a.png", BgImageURL: "https://media.example/cozy-media/banners/b.jpg",
	})

	for _, want := range []string{
		`class="shop-banner shop-banner--ink"`,
		`--banner-bg: #102030;`,
		`--banner-ink: #FFFFFF;`,
		`background-image: url(https://media.example/cozy-media/banners/b.jpg);`,
		`<div class="shop-banner__eyebrow">Новинки</div>`,
		`<div class="shop-banner__title">Осень &lt;2026&gt;</div>`,
		`<a class="btn btn--primary shop-banner__btn" href="/catalog/sneakers">Смотреть</a>`,
		`<img class="shop-banner__img" src="https://media.example/cozy-media/banners/a.png" alt="Осень &lt;2026&gt;" width="240" height="160" loading="eager"`,
		`id="catalog"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered banner lacks %s", want)
		}
	}
	if strings.Contains(body, `viewBox="0 0 100 60"`) {
		t.Error("decorative SVG should be replaced by the picture")
	}
}

func TestRenderShopStaticBannerKeepsLookAndSVG(t *testing.T) {
	body := renderShopBanner(t, &BannerView{Eyebrow: "Обувь для всей семьи", Title: "Доставка"})

	if !strings.Contains(body, `<div class="shop-banner">`) {
		t.Error("static banner should carry no inline style")
	}
	if !strings.Contains(body, `viewBox="0 0 100 60"`) || strings.Contains(body, "shop-banner__btn") {
		t.Error("static banner: want the SVG and no button")
	}
}

func TestRenderShopWithoutBannerWhenDisabled(t *testing.T) {
	body := renderShopBanner(t, nil)

	if strings.Contains(body, "shop-banner") {
		t.Error("disabled banner rendered")
	}
}
