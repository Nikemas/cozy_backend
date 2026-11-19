package web

import (
	"regexp"
	"strings"
	"testing"
)

// TestCheckoutCourierPhoneField: a delivery checkout has a required tel
// field for the courier's contact number, pre-filled with the account
// phone; it sits with the delivery options (pickup disables it in the
// page script, so a pickup order doesn't need it).
func TestCheckoutCourierPhoneField(t *testing.T) {
	addr := []AddressView{{ID: "a1", AddressText: "ул. Ленина 1"}}
	body := renderCheckout(t, CheckoutPageData{Addresses: addr, ItemsTotal: 5000, ItemCount: 1,
		CourierPhone: "+996700123456"})

	re := regexp.MustCompile(`<input[^>]*id="checkout-courier-phone"[^>]*>`)
	input := re.FindString(body)
	if input == "" {
		t.Fatalf("no courier phone input in:\n%s", body)
	}
	for _, want := range []string{`type="tel"`, `name="courier_phone"`, `value="&#43;996700123456"`, `required`, `autocomplete="tel"`} {
		if !strings.Contains(input, want) {
			t.Errorf("courier phone input %s lacks %s", input, want)
		}
	}
	if !strings.Contains(body, `<label class="field-label" for="checkout-courier-phone">Номер для курьера</label>`) {
		t.Error("courier phone field has no Russian label")
	}
	if !strings.Contains(body, "courierPhone.disabled") {
		t.Error("page script must disable the courier phone for pickup")
	}

	// No saved address → no delivery → no courier phone field.
	body = renderCheckout(t, CheckoutPageData{Points: []PointView{{ID: "p1", Name: "Cozy", Address: "Чуй 1"}},
		ItemsTotal: 5000, ItemCount: 1, CourierPhone: "+996700123456"})
	if strings.Contains(body, `name="courier_phone"`) {
		t.Error("courier phone shown without a delivery option")
	}
}
