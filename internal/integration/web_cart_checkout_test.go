//go:build integration

package integration

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/payments"
	"github.com/Nikemas/cozy_backend/internal/web"
)

// TestWebGuestIsSentToLogin: every customer-only screen redirects a guest
// to /profile, and every HTMX mutation answers 401.
func TestWebGuestIsSentToLogin(t *testing.T) {
	t.Parallel()
	a := app(t)
	for _, path := range []string{"/cart", "/checkout", "/favorites", "/addresses", "/order/X-1/done"} {
		t.Run("GET "+path, func(t *testing.T) {
			wantRedirect(t, a.get(t, path), "/profile")
		})
	}
	for _, path := range []string{"/checkout", "/orders/x/repeat", "/orders/x/cancel", "/pay/x/retry", "/pay/x/qr"} {
		t.Run("POST "+path, func(t *testing.T) {
			wantRedirect(t, a.do(t, req{method: http.MethodPost, path: path}), "/profile")
		})
	}
	for _, r := range []req{
		{method: http.MethodPost, path: "/cart/items"},
		{method: http.MethodPost, path: "/cart/items/x/increment"},
		{method: http.MethodPost, path: "/favorites/x"},
		{method: http.MethodDelete, path: "/favorites/x"},
		{method: http.MethodPost, path: "/favorites/x/cart"},
		{method: http.MethodGet, path: "/addresses/new"},
		{method: http.MethodPost, path: "/addresses"},
		{method: http.MethodPost, path: "/login/name"},
	} {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			wantStatus(t, a.do(t, r), http.StatusUnauthorized)
		})
	}

	// The orders screen is public but empty for a guest.
	wantStatus(t, a.get(t, "/orders"), http.StatusOK)
}

func TestWebCartAddAdjustAndRemove(t *testing.T) {
	t.Parallel()
	a := app(t)
	f := newFixture(t, 5, 5)
	c := []*http.Cookie{a.customerSession(t, f.CustomerID)}

	// Empty cart page.
	w := a.get(t, "/cart", c...)
	wantStatus(t, w, http.StatusOK)

	// Plain form add redirects to /cart; HTMX add returns the toast.
	w = a.do(t, req{method: http.MethodPost, path: "/cart/items", form: url.Values{"variant_id": {f.VariantA}, "qty": {"2"}}, cookies: c})
	wantRedirect(t, w, "/cart")
	w = a.do(t, req{method: http.MethodPost, path: "/cart/items", form: url.Values{"variant_id": {f.VariantB}}, cookies: c, htmx: true})
	wantStatus(t, w, http.StatusOK)
	if cartQty(t, f.CustomerID, f.VariantA) != 2 || cartQty(t, f.CustomerID, f.VariantB) != 1 {
		t.Fatalf("cart = A:%d B:%d, want 2/1", cartQty(t, f.CustomerID, f.VariantA), cartQty(t, f.CustomerID, f.VariantB))
	}

	for _, bad := range []string{"0", "-1", "abc"} {
		w = a.do(t, req{method: http.MethodPost, path: "/cart/items", form: url.Values{"variant_id": {f.VariantA}, "qty": {bad}}, cookies: c})
		wantStatus(t, w, http.StatusBadRequest)
	}

	// The full page lists both lines with the grand total (2×2500 + 3000).
	w = a.get(t, "/cart", c...)
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, productName(t, f.ProductID), "8 000")

	w = a.do(t, req{method: http.MethodPost, path: "/cart/items/" + f.VariantA + "/increment", cookies: c, htmx: true})
	wantStatus(t, w, http.StatusOK)
	if got := cartQty(t, f.CustomerID, f.VariantA); got != 3 {
		t.Fatalf("after increment qty = %d, want 3", got)
	}
	w = a.do(t, req{method: http.MethodPost, path: "/cart/items/" + f.VariantB + "/decrement", cookies: c, htmx: true})
	wantStatus(t, w, http.StatusOK)
	if got := cartQty(t, f.CustomerID, f.VariantB); got != 0 {
		t.Fatalf("decrement to zero must remove the line, qty = %d", got)
	}
}

func TestWebProductPageActions(t *testing.T) {
	t.Parallel()
	a := app(t)
	f := newFixture(t, 5, 5)
	c := []*http.Cookie{a.customerSession(t, f.CustomerID)}
	path := web.ProductPath(f.ProductID, productName(t, f.ProductID))

	// Guests are sent to log in (HX-Redirect for HTMX, 303 otherwise).
	wantRedirect(t, a.do(t, req{method: http.MethodPost, path: path + "/cart"}), "/profile")
	w := a.do(t, req{method: http.MethodPost, path: path + "/buy", htmx: true})
	if w.Header().Get("HX-Redirect") != "/profile" {
		t.Fatalf("HX-Redirect = %q, want /profile", w.Header().Get("HX-Redirect"))
	}

	cases := []struct {
		name string
		form url.Values
		want int
	}{
		{"missing selection", url.Values{"size": {"40"}}, http.StatusBadRequest},
		{"unknown size", url.Values{"size": {"47"}, "color": {"black"}}, http.StatusNotFound},
		{"ok", url.Values{"size": {"40"}, "color": {"black"}}, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantStatus(t, a.do(t, req{method: http.MethodPost, path: path + "/cart", form: tc.form, cookies: c, htmx: true}), tc.want)
		})
	}
	if got := cartQty(t, f.CustomerID, f.VariantA); got != 1 {
		t.Fatalf("cart qty = %d, want 1", got)
	}

	// Buy now doesn't add a second unit of a variant already in the cart.
	w = a.do(t, req{method: http.MethodPost, path: path + "/buy", form: url.Values{"size": {"40"}, "color": {"black"}}, cookies: c})
	wantRedirect(t, w, "/checkout")
	if got := cartQty(t, f.CustomerID, f.VariantA); got != 1 {
		t.Fatalf("buy-now must not duplicate the line, qty = %d", got)
	}
	w = a.do(t, req{method: http.MethodPost, path: path + "/buy", form: url.Values{"size": {"41"}, "color": {"black"}}, cookies: c, htmx: true})
	if w.Header().Get("HX-Redirect") != "/checkout" || cartQty(t, f.CustomerID, f.VariantB) != 1 {
		t.Fatalf("HTMX buy-now: HX-Redirect=%q qtyB=%d", w.Header().Get("HX-Redirect"), cartQty(t, f.CustomerID, f.VariantB))
	}

	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/product/bogus/cart", cookies: c}), http.StatusNotFound)
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/product/bogus/buy", cookies: c}), http.StatusNotFound)
}

func TestWebCheckoutPickupDeliveryAndReplay(t *testing.T) {
	t.Parallel()
	a := app(t)
	f := newFixture(t, 10, 10)
	c := []*http.Cookie{a.customerSession(t, f.CustomerID)}
	addToCart := func(variantID string) {
		t.Helper()
		wantRedirect(t, a.do(t, req{method: http.MethodPost, path: "/cart/items", form: url.Values{"variant_id": {variantID}}, cookies: c}), "/cart")
	}

	// Empty cart: the form sends back to /cart.
	wantRedirect(t, a.get(t, "/checkout", c...), "/cart")

	addToCart(f.VariantA)
	w := a.get(t, "/checkout", c...)
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, "г. Бишкек, ул. Тестовая, 10", "Точка A")

	invalid := []url.Values{
		{},
		{"fulfillment": {"delivery"}},
		{"fulfillment": {"pickup"}},
		{"fulfillment": {"pickup"}, "point_id": {f.PointA}, "payment_method": {"barter"}},
	}
	for _, form := range invalid {
		wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/checkout", form: form, cookies: c}), http.StatusBadRequest)
	}

	// Pickup, cash: lands on the done page naming the pickup point.
	key := "it-web-checkout-" + f.CustomerID
	w = a.do(t, req{method: http.MethodPost, path: "/checkout", cookies: c, form: url.Values{
		"fulfillment": {"pickup"}, "point_id": {f.PointA}, "idempotency_key": {key}, "comment": {"позвонить"},
	}})
	wantStatus(t, w, http.StatusSeeOther)
	done := w.Header().Get("Location")
	if !strings.HasPrefix(done, "/order/") || !strings.HasSuffix(done, "/done") {
		t.Fatalf("Location = %q, want /order/{n}/done", done)
	}
	w = a.get(t, done, c...)
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, "Точка A")

	// A double submit of the same form (cart now empty) replays to the same order.
	w = a.do(t, req{method: http.MethodPost, path: "/checkout", cookies: c, form: url.Values{
		"fulfillment": {"pickup"}, "point_id": {f.PointA}, "idempotency_key": {key},
	}})
	wantRedirect(t, w, done)
	// Without a known key an empty cart is an error.
	w = a.do(t, req{method: http.MethodPost, path: "/checkout", cookies: c, form: url.Values{
		"fulfillment": {"pickup"}, "point_id": {f.PointA}, "idempotency_key": {"unknown-key"},
	}})
	wantStatus(t, w, http.StatusBadRequest)

	// Delivery to the saved address.
	addToCart(f.VariantB)
	w = a.do(t, req{method: http.MethodPost, path: "/checkout", cookies: c, form: url.Values{
		"fulfillment": {"delivery"}, "address_id": {f.AddressID}, "payment_method": {"cash_on_delivery"},
	}})
	wantStatus(t, w, http.StatusSeeOther)
	w = a.get(t, w.Header().Get("Location"), c...)
	wantStatus(t, w, http.StatusOK)

	// Someone else's order number is a 404.
	other := a.customerSession(t, newCustomer(t))
	wantStatus(t, a.get(t, done, other), http.StatusNotFound)

	// Both orders show on /orders, with photo-less items and status labels.
	w = a.get(t, "/orders?repeat=added", c...)
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, strings.TrimSuffix(strings.TrimPrefix(done, "/order/"), "/done"))
}

func TestWebOrdersCancelAndRepeat(t *testing.T) {
	t.Parallel()
	a := app(t)
	f := newFixture(t, 10, 10)
	c := []*http.Cookie{a.customerSession(t, f.CustomerID)}
	wantRedirect(t, a.do(t, req{method: http.MethodPost, path: "/cart/items", form: url.Values{"variant_id": {f.VariantA}, "qty": {"2"}}, cookies: c}), "/cart")
	w := a.do(t, req{method: http.MethodPost, path: "/checkout", cookies: c, form: url.Values{"fulfillment": {"pickup"}, "point_id": {f.PointA}}})
	wantStatus(t, w, http.StatusSeeOther)
	var orderID string
	if err := testDB.QueryRowContext(ctxT(t), `SELECT id FROM orders WHERE customer_id = $1`, f.CustomerID).Scan(&orderID); err != nil {
		t.Fatal(err)
	}

	// Cancel a placed cash order: stock returns, second cancel fails softly.
	wantRedirect(t, a.do(t, req{method: http.MethodPost, path: "/orders/" + orderID + "/cancel", cookies: c}), "/orders?cancel=done")
	if got := stockQty(t, f.VariantA, f.PointA); got != 10 {
		t.Fatalf("stock after cancel = %d, want 10", got)
	}
	wantRedirect(t, a.do(t, req{method: http.MethodPost, path: "/orders/" + orderID + "/cancel", cookies: c}), "/orders?cancel=failed")
	for _, q := range []string{"?cancel=done", "?cancel=failed", "?repeat=soon"} {
		wantStatus(t, a.get(t, "/orders"+q, c...), http.StatusOK)
	}

	// Repeat puts the same items back into the cart.
	wantRedirect(t, a.do(t, req{method: http.MethodPost, path: "/orders/" + orderID + "/repeat", cookies: c}), "/orders?repeat=added")
	if got := cartQty(t, f.CustomerID, f.VariantA); got != 2 {
		t.Fatalf("repeat cart qty = %d, want 2", got)
	}
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/orders/00000000-0000-0000-0000-000000000000/repeat", cookies: c}), http.StatusNotFound)
}

func TestWebOnlinePaymentPages(t *testing.T) {
	t.Parallel()
	a := app(t)
	f := newFixture(t, 10, 10)
	c := []*http.Cookie{a.customerSession(t, f.CustomerID)}
	wantRedirect(t, a.do(t, req{method: http.MethodPost, path: "/cart/items", form: url.Values{"variant_id": {f.VariantA}}, cookies: c}), "/cart")

	// Online card at checkout sends the customer to the (mock) bank page.
	w := a.do(t, req{method: http.MethodPost, path: "/checkout", cookies: c, form: url.Values{
		"fulfillment": {"pickup"}, "point_id": {f.PointA}, "payment_method": {"online_card"},
	}})
	wantStatus(t, w, http.StatusSeeOther)
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, "https://cozy.test"+payments.MockCheckoutPath) {
		t.Fatalf("Location = %q, want the mock checkout page", loc)
	}
	var orderID string
	if err := testDB.QueryRowContext(ctxT(t), `SELECT id FROM orders WHERE customer_id = $1`, f.CustomerID).Scan(&orderID); err != nil {
		t.Fatal(err)
	}

	// The return page and its poll fragment show the pending state.
	w = a.get(t, payments.ReturnPath(orderID), c...)
	wantStatus(t, w, http.StatusOK)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", w.Header().Get("Cache-Control"))
	}
	wantStatus(t, a.get(t, payments.ReturnPath(orderID)+"/status?n=3", c...), http.StatusOK)
	wantStatus(t, a.get(t, payments.ReturnPath(orderID)+"/status?n=-5", c...), http.StatusOK)
	// Anonymous (app customer without a site session) and bad ids still render.
	wantStatus(t, a.get(t, payments.ReturnPath(orderID)), http.StatusOK)
	wantStatus(t, a.get(t, "/pay/return/not-a-uuid", c...), http.StatusOK)

	// Retry opens a new attempt on the mock page.
	w = a.do(t, req{method: http.MethodPost, path: "/pay/" + orderID + "/retry", cookies: c})
	wantStatus(t, w, http.StatusSeeOther)
	if loc := w.Header().Get("Location"); !strings.Contains(loc, payments.MockCheckoutPath) {
		t.Fatalf("retry Location = %q", loc)
	}
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/pay/not-a-uuid/retry", cookies: c}), http.StatusNotFound)
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/pay/not-a-uuid/qr", cookies: c}), http.StatusNotFound)
	// The mock provider has no QR: the fragment shows the error in place.
	w = a.do(t, req{method: http.MethodPost, path: "/pay/" + orderID + "/qr", cookies: c, htmx: true})
	wantStatus(t, w, http.StatusOK)

	// Once cancelled, retry can't pay any more: back to the result page.
	wantRedirect(t, a.do(t, req{method: http.MethodPost, path: "/orders/" + orderID + "/cancel", cookies: c}), "/orders?cancel=done")
	wantRedirect(t, a.do(t, req{method: http.MethodPost, path: "/pay/" + orderID + "/retry", cookies: c}), payments.ReturnPath(orderID))

	// A cash order's return URL goes to its done page.
	wantRedirect(t, a.do(t, req{method: http.MethodPost, path: "/cart/items", form: url.Values{"variant_id": {f.VariantB}}, cookies: c}), "/cart")
	w = a.do(t, req{method: http.MethodPost, path: "/checkout", cookies: c, form: url.Values{"fulfillment": {"pickup"}, "point_id": {f.PointA}}})
	wantStatus(t, w, http.StatusSeeOther)
	var cashID, cashNumber string
	if err := testDB.QueryRowContext(ctxT(t), `SELECT id, order_number FROM orders WHERE customer_id = $1 AND payment_method = 'cash_on_delivery'`, f.CustomerID).Scan(&cashID, &cashNumber); err != nil {
		t.Fatal(err)
	}
	wantRedirect(t, a.get(t, payments.ReturnPath(cashID), c...), "/order/"+cashNumber+"/done")
}
