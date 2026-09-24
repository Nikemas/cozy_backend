package web

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/internal/storefront"
)

func TestBuildAvailabilityListsActivePointsWithStock(t *testing.T) {
	branches := []storefront.Branch{
		{ID: "p1", Name: "Cozy Asia Mall", Address: "пр. Чуй, 3"},
		{ID: "p2", Name: "Cozy Bishkek Park", Address: "ул. Киевская, 148"},
		{ID: "p3", Name: "Cozy Vefa", Address: "ул. Горького, 1"},
	}
	stock := []catalog.StockEntry{
		{VariantID: "v1", PointID: "p2", Quantity: 5},
		{VariantID: "v1", PointID: "p3", Quantity: 1},
		{VariantID: "v1", PointID: "p1", Quantity: 0},       // sold out here
		{VariantID: "v2", PointID: "p1", Quantity: 9},       // another variant
		{VariantID: "v1", PointID: "inactive", Quantity: 4}, // not an active point
	}

	got := buildAvailability("v1", stock, branches)
	if len(got) != 2 {
		t.Fatalf("got %d points, want 2: %+v", len(got), got)
	}
	if got[0].Name != "Cozy Bishkek Park" || got[0].Qty != 5 || got[0].Few {
		t.Errorf("first point = %+v, want Bishkek Park ×5 (not few)", got[0])
	}
	if got[1].Name != "Cozy Vefa" || !got[1].Few {
		t.Errorf("second point = %+v, want Vefa marked Few", got[1])
	}

	if got := buildAvailability("", stock, branches); got != nil {
		t.Errorf("no selected variant should yield nil, got %+v", got)
	}
}

func TestBuildGalleryUsesFullAndThumbVariants(t *testing.T) {
	images := []catalog.ProductImage{
		{ObjectKey: "products/a/1/full.jpg"},
		{ObjectKey: "products/a/2/full.jpg"},
	}
	got := buildGallery(images, func(k string) string { return "https://m/" + k })
	if len(got) != 2 {
		t.Fatalf("got %d photos, want 2", len(got))
	}
	if got[0].URL != "https://m/products/a/1/full.jpg" {
		t.Errorf("URL = %q", got[0].URL)
	}
	if got[0].ThumbURL == got[0].URL {
		t.Errorf("ThumbURL should point at the thumb variant, got %q", got[0].ThumbURL)
	}
}

func TestRenderProductShowsGalleryDescriptionAndAvailability(t *testing.T) {
	rr := newTestRenderer(t)
	pd := &ProductData{
		Name:        "Кроссовки Air",
		Brand:       "Cozy",
		PriceText:   "4 500 сом",
		Description: "Лёгкие кроссовки на каждый день.",
		HasPhoto:    true,
		PhotoURL:    "https://m/1_full.jpg",
		Photos: []ProductPhoto{
			{URL: "https://m/1_full.jpg", ThumbURL: "https://m/1_thumb.jpg"},
			{URL: "https://m/2_full.jpg", ThumbURL: "https://m/2_thumb.jpg"},
		},
		Sizes:             []SizeOption{{Label: "42", Selected: true, Available: true, Href: "/product/x?size=42"}},
		SelectedVariantID: "v1",
		InStock:           true,
		Availability:      []PointStock{{Name: "Cozy Bishkek Park", Address: "ул. Киевская", Qty: 1, Few: true}},
		ProductPath:       "/product/x",
		CartActionURL:     "/product/x/cart",
		BuyActionURL:      "/product/x/buy",
		FavoriteActionURL: "/favorites/x",
	}
	w := httptest.NewRecorder()
	if err := rr.Render(w, "product", PageData{Lang: i18n.LangRU, Screen: "product", Data: pd}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := w.Body.String()
	for _, want := range []string{
		`data-photo="https://m/2_full.jpg"`,
		`loading="lazy"`,
		"Лёгкие кроссовки на каждый день.",
		"Cozy Bishkek Park",
		"осталось 1",
		`aria-current="true"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("product page missing %q", want)
		}
	}

	// Out of stock: buy buttons disabled, "нет в наличии" shown.
	pd.InStock, pd.Availability = false, nil
	w = httptest.NewRecorder()
	if err := rr.Render(w, "product", PageData{Lang: i18n.LangRU, Screen: "product", Data: pd}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(w.Body.String(), "сейчас нет в наличии") || !strings.Contains(w.Body.String(), "disabled") {
		t.Error("out-of-stock product should say so and disable the buy buttons")
	}
}
