package web

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/internal/orders"
)

// withPickup installs process settings with self-pickup on or off for one
// test and restores the previous settings afterwards.
func withPickup(t *testing.T, enabled bool) {
	t.Helper()
	prev := orders.CurrentSettings()
	st := prev
	st.PickupDisabled = !enabled
	orders.SetDefaultSettings(st)
	t.Cleanup(func() { orders.SetDefaultSettings(prev) })
}

func renderCheckout(t *testing.T, data CheckoutPageData) string {
	t.Helper()
	w := httptest.NewRecorder()
	page := PageData{Lang: i18n.LangRU, Screen: "checkout", Authed: true, Data: data}
	if err := newTestRenderer(t).Render(w, "checkout", page); err != nil {
		t.Fatalf("Render: %v", err)
	}
	return w.Body.String()
}

func TestCheckoutHidesPickupWhenDisabled(t *testing.T) {
	addr := []AddressView{{ID: "a1", Label: "Дом", AddressText: "ул. Ленина 1", IsDefault: true}}
	points := []PointView{{ID: "p1", Name: "Cozy ЦУМ", Address: "пр. Чуй 155"}}

	withPickup(t, false)
	body := renderCheckout(t, CheckoutPageData{Addresses: addr, ItemsTotal: 5000, ItemCount: 1})
	if strings.Contains(body, `value="pickup"`) || strings.Contains(body, "Самовывоз") {
		t.Error("checkout offers self-pickup while PICKUP_ENABLED is off")
	}
	if !strings.Contains(body, `value="delivery" checked`) {
		t.Error("delivery must be preselected when pickup is off")
	}

	body = renderCheckout(t, CheckoutPageData{ItemsTotal: 5000, ItemCount: 1})
	if !strings.Contains(body, `href="/addresses"`) || strings.Contains(body, "временно недоступны") {
		t.Error("with no address and no pickup the checkout must link to adding an address")
	}

	withPickup(t, true)
	body = renderCheckout(t, CheckoutPageData{Addresses: addr, Points: points, ItemsTotal: 5000, ItemCount: 1})
	if !strings.Contains(body, `value="pickup"`) {
		t.Error("checkout must offer self-pickup when PICKUP_ENABLED is on")
	}
}

func TestStaticPagesFollowPickupFlag(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		withPickup(t, enabled)
		for _, lang := range []string{i18n.LangRU, i18n.LangKY} {
			w := httptest.NewRecorder()
			if err := newTestRenderer(t).Render(w, "page_delivery", PageData{Lang: lang, Screen: "page_delivery"}); err != nil {
				t.Fatalf("Render delivery %s: %v", lang, err)
			}
			has := strings.Contains(w.Body.String(), "ti-building-store")
			if has != enabled {
				t.Errorf("delivery page (%s, pickup=%v) shows pickup section = %v", lang, enabled, has)
			}
		}
	}
}
