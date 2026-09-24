package web

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/i18n"
)

// newTestRenderer parses the real templates from the repo root — see
// repoRoot/chdir in render_test.go — so these tests exercise the exact
// .gohtml files that ship, not a copy.
func newTestRenderer(t *testing.T) *Renderer {
	t.Helper()
	restore := chdir(t, repoRoot(t))
	defer restore()

	bundle, err := i18n.Load("locales")
	if err != nil {
		t.Fatalf("i18n.Load: %v", err)
	}
	rr, err := NewRenderer(bundle)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	return rr
}

// TestRenderCartExecutes exercises cart.gohtml's three states (logged
// out, empty cart, cart with lines) end to end — Task 3's acceptance
// criteria call for all three ("пустое состояние с CTA", "степпер qty",
// sticky итог), and a nil CartPageData or a bad field reference in the
// template would panic at Execute time, not at parse time, so
// TestNewRendererParsesAllScreens alone wouldn't catch it.
func TestRenderCartExecutes(t *testing.T) {
	rr := newTestRenderer(t)

	cases := []struct {
		name string
		data PageData
	}{
		{"logged out", PageData{Lang: i18n.LangRU, Screen: "cart", Authed: false}},
		{"empty cart", PageData{Lang: i18n.LangRU, Screen: "cart", Authed: true, Data: &CartPageData{Lines: []CartLineView{}}}},
		{"cart with lines", PageData{Lang: i18n.LangRU, Screen: "cart", Authed: true, Data: &CartPageData{
			Lines: []CartLineView{
				{VariantID: "v1", ProductName: "Air Max", Size: "42", Color: "Черный", Qty: 2, UnitPrice: 5000, LineTotal: 10000},
			},
			ItemsTotal:  10000,
			DeliveryFee: 200,
			GrandTotal:  10200,
		}}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			if err := rr.Render(w, "cart", c.data); err != nil {
				t.Fatalf("Render: %v", err)
			}
			if w.Code != 200 {
				t.Fatalf("status = %d, want 200", w.Code)
			}
		})
	}
}

// TestRenderCartFragmentExecutes exercises the HTMX partial path
// (Renderer.RenderPartial rendering just "cart_page") the stepper
// increment/decrement handlers use — a separate code path from the
// full-page Render above, and the one that must not accidentally pull in
// (or fail without) the layout/header/footer.
func TestRenderCartFragmentExecutes(t *testing.T) {
	rr := newTestRenderer(t)
	data := PageData{Lang: i18n.LangRU, Screen: "cart", Authed: true, Data: &CartPageData{
		Lines: []CartLineView{
			{VariantID: "v1", ProductName: "Air Max", Size: "42", Color: "Черный", Qty: 1, UnitPrice: 5000, LineTotal: 5000},
		},
		ItemsTotal:  5000,
		DeliveryFee: 200,
		GrandTotal:  5200,
	}}

	w := httptest.NewRecorder()
	if err := rr.RenderPartial(w, "cart", "cart_page", data); err != nil {
		t.Fatalf("RenderPartial: %v", err)
	}
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

// TestRenderCheckoutExecutes covers the three shapes checkoutForm can
// hand the template: both delivery and pickup available, pickup-only (no
// saved addresses yet — Task 4 owns that CRUD), and neither available.
func TestRenderCheckoutExecutes(t *testing.T) {
	rr := newTestRenderer(t)

	cases := []struct {
		name string
		data CheckoutPageData
	}{
		{"delivery and pickup", CheckoutPageData{
			Addresses:  []AddressView{{ID: "a1", Label: "Дом", AddressText: "ул. Ленина 1", IsDefault: true}},
			Points:     []PointView{{ID: "p1", Name: "Cozy Bishkek Park", Address: "ул. Чуй 1"}},
			ItemsTotal: 5000,
			ItemCount:  2,
		}},
		{"pickup only", CheckoutPageData{
			Points:     []PointView{{ID: "p1", Name: "Cozy Bishkek Park", Address: "ул. Чуй 1"}},
			ItemsTotal: 5000,
			ItemCount:  1,
		}},
		{"nothing available", CheckoutPageData{ItemsTotal: 5000, ItemCount: 1}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			data := PageData{Lang: i18n.LangRU, Screen: "checkout", Authed: true, Data: c.data}
			if err := rr.Render(w, "checkout", data); err != nil {
				t.Fatalf("Render: %v", err)
			}
			if w.Code != 200 {
				t.Fatalf("status = %d, want 200", w.Code)
			}
		})
	}
}

// TestRenderDoneExecutes covers done.gohtml with a real order summary.
func TestRenderDoneExecutes(t *testing.T) {
	rr := newTestRenderer(t)
	w := httptest.NewRecorder()
	data := PageData{Lang: i18n.LangRU, Screen: "done", Authed: true, Data: DoneData{OrderNumber: "COZY-20260915-001", Total: 10200}}
	if err := rr.Render(w, "done", data); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestPluralForm(t *testing.T) {
	cases := map[int]string{0: "many", 1: "one", 2: "few", 4: "few", 5: "many", 11: "many", 12: "many", 14: "many", 21: "one", 22: "few", 25: "many", 101: "one", 111: "many"}
	for n, want := range cases {
		if got := pluralForm(n); got != want {
			t.Errorf("pluralForm(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestRenderCartShowsPhotosAndLocalizedMoney: cart lines show the product
// photo (not the shoe placeholder) and amounts go through {{money}}.
func TestRenderCartShowsPhotosAndLocalizedMoney(t *testing.T) {
	rr := newTestRenderer(t)
	data := PageData{Lang: i18n.LangKY, Screen: "cart", Authed: true, Data: &CartPageData{
		Lines: []CartLineView{
			{VariantID: "v1", ProductName: "Air", Size: "42", Color: "Ак", Qty: 2, UnitPrice: 4500, LineTotal: 9000, PhotoURL: "https://m/v1/thumb.jpg"},
			{VariantID: "v2", ProductName: "Boot", Size: "40", Color: "Кара", Qty: 1, UnitPrice: 12000, LineTotal: 12000},
		},
		ItemsTotal: 21000, DeliveryFee: 200, GrandTotal: 21200,
	}}
	w := httptest.NewRecorder()
	if err := rr.Render(w, "cart", data); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := w.Body.String()
	for _, want := range []string{`src="https://m/v1/thumb.jpg"`, "ti-shoe", "9 000 сом", "21 200 сом"} {
		if !strings.Contains(body, want) {
			t.Errorf("cart missing %q", want)
		}
	}
}
