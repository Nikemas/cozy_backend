package web

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/i18n"
)

func TestDefaultQuickBuySelection(t *testing.T) {
	variants := []catalog.Variant{
		{ID: "v1", Size: "40", Color: "Синий"},
		{ID: "v2", Size: "40", Color: "Чёрный"},
		{ID: "v3", Size: "41", Color: "Синий"},
	}
	tests := []struct {
		name                string
		qty                 map[string]int
		size, color         string
		wantSize, wantColor string
	}{
		{"first available variant wins", map[string]int{"v2": 3}, "", "", "40", "Чёрный"},
		{"skips unavailable leading variants", map[string]int{"v3": 1}, "", "", "41", "Синий"},
		{"nothing in stock falls back to first", map[string]int{}, "", "", "40", "Синий"},
		{"explicit selection is kept", map[string]int{"v2": 3}, "41", "Синий", "41", "Синий"},
		{"partial selection is completed from first values", map[string]int{"v2": 3}, "41", "", "41", "Синий"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			size, color := defaultQuickBuySelection(variants, tc.qty, tc.size, tc.color)
			if size != tc.wantSize || color != tc.wantColor {
				t.Fatalf("got %q/%q, want %q/%q", size, color, tc.wantSize, tc.wantColor)
			}
		})
	}
}

func TestQuickBuyModalExplainsOutOfStock(t *testing.T) {
	rr := newTestRenderer(t)
	render := func(inStock bool) string {
		w := httptest.NewRecorder()
		qd := &QuickBuyData{Name: "Кроссовки", InStock: inStock}
		if err := rr.RenderPartial(w, "shop", "_quickbuy_modal", PageData{Lang: i18n.LangRU, Data: qd}); err != nil {
			t.Fatalf("RenderPartial: %v", err)
		}
		return w.Body.String()
	}
	const hint = "сейчас нет в наличии"
	if !strings.Contains(render(false), hint) {
		t.Errorf("out-of-stock modal missing hint %q", hint)
	}
	if strings.Contains(render(true), hint) {
		t.Errorf("in-stock modal must not show the out-of-stock hint")
	}
}
