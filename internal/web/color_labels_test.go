package web

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/internal/orders"
)

func TestColorFilterOptionsLabelOnlyInKY(t *testing.T) {
	ky := colorFilterOptions([]string{"Чёрный", "Терракотовый"}, "Чёрный", i18n.LangKY)
	want := []FilterOption{
		{Value: "Чёрный", Label: "Кара", Selected: true},
		{Value: "Терракотовый", Label: "Терракотовый"},
	}
	if len(ky) != len(want) || ky[0] != want[0] || ky[1] != want[1] {
		t.Fatalf("ky options = %+v, want %+v", ky, want)
	}
	ru := colorFilterOptions([]string{"Чёрный"}, "", i18n.LangRU)
	if ru[0].Label != "Чёрный" || ru[0].Value != "Чёрный" {
		t.Fatalf("ru options = %+v, want raw label", ru)
	}
}

func TestBuildProductDataColorLabelsKY(t *testing.T) {
	h := newProductPageHandlers(t, [][2]any{{"v2", 3}})
	pd, err := h.buildProductData(context.Background(), url.Values{}, i18n.LangKY, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(pd.Colors) != 2 {
		t.Fatalf("colors = %+v, want 2", pd.Colors)
	}
	if pd.Colors[0].Label != "Көк" || pd.Colors[1].Label != "Кара" {
		t.Errorf("labels = %q/%q, want Көк/Кара", pd.Colors[0].Label, pd.Colors[1].Label)
	}
	if !strings.Contains(pd.Colors[1].Href, "color="+url.QueryEscape("Чёрный")) {
		t.Errorf("href = %q, want the raw color in the query", pd.Colors[1].Href)
	}
	if pd.SelectedColor != "Чёрный" {
		t.Errorf("SelectedColor = %q, want the raw value for the hidden input", pd.SelectedColor)
	}
}

func TestBuildOrderViewsLocalizesItemColor(t *testing.T) {
	list := []orders.Order{{ID: "o1", Items: []orders.OrderItem{
		{ProductNameSnapshot: "Air", SizeSnapshot: "42", ColorSnapshot: "Чёрный", Quantity: 1},
	}}}
	noop := func(key string) string { return key }

	ky := buildOrderViews(list, noop, i18n.LangKY, nil)
	if got := ky[0].Items[0].Title; got != "Air — order.item.size_prefix 42, кара" {
		t.Errorf("ky title = %q", got)
	}
	ru := buildOrderViews(list, noop, i18n.LangRU, nil)
	if got := ru[0].Items[0].Title; got != "Air — order.item.size_prefix 42, чёрный" {
		t.Errorf("ru title = %q", got)
	}
}

func TestRenderCartShowsKYColorLabel(t *testing.T) {
	rr := newTestRenderer(t)
	data := PageData{Lang: i18n.LangKY, Screen: "cart", Authed: true, Data: &CartPageData{
		Lines: []CartLineView{{VariantID: "v1", ProductName: "Air", Size: "42", Color: "Белый", Qty: 1, UnitPrice: 1, LineTotal: 1}},
	}}
	w := httptest.NewRecorder()
	if err := rr.Render(w, "cart", data); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if body := w.Body.String(); !strings.Contains(body, "42 · Ак") {
		t.Errorf("ky cart should show the Kyrgyz color label")
	}

	data.Lang = i18n.LangRU
	w = httptest.NewRecorder()
	if err := rr.Render(w, "cart", data); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if body := w.Body.String(); !strings.Contains(body, "42 · Белый") {
		t.Errorf("ru cart should show the raw color")
	}
}
