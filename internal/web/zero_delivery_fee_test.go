package web

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/i18n"
)

// A zero delivery fee (DELIVERY_FEE_SOM=0 with no active zones, or a zone
// that is free for this cart) must not print any delivery amount on the
// customer site — no "Доставка 0 сом", no "от 0 сом" — and the total is
// just the items. Positive fees render exactly as before.

func TestCartSummaryHidesZeroDeliveryFee(t *testing.T) {
	rr := newTestRenderer(t)
	lines := []CartLineView{{VariantID: "v1", ProductName: "Air Max", Size: "42", Color: "Черный", Qty: 2, UnitPrice: 5000, LineTotal: 10000}}

	cases := []struct {
		name      string
		page      CartPageData
		wantRow   string // "" = no delivery row at all
		wantTotal string
	}{
		{"flat fee 0", CartPageData{ItemsTotal: 10000, DeliveryFee: 0, GrandTotal: 10000},
			"", `<span>Итого</span><span>10 000 сом</span>`},
		{"cheapest zone free, zones differ", CartPageData{ItemsTotal: 10000, DeliveryFee: 0, GrandTotal: 10000, DeliveryFrom: true},
			"", `<span>Итого</span><span>10 000 сом</span>`},
		{"flat fee 200", CartPageData{ItemsTotal: 10000, DeliveryFee: 200, GrandTotal: 10200},
			`<span>Доставка</span><span>200 сом</span>`, `<span>Итого</span><span>10 200 сом</span>`},
		{"zones from 150", CartPageData{ItemsTotal: 10000, DeliveryFee: 150, GrandTotal: 10150, DeliveryFrom: true},
			`<span>Доставка</span><span>от 150 сом</span>`, `<span>Итого</span><span>от 10 150 сом</span>`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			page := c.page
			page.Lines = lines
			w := httptest.NewRecorder()
			if err := rr.Render(w, "cart", PageData{Lang: i18n.LangRU, Screen: "cart", Authed: true, Data: &page}); err != nil {
				t.Fatalf("Render: %v", err)
			}
			body := w.Body.String()
			hasRow := strings.Contains(body, `<span>Доставка</span>`)
			switch {
			case c.wantRow == "" && hasRow:
				t.Errorf("cart shows a delivery row for a zero fee")
			case c.wantRow != "" && !strings.Contains(body, c.wantRow):
				t.Errorf("cart missing delivery row %q", c.wantRow)
			}
			if strings.Contains(body, "от 0 сом") || strings.Contains(body, "<span>0 сом</span>") {
				t.Errorf("cart prints a zero delivery amount")
			}
			if !strings.Contains(body, c.wantTotal) {
				t.Errorf("cart missing total %q", c.wantTotal)
			}
		})
	}
}

var checkoutFeeRowRe = regexp.MustCompile(`<div class="checkout-summary__row" id="checkout-fee-row"( hidden)?><span>Доставка</span><span id="checkout-fee">([^<]*)</span></div>`)

func TestCheckoutSummaryHidesZeroDeliveryFee(t *testing.T) {
	rr := newTestRenderer(t)
	addr := []AddressView{{ID: "a1", Label: "Дом", AddressText: "ул. Ленина 1", IsDefault: true}}
	points := []PointView{{ID: "p1", Name: "Cozy Bishkek Park", Address: "ул. Чуй 1"}}

	cases := []struct {
		name       string
		data       CheckoutPageData
		wantHidden bool
		wantFee    string
		wantTotal  string
	}{
		{"flat fee 0, delivery preselected", CheckoutPageData{Addresses: addr, Points: points, ItemsTotal: 5000, ItemCount: 1, DeliveryFee: 0},
			true, "0 сом", `<span id="checkout-total">5 000 сом</span>`},
		{"free zone preselected", CheckoutPageData{Addresses: addr, Points: points, ItemsTotal: 5000, ItemCount: 1,
			Zones: []CheckoutZoneView{{ID: "z1", Name: "Центр", Fee: 0, Total: 5000}, {ID: "z2", Name: "Пригород", Fee: 300, Total: 5300}}},
			true, "0 сом", `<span id="checkout-total">5 000 сом</span>`},
		{"pickup only", CheckoutPageData{Points: points, ItemsTotal: 5000, ItemCount: 1, DeliveryFee: 200},
			true, "0 сом", `<span id="checkout-total">5 000 сом</span>`},
		{"flat fee 200", CheckoutPageData{Addresses: addr, Points: points, ItemsTotal: 5000, ItemCount: 1, DeliveryFee: 200},
			false, "200 сом", `<span id="checkout-total">5 200 сом</span>`},
		{"paid zone preselected", CheckoutPageData{Addresses: addr, Points: points, ItemsTotal: 5000, ItemCount: 1,
			Zones: []CheckoutZoneView{{ID: "z2", Name: "Пригород", Fee: 300, Total: 5300}}},
			false, "300 сом", `<span id="checkout-total">5 300 сом</span>`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			data := PageData{Lang: i18n.LangRU, Screen: "checkout", Authed: true, Data: c.data}
			if err := rr.Render(w, "checkout", data); err != nil {
				t.Fatalf("Render: %v", err)
			}
			body := w.Body.String()
			m := checkoutFeeRowRe.FindStringSubmatch(body)
			if m == nil {
				t.Fatalf("checkout delivery row not found")
			}
			if hidden := m[1] != ""; hidden != c.wantHidden {
				t.Errorf("delivery row hidden = %v, want %v", hidden, c.wantHidden)
			}
			if m[2] != c.wantFee {
				t.Errorf("delivery fee = %q, want %q", m[2], c.wantFee)
			}
			if !strings.Contains(body, c.wantTotal) {
				t.Errorf("checkout missing total %q", c.wantTotal)
			}
		})
	}
}

func TestProductCourierLineHidesZeroDeliveryFee(t *testing.T) {
	rr := newTestRenderer(t)
	const courier = "Доставка по Бишкеку за 1 день"

	cases := []struct {
		name string
		fee  float64
		from bool
		want string
	}{
		{"flat fee 0", 0, false, courier + "</div>"},
		{"cheapest zone free", 0, true, courier + "</div>"},
		{"flat fee 200", 200, false, courier + " — 200 сом</div>"},
		{"zones from 150", 150, true, courier + " — от 150 сом</div>"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pd := &ProductData{Name: "Кроссовки Air", PriceText: "4 500 сом", InStock: true, ProductPath: "/product/x", DeliveryFee: c.fee, DeliveryFrom: c.from}
			w := httptest.NewRecorder()
			if err := rr.Render(w, "product", PageData{Lang: i18n.LangRU, Screen: "product", Data: pd}); err != nil {
				t.Fatalf("Render: %v", err)
			}
			if body := w.Body.String(); !strings.Contains(body, c.want) {
				t.Errorf("product page missing %q", c.want)
			}
		})
	}
}
