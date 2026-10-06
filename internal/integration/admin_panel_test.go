//go:build integration

package integration

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// placePickupOrder places a cash pickup order for f's customer (1 × VariantA at PointA).
func placePickupOrder(t *testing.T, f fixture) *orders.Order {
	t.Helper()
	o, _, err := orders.NewService(testDB).PlaceOrder(ctxT(t), orders.PlaceOrderInput{
		CustomerID: f.CustomerID, Items: []orders.OrderItemInput{{VariantID: f.VariantA, Quantity: 1}},
		PickupPointID: strptr(f.PointA),
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	return o
}

func TestAdminPanelLoginAndGate(t *testing.T) {
	t.Parallel()
	a := app(t)
	f := newFixture(t, 1, 1)
	local, _ := uniqueKGPhone()
	if _, err := a.staff.CreateStaff(ctxT(t), staff.CreateStaffInput{Phone: local, Password: "panel-pass-1", Name: "Менеджер", Role: staff.RoleManager}); err != nil {
		t.Fatal(err)
	}

	wantStatus(t, a.get(t, "/admin/login?phone=0700"), http.StatusOK)

	// Wrong password re-renders the form with the phone kept.
	w := a.do(t, req{method: http.MethodPost, path: "/admin/login", form: url.Values{"phone": {local}, "password": {"wrong-pass-1"}}})
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, local)

	w = a.do(t, req{method: http.MethodPost, path: "/admin/login", form: url.Values{"phone": {local}, "password": {"panel-pass-1"}}})
	wantRedirect(t, w, "/admin/orders")
	var session *http.Cookie
	for _, ck := range w.Result().Cookies() {
		if ck.Name == staff.SessionCookieName {
			session = ck
		}
	}
	if session == nil {
		t.Fatal("no staff session cookie")
	}

	// Already logged in: the login page forwards to the first allowed screen.
	wantRedirect(t, a.get(t, "/admin/login", session), "/admin/orders")

	// Gate: guests go to the login page; a manager can't open owner screens.
	w = a.get(t, "/admin/orders")
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/admin/login") {
		t.Fatalf("guest /admin/orders: %d %q", w.Code, w.Header().Get("Location"))
	}
	wantRedirect(t, a.get(t, "/admin/staff", session), "/admin/orders")
	wantStatus(t, a.get(t, "/admin/no-access", session), http.StatusOK)

	// point_staff lands on orders scoped to their point.
	ps, _ := a.newStaffSession(t, staff.RolePointStaff, &f.PointA)
	wantStatus(t, a.get(t, "/admin/orders", ps), http.StatusOK)
	wantStatus(t, a.get(t, "/admin/stock", ps), http.StatusOK)

	w = a.do(t, req{method: http.MethodPost, path: "/admin/lang", form: url.Values{"lang": {"ky"}}, cookies: []*http.Cookie{session}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST /admin/lang: %d", w.Code)
	}

	w = a.do(t, req{method: http.MethodPost, path: "/admin/logout", cookies: []*http.Cookie{session}})
	wantRedirect(t, w, "/admin/login")
	if w := a.get(t, "/admin/orders", session); w.Code != http.StatusSeeOther {
		t.Fatalf("revoked session still works: %d", w.Code)
	}
}

func TestAdminPanelProductsScreens(t *testing.T) {
	t.Parallel()
	a := app(t)
	f := newFixture(t, 4, 2)
	addProductImage(t, f.ProductID, "products/"+f.ProductID+"/p_full.jpg")
	mgr, _ := a.newStaffSession(t, staff.RoleManager, nil)
	owner, _ := a.newStaffSession(t, staff.RoleOwner, nil)
	c := []*http.Cookie{mgr}
	slug := categorySlug(t, f.CategoryID)

	for _, q := range []string{"", "?q=" + url.QueryEscape(productName(t, f.ProductID)), "?cat=" + slug, "?cat=" + slug + "&sub=nope",
		"?point=" + f.PointA, "?page=2&page_size=50", "?page_size=7", "?toast=ok"} {
		w := a.get(t, "/admin/products"+q, c...)
		wantStatus(t, w, http.StatusOK)
	}
	w := a.get(t, "/admin/products?q="+url.QueryEscape(productName(t, f.ProductID)), c...)
	wantBody(t, w, f.ProductID)

	wantStatus(t, a.get(t, "/admin/products/new", c...), http.StatusOK)
	wantStatus(t, a.get(t, "/admin/products/import", c...), http.StatusOK)
	w = a.get(t, "/admin/products/"+f.ProductID, c...)
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, productName(t, f.ProductID), "products/"+f.ProductID+"/p_")
	w = a.get(t, "/admin/products/"+uuid.NewString(), c...)
	if w.Code == http.StatusOK && !strings.Contains(w.Body.String(), "toast") {
		t.Fatalf("unknown product edit page: %d", w.Code)
	}

	// Create: invalid (no name/price) re-renders the form; valid saves product + variant + stock.
	w = a.do(t, req{method: http.MethodPost, path: "/admin/products", cookies: c, form: url.Values{"category_id": {f.CategoryID}, "base_price": {"abc"}}})
	wantStatus(t, w, http.StatusOK)
	tag := uuid.NewString()[:8]
	form := url.Values{
		"category_id": {f.CategoryID}, "name_ru": {"Панель " + tag}, "name_ky": {"Панель " + tag},
		"brand": {"Cozy"}, "base_price": {"1990"}, "description_ru": {"Описание"},
		"variant_key": {"n1"}, "variant_id": {""}, "variant_size": {"39"}, "variant_color": {"white"}, "variant_price": {""},
		"qty_n1_" + f.PointA: {"5"},
		"image_object_key":   {"products/new/x_full.jpg"}, "image_color": {"white"},
	}
	w = a.do(t, req{method: http.MethodPost, path: "/admin/products", cookies: c, form: form})
	wantStatus(t, w, http.StatusSeeOther)
	if !strings.HasPrefix(w.Header().Get("Location"), "/admin/products") {
		t.Fatalf("Location = %q", w.Header().Get("Location"))
	}
	var newID, variantID string
	if err := testDB.QueryRowContext(ctxT(t), `SELECT p.id, v.id FROM products p JOIN product_variants v ON v.product_id = p.id WHERE p.name_ru = $1`, "Панель "+tag).Scan(&newID, &variantID); err != nil {
		t.Fatalf("created product not found: %v", err)
	}
	if got := stockQty(t, variantID, f.PointA); got != 5 {
		t.Fatalf("stock of created variant = %d, want 5", got)
	}

	// Update with a stale stock cell: conflict re-renders the form, nothing written.
	upd := url.Values{
		"category_id": {f.CategoryID}, "name_ru": {"Панель " + tag + " v2"}, "name_ky": {"Панель"}, "base_price": {"2100"},
		"variant_key": {variantID}, "variant_id": {variantID}, "variant_size": {"39"}, "variant_color": {"white"}, "variant_price": {"2500"},
		"orig_variant_price_" + variantID:   {""},
		"qty_" + variantID + "_" + f.PointA: {"9"}, "orig_" + variantID + "_" + f.PointA: {"1"},
	}
	w = a.do(t, req{method: http.MethodPost, path: "/admin/products/" + newID, cookies: c, form: upd})
	wantStatus(t, w, http.StatusOK)
	if got := stockQty(t, variantID, f.PointA); got != 5 {
		t.Fatalf("stale form overwrote stock: %d", got)
	}
	upd.Set("orig_"+variantID+"_"+f.PointA, "5")
	w = a.do(t, req{method: http.MethodPost, path: "/admin/products/" + newID, cookies: c, form: upd, htmx: true})
	if w.Header().Get("HX-Redirect") == "" {
		t.Fatalf("HTMX save should answer with HX-Redirect; status %d body %.300s", w.Code, w.Body.String())
	}
	if got := stockQty(t, variantID, f.PointA); got != 9 {
		t.Fatalf("stock after update = %d, want 9", got)
	}
	w = a.do(t, req{method: http.MethodPost, path: "/admin/products/" + uuid.NewString(), cookies: c, form: upd})
	wantStatus(t, w, http.StatusSeeOther)

	// Toggle active twice, then owner-only delete.
	isActive := func() bool {
		var v bool
		if err := testDB.QueryRowContext(ctxT(t), `SELECT is_active FROM products WHERE id = $1`, newID).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/admin/products/" + newID + "/toggle-active", cookies: c}), http.StatusSeeOther)
	if isActive() {
		t.Fatal("toggle should deactivate")
	}
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/admin/products/" + newID + "/toggle-active", cookies: c}), http.StatusSeeOther)
	if !isActive() {
		t.Fatal("second toggle should reactivate")
	}
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/admin/products/" + uuid.NewString() + "/toggle-active", cookies: c}), http.StatusSeeOther)
	// A manager is bounced to their first allowed screen; nothing is deleted.
	wantRedirect(t, a.do(t, req{method: http.MethodPost, path: "/admin/products/" + newID + "/delete", cookies: c}), "/admin/orders")
	if !isActive() {
		t.Fatal("manager must not delete products")
	}
	w = a.do(t, req{method: http.MethodPost, path: "/admin/products/" + newID + "/delete", cookies: []*http.Cookie{owner}, htmx: true})
	if w.Header().Get("HX-Redirect") == "" || isActive() {
		t.Fatalf("owner delete: HX-Redirect=%q active=%v", w.Header().Get("HX-Redirect"), isActive())
	}
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/admin/products/" + uuid.NewString() + "/delete", cookies: []*http.Cookie{owner}}), http.StatusSeeOther)
}

func TestAdminPanelStaffAndPointsScreens(t *testing.T) {
	t.Parallel()
	a := app(t)
	f := newFixture(t, 1, 1)
	owner, _ := a.newStaffSession(t, staff.RoleOwner, nil)
	c := []*http.Cookie{owner}
	tag := uuid.NewString()[:8]

	for _, p := range []string{"/admin/staff", "/admin/staff?done=password", "/admin/points"} {
		wantStatus(t, a.get(t, p, c...), http.StatusOK)
	}

	// Staff: invalid create shows the error in the page; valid create redirects.
	w := a.do(t, req{method: http.MethodPost, path: "/admin/staff", cookies: c, form: url.Values{"phone": {"1"}, "password": {"x"}, "name": {"x"}, "role": {"manager"}}})
	wantStatus(t, w, http.StatusOK)
	local, canonical := uniqueKGPhone()
	w = a.do(t, req{method: http.MethodPost, path: "/admin/staff", cookies: c, form: url.Values{
		"phone": {local}, "password": {"cashier-pass-1"}, "name": {"Кассир " + tag}, "role": {"point_staff"}, "point_id": {f.PointA},
	}})
	wantRedirect(t, w, "/admin/staff?done=created")
	var id string
	var pointID *string
	if err := testDB.QueryRowContext(ctxT(t), `SELECT id, point_id FROM staff WHERE phone = $1`, canonical).Scan(&id, &pointID); err != nil {
		t.Fatal(err)
	}
	if pointID == nil || *pointID != f.PointA {
		t.Fatalf("point_id = %v, want %s", pointID, f.PointA)
	}

	activeOf := func() bool {
		var v bool
		if err := testDB.QueryRowContext(ctxT(t), `SELECT is_active FROM staff WHERE id = $1`, id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	wantRedirect(t, a.do(t, req{method: http.MethodPost, path: "/admin/staff/" + id + "/toggle", cookies: c}), "/admin/staff?done=deactivated")
	if activeOf() {
		t.Fatal("toggle should deactivate")
	}
	wantRedirect(t, a.do(t, req{method: http.MethodPost, path: "/admin/staff/" + id + "/toggle", cookies: c}), "/admin/staff?done=activated")
	if !activeOf() {
		t.Fatal("second toggle should reactivate")
	}
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/admin/staff/" + uuid.NewString() + "/toggle", cookies: c}), http.StatusOK)

	// Password reset: mismatch and too-short are shown inline; a valid one works for login.
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/admin/staff/" + id + "/password", cookies: c, form: url.Values{"password": {"aaaaaaaa1"}, "password_confirm": {"bbbbbbbb1"}}}), http.StatusOK)
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/admin/staff/" + id + "/password", cookies: c, form: url.Values{"password": {"x"}, "password_confirm": {"x"}}}), http.StatusOK)
	wantRedirect(t, a.do(t, req{method: http.MethodPost, path: "/admin/staff/" + id + "/password", cookies: c, form: url.Values{"password": {"fresh-pass-77"}, "password_confirm": {"fresh-pass-77"}}}), "/admin/staff?done=password")
	if _, err := a.staff.Login(ctxT(t), local, "fresh-pass-77"); err != nil {
		t.Fatalf("login with reset password: %v", err)
	}

	// Points: create (invalid, valid), update, toggle.
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/admin/points", cookies: c, form: url.Values{"name": {""}}}), http.StatusOK)
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/admin/points", cookies: c, form: url.Values{"name": {"x"}, "city": {"c"}, "address": {"a"}, "latitude": {"abc"}}}), http.StatusOK)
	w = a.do(t, req{method: http.MethodPost, path: "/admin/points", cookies: c, form: url.Values{
		"name": {"Панель точка " + tag}, "city": {"Бишкек"}, "address": {"ул. Панель, 1"}, "working_hours": {"9-18"}, "latitude": {"42.8"}, "longitude": {"74.6"},
	}})
	wantRedirectPath(t, w, "/admin/points")
	var pid string
	if err := testDB.QueryRowContext(ctxT(t), `SELECT id FROM points_of_sale WHERE name = $1`, "Панель точка "+tag).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	wantRedirectPath(t, a.do(t, req{method: http.MethodPost, path: "/admin/points/" + pid, cookies: c, form: url.Values{
		"name": {"Панель точка " + tag}, "city": {"Ош"}, "address": {"ул. Панель, 2"},
	}}), "/admin/points")
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/admin/points/" + pid, cookies: c, form: url.Values{"name": {""}}}), http.StatusOK)
	wantRedirectPath(t, a.do(t, req{method: http.MethodPost, path: "/admin/points/" + pid + "/toggle", cookies: c}), "/admin/points")
	var city string
	var active bool
	if err := testDB.QueryRowContext(ctxT(t), `SELECT city, is_active FROM points_of_sale WHERE id = $1`, pid).Scan(&city, &active); err != nil {
		t.Fatal(err)
	}
	if city != "Ош" || active {
		t.Fatalf("point = %s/%v, want Ош/inactive", city, active)
	}
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/admin/points/" + uuid.NewString() + "/toggle", cookies: c}), http.StatusOK)
}

func TestAdminPanelOrdersStockAndCategories(t *testing.T) {
	t.Parallel()
	a := app(t)
	f := newFixture(t, 5, 5)
	o := placePickupOrder(t, f)
	mgr, _ := a.newStaffSession(t, staff.RoleManager, nil)
	c := []*http.Cookie{mgr}

	for _, q := range []string{"", "?status=placed", "?range=today", "?range=custom&from=2020-01-01&to=2099-01-01",
		"?point=" + f.PointA, "?q=" + url.QueryEscape(o.OrderNumber), "?page=3"} {
		wantStatus(t, a.get(t, "/admin/orders"+q, c...), http.StatusOK)
	}
	w := a.get(t, "/admin/orders/"+o.ID, c...)
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, o.OrderNumber)
	wantStatus(t, a.get(t, "/admin/orders/"+o.ID+"?toast=error&toast_code=invalid_status_transition", c...), http.StatusOK)
	if w := a.get(t, "/admin/orders/"+uuid.NewString(), c...); w.Code != http.StatusNotFound {
		t.Fatalf("unknown order: %d, want 404", w.Code)
	}

	// Status change: invalid transition reports the error, valid one applies.
	w = a.do(t, req{method: http.MethodPost, path: "/admin/orders/" + o.ID + "/status", cookies: c, form: url.Values{"status": {"delivered"}}})
	wantStatus(t, w, http.StatusSeeOther)
	if loc, err := url.Parse(w.Header().Get("Location")); err != nil || loc.Path != "/admin/orders/"+o.ID || loc.Query().Get("toast") != "error" {
		t.Fatalf("illegal placed→delivered should redirect with an error toast, got %q", w.Header().Get("Location"))
	}
	wantRedirectPath(t, a.do(t, req{method: http.MethodPost, path: "/admin/orders/" + o.ID + "/status", cookies: c, form: url.Values{"status": {"confirmed"}}}), "/admin/orders/"+o.ID)
	var status string
	if err := testDB.QueryRowContext(ctxT(t), `SELECT status FROM orders WHERE id = $1`, o.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "confirmed" {
		t.Fatalf("status = %s, want confirmed", status)
	}

	// Stock screen: view, search, save one cell.
	for _, q := range []string{"?point=" + f.PointA, "?point=" + f.PointA + "&q=IT-", "?point=" + uuid.NewString()} {
		wantStatus(t, a.get(t, "/admin/stock"+q, c...), http.StatusOK)
	}
	w = a.do(t, req{method: http.MethodPost, path: "/admin/stock", cookies: c, form: url.Values{
		"point": {f.PointA}, "variant_id": {f.VariantB},
		"qty_" + f.VariantB + "_" + f.PointA: {"11"}, "orig_" + f.VariantB + "_" + f.PointA: {"5"},
	}})
	if w.Code >= 400 {
		t.Fatalf("stock save: %d", w.Code)
	}
	if got := stockQty(t, f.VariantB, f.PointA); got != 11 {
		t.Fatalf("stock = %d, want 11", got)
	}

	// Categories screen: list, create, update, delete.
	tag := uuid.NewString()[:8]
	wantStatus(t, a.get(t, "/admin/categories", c...), http.StatusOK)
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/admin/categories", cookies: c, form: url.Values{"name_ru": {""}}}), http.StatusOK)
	wantRedirectPath(t, a.do(t, req{method: http.MethodPost, path: "/admin/categories", cookies: c, form: url.Values{
		"name_ru": {"Панель кат " + tag}, "name_ky": {"Кат"}, "slug": {"it-panel-" + tag}, "parent_id": {f.CategoryID}, "sort_order": {"2"},
	}}), "/admin/categories")
	var catID string
	if err := testDB.QueryRowContext(ctxT(t), `SELECT id FROM categories WHERE slug = $1`, "it-panel-"+tag).Scan(&catID); err != nil {
		t.Fatal(err)
	}
	wantRedirectPath(t, a.do(t, req{method: http.MethodPost, path: "/admin/categories/" + catID, cookies: c, form: url.Values{
		"name_ru": {"Панель кат 2 " + tag}, "name_ky": {"Кат"}, "slug": {"it-panel-" + tag},
	}}), "/admin/categories")
	wantRedirectPath(t, a.do(t, req{method: http.MethodPost, path: "/admin/categories/" + catID + "/delete", cookies: c}), "/admin/categories")
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/admin/categories/" + f.CategoryID + "/delete", cookies: c}), http.StatusOK)
}
