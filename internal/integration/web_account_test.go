//go:build integration

package integration

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func favoriteExists(t *testing.T, customerID, productID string) bool {
	t.Helper()
	var ok bool
	err := testDB.QueryRowContext(ctxT(t),
		`SELECT EXISTS (SELECT 1 FROM favorites WHERE customer_id = $1 AND product_id = $2)`, customerID, productID).Scan(&ok)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestWebFavoritesAddListCartRemove(t *testing.T) {
	t.Parallel()
	a := app(t)
	f := newFixture(t, 3, 3)
	addProductImage(t, f.ProductID, "products/"+f.ProductID+"/fav.jpg")
	c := []*http.Cookie{a.customerSession(t, f.CustomerID)}

	w := a.do(t, req{method: http.MethodPost, path: "/favorites/" + f.ProductID, cookies: c, htmx: true})
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, `id="toast-slot"`)
	if !favoriteExists(t, f.CustomerID, f.ProductID) {
		t.Fatal("favorite not stored")
	}

	for _, lang := range []string{"ru", "ky"} {
		w = a.get(t, "/favorites?lang="+lang, c...)
		wantStatus(t, w, http.StatusOK)
		wantBody(t, w, f.ProductID, "https://media.cozy.test/cozy/products/"+f.ProductID+"/fav.jpg")
	}

	// "В корзину" from favorites adds the first variant.
	w = a.do(t, req{method: http.MethodPost, path: "/favorites/" + f.ProductID + "/cart", cookies: c, htmx: true})
	wantStatus(t, w, http.StatusOK)
	if cartQty(t, f.CustomerID, f.VariantA)+cartQty(t, f.CustomerID, f.VariantB) != 1 {
		t.Fatal("favorites add-to-cart should add exactly one unit")
	}

	// A product without variants yields a toast, not an error.
	var bare string
	if err := testDB.QueryRowContext(ctxT(t),
		`INSERT INTO products (category_id, name_ru, name_ky, base_price) VALUES ($1, 'Без вариантов', 'Без вариантов', 100) RETURNING id`,
		f.CategoryID).Scan(&bare); err != nil {
		t.Fatal(err)
	}
	w = a.do(t, req{method: http.MethodPost, path: "/favorites/" + bare + "/cart", cookies: c, htmx: true})
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, `id="toast-slot"`)

	w = a.do(t, req{method: http.MethodDelete, path: "/favorites/" + f.ProductID, cookies: c, htmx: true})
	wantStatus(t, w, http.StatusOK)
	if favoriteExists(t, f.CustomerID, f.ProductID) {
		t.Fatal("favorite not removed")
	}
	if strings.Contains(w.Body.String(), "<html") {
		t.Fatal("remove must render the grid fragment only")
	}
}

func TestWebAddressesCRUD(t *testing.T) {
	t.Parallel()
	a := app(t)
	f := newFixture(t, 1, 1)
	c := []*http.Cookie{a.customerSession(t, f.CustomerID)}

	w := a.get(t, "/addresses", c...)
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, "г. Бишкек, ул. Тестовая, 10")

	for _, p := range []string{"/addresses/new", "/addresses/cancel", "/addresses/" + f.AddressID + "/edit"} {
		w = a.do(t, req{method: http.MethodGet, path: p, cookies: c, htmx: true})
		wantStatus(t, w, http.StatusOK)
	}
	wantStatus(t, a.do(t, req{method: http.MethodGet, path: "/addresses/00000000-0000-0000-0000-000000000000/edit", cookies: c}), http.StatusNotFound)

	// Create: an empty address text re-renders the form with an error and stores nothing.
	count := func() int {
		var n int
		if err := testDB.QueryRowContext(ctxT(t), `SELECT count(*) FROM customer_addresses WHERE customer_id = $1`, f.CustomerID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	w = a.do(t, req{method: http.MethodPost, path: "/addresses", cookies: c, htmx: true, form: url.Values{"label": {"Офис"}, "address_text": {"  "}}})
	wantStatus(t, w, http.StatusOK)
	if count() != 1 {
		t.Fatalf("invalid address was stored")
	}
	w = a.do(t, req{method: http.MethodPost, path: "/addresses", cookies: c, htmx: true, form: url.Values{
		"label": {"Офис"}, "address_text": {"ул. Киевская, 5"}, "is_default": {"on"},
	}})
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, "ул. Киевская, 5", `id="toast-slot"`)
	if count() != 2 {
		t.Fatalf("address count = %d, want 2", count())
	}
	var newID string
	var isDefault bool
	if err := testDB.QueryRowContext(ctxT(t),
		`SELECT id, is_default FROM customer_addresses WHERE customer_id = $1 AND address_text = 'ул. Киевская, 5'`, f.CustomerID).Scan(&newID, &isDefault); err != nil {
		t.Fatal(err)
	}
	if !isDefault {
		t.Fatal("new address should be the default")
	}

	// Update: invalid then valid.
	w = a.do(t, req{method: http.MethodPost, path: "/addresses/" + newID, cookies: c, htmx: true, form: url.Values{"address_text": {""}}})
	wantStatus(t, w, http.StatusOK)
	w = a.do(t, req{method: http.MethodPost, path: "/addresses/" + newID, cookies: c, htmx: true, form: url.Values{"label": {"Работа"}, "address_text": {"ул. Киевская, 7"}}})
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, "ул. Киевская, 7")

	// Another customer can't touch it.
	other := a.customerSession(t, newCustomer(t))
	wantStatus(t, a.do(t, req{method: http.MethodDelete, path: "/addresses/" + newID, cookies: []*http.Cookie{other}}), http.StatusNotFound)

	w = a.do(t, req{method: http.MethodDelete, path: "/addresses/" + newID, cookies: c, htmx: true})
	wantStatus(t, w, http.StatusOK)
	if count() != 1 {
		t.Fatalf("address count after delete = %d, want 1", count())
	}
}

func TestWebLoginFlowNewCustomer(t *testing.T) {
	t.Parallel()
	a := app(t)
	phone := uniquePhone()
	local := "0" + strings.TrimPrefix(phone, "+996")

	// Bad phone: inline error on the phone step.
	w := a.do(t, req{method: http.MethodPost, path: "/login/otp/request", form: url.Values{"phone": {"12"}}, htmx: true})
	wantStatus(t, w, http.StatusOK)

	// Local spelling is canonicalised into the OTP step.
	w = a.do(t, req{method: http.MethodPost, path: "/login/otp/request", form: url.Values{"phone": {local}}, htmx: true})
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, `name="phone" value="&#43;`+strings.TrimPrefix(phone, "+"))

	// Wrong code: stays on the otp step, no cookie.
	w = a.do(t, req{method: http.MethodPost, path: "/login/otp/verify", form: url.Values{"phone": {phone}, "code": {"1111"}}})
	wantStatus(t, w, http.StatusOK)
	if strings.Contains(w.Header().Get("Set-Cookie"), "cozy_session=") {
		t.Fatal("wrong code must not set a session")
	}

	// Right code for a brand-new phone: cookie set, name step shown.
	w = a.do(t, req{method: http.MethodPost, path: "/login/otp/verify", form: url.Values{"phone": {phone}, "code": {"0000"}}, htmx: true})
	wantStatus(t, w, http.StatusOK)
	var session *http.Cookie
	for _, ck := range w.Result().Cookies() {
		if ck.Name == "cozy_session" {
			session = ck
		}
	}
	if session == nil || session.Value == "" || !session.HttpOnly {
		t.Fatalf("session cookie = %+v, want httpOnly with a token", session)
	}
	wantBody(t, w, `name="name"`)

	// Blank name is rejected inline; a real one completes onboarding.
	w = a.do(t, req{method: http.MethodPost, path: "/login/name", form: url.Values{"name": {"  "}}, cookies: []*http.Cookie{session}})
	wantStatus(t, w, http.StatusOK)
	w = a.do(t, req{method: http.MethodPost, path: "/login/name", form: url.Values{"name": {"Айбек"}}, cookies: []*http.Cookie{session}, htmx: true})
	if w.Header().Get("HX-Redirect") != "/profile" {
		t.Fatalf("HX-Redirect = %q, want /profile", w.Header().Get("HX-Redirect"))
	}

	w = a.get(t, "/profile", session)
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, "Айбек")

	// A returning customer (name on file) goes straight to /profile.
	f := newFixture(t, 1, 1)
	var known string
	if err := testDB.QueryRowContext(ctxT(t), `SELECT phone FROM customers WHERE id = $1`, f.CustomerID).Scan(&known); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/login/otp/request", form: url.Values{"phone": {known}}}), http.StatusOK)
	w = a.do(t, req{method: http.MethodPost, path: "/login/otp/verify", form: url.Values{"phone": {known}, "code": {"0000"}}})
	wantRedirect(t, w, "/profile")

	wantRedirect(t, a.do(t, req{method: http.MethodPost, path: "/logout", cookies: []*http.Cookie{session}}), "/")

	// Guest profile shows the requested login step.
	w = a.get(t, "/profile?step=otp&phone="+url.QueryEscape(phone))
	wantStatus(t, w, http.StatusOK)
}

func TestWebAccountDeletion(t *testing.T) {
	t.Parallel()
	a := app(t)
	f := newFixture(t, 5, 5)
	c := []*http.Cookie{a.customerSession(t, f.CustomerID)}

	wantRedirect(t, a.do(t, req{method: http.MethodPost, path: "/account/delete"}), "/profile")
	wantRedirect(t, a.do(t, req{method: http.MethodPost, path: "/account/delete", cookies: c, form: url.Values{}}),
		"/profile?delete=unconfirmed#account-delete")

	// An open order blocks deletion.
	wantRedirect(t, a.do(t, req{method: http.MethodPost, path: "/cart/items", form: url.Values{"variant_id": {f.VariantA}}, cookies: c}), "/cart")
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/checkout", cookies: c, form: url.Values{"fulfillment": {"pickup"}, "point_id": {f.PointA}}}), http.StatusSeeOther)
	wantRedirect(t, a.do(t, req{method: http.MethodPost, path: "/account/delete", cookies: c, form: url.Values{"confirm": {"yes"}}}),
		"/profile?delete=active_orders#account-delete")
	for _, st := range []string{"active_orders", "unconfirmed", "other"} {
		wantStatus(t, a.get(t, "/profile?delete="+st, c...), http.StatusOK)
	}

	// Without open orders the account is deleted and the cookie cleared.
	if _, err := testDB.ExecContext(ctxT(t), `UPDATE orders SET status = 'delivered' WHERE customer_id = $1`, f.CustomerID); err != nil {
		t.Fatal(err)
	}
	w := a.do(t, req{method: http.MethodPost, path: "/account/delete", cookies: c, form: url.Values{"confirm": {"yes"}}})
	wantRedirect(t, w, "/account-deletion?deleted=1")
	var exists bool
	if err := testDB.QueryRowContext(ctxT(t), `SELECT EXISTS (SELECT 1 FROM customers WHERE id = $1 AND phone NOT LIKE 'deleted%')`, f.CustomerID).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("customer should be deleted or anonymised")
	}
	wantStatus(t, a.get(t, "/account-deletion?deleted=1"), http.StatusOK)
}
