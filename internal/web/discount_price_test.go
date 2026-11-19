package web

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/i18n"
)

func TestOldPriceText(t *testing.T) {
	h := newTestHandlers(t)
	old := 5500.0
	if got := h.oldPriceText(&old, 4500, i18n.LangRU); !strings.Contains(got, "5") || got == "" {
		t.Errorf("discounted: got %q, want the formatted old price", got)
	}
	if got := h.oldPriceText(&old, 6000, i18n.LangRU); got != "" {
		t.Errorf("old price below shown price: got %q, want empty", got)
	}
	if got := h.oldPriceText(nil, 4500, i18n.LangRU); got != "" {
		t.Errorf("no old price: got %q, want empty", got)
	}
}

func TestProductCardRendersDiscount(t *testing.T) {
	rr := newTestRenderer(t)
	render := func(card ProductCard) string {
		w := httptest.NewRecorder()
		sd := &ShopData{BasePath: "/", Page: 1, TotalPages: 1, Products: []ProductCard{card}, Total: 1}
		if err := rr.Render(w, "shop", PageData{Lang: i18n.LangRU, Screen: "shop", Data: sd}); err != nil {
			t.Fatalf("Render: %v", err)
		}
		return w.Body.String()
	}

	sale := render(ProductCard{Name: "Кеды", PriceText: "4 500 сом", OldPriceText: "5 500 сом"})
	for _, want := range []string{"price--sale", `<s class="price-old">`, "5 500 сом", "4 500 сом"} {
		if !strings.Contains(sale, want) {
			t.Errorf("discounted card missing %q:\n%s", want, sale)
		}
	}

	plain := render(ProductCard{Name: "Кеды", PriceText: "4 500 сом"})
	if strings.Contains(plain, "price--sale") || strings.Contains(plain, `<s class="price-old">`) {
		t.Errorf("card without discount must not render sale markup:\n%s", plain)
	}
}

func TestQuickBuyModalRendersDiscount(t *testing.T) {
	rr := newTestRenderer(t)
	w := httptest.NewRecorder()
	qd := &QuickBuyData{Name: "Кеды", PriceText: "4 500 сом", OldPriceText: "5 500 сом"}
	if err := rr.RenderPartial(w, "shop", "_quickbuy_modal", PageData{Lang: i18n.LangRU, Data: qd}); err != nil {
		t.Fatalf("RenderPartial: %v", err)
	}
	body := w.Body.String()
	for _, want := range []string{"price--sale", `<s class="price-old">`, "Старая цена"} {
		if !strings.Contains(body, want) {
			t.Errorf("quick-buy modal missing %q", want)
		}
	}
}
