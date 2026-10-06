//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Nikemas/cozy_backend/internal/staff"
)

// TestAdminCatalogAPIFullLifecycle drives /admin/api/{categories,products,
// variants,images,stock} as a manager against the real schema.
func TestAdminCatalogAPIFullLifecycle(t *testing.T) {
	t.Parallel()
	a := app(t)
	f := newFixture(t, 1, 1)
	mgr, _ := a.newStaffSession(t, staff.RoleManager, nil)
	c := []*http.Cookie{mgr}
	tag := uuid.NewString()[:8]
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		return a.do(t, req{method: method, path: path, json: body, cookies: c})
	}

	// Unauthenticated and point_staff are refused.
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/admin/api/categories", json: map[string]any{}}), http.StatusUnauthorized)
	ps, _ := a.newStaffSession(t, staff.RolePointStaff, &f.PointA)
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/admin/api/categories", json: map[string]any{}, cookies: []*http.Cookie{ps}}), http.StatusForbidden)

	// Categories.
	r := call(http.MethodPost, "/admin/api/categories", map[string]any{"name_ru": "Сапоги " + tag, "name_ky": "Өтүк " + tag, "slug": "it-boots-" + tag})
	wantStatus(t, r, http.StatusCreated)
	var cat struct{ ID, Slug string }
	decodeJSON(t, r, &cat)
	wantStatus(t, call(http.MethodPost, "/admin/api/categories", map[string]any{"name_ru": "dup", "name_ky": "dup", "slug": "it-boots-" + tag}), http.StatusConflict)
	wantStatus(t, call(http.MethodPost, "/admin/api/categories", "not an object"), http.StatusBadRequest)
	r = call(http.MethodPut, "/admin/api/categories/"+cat.ID, map[string]any{"name_ru": "Сапоги 2 " + tag, "name_ky": "Өтүк", "slug": "it-boots2-" + tag, "sort_order": 3})
	wantStatus(t, r, http.StatusOK)
	wantBody(t, r, "it-boots2-"+tag)
	wantStatus(t, call(http.MethodPut, "/admin/api/categories/"+uuid.NewString(), map[string]any{"name_ru": "x", "name_ky": "x", "slug": "it-x-" + tag}), http.StatusNotFound)

	// Products.
	r = call(http.MethodPost, "/admin/api/products", map[string]any{"category_id": cat.ID, "name_ru": "Сапог " + tag, "name_ky": "Өтүк " + tag, "brand": "Cozy", "base_price": 4100})
	wantStatus(t, r, http.StatusCreated)
	var prod struct {
		ID       string
		IsActive bool `json:"is_active"`
	}
	decodeJSON(t, r, &prod)
	if !prod.IsActive {
		t.Fatal("new product should be active")
	}
	wantStatus(t, call(http.MethodPost, "/admin/api/products", map[string]any{"category_id": cat.ID, "name_ru": "", "base_price": -1}), http.StatusBadRequest)
	r = call(http.MethodPut, "/admin/api/products/"+prod.ID, map[string]any{"category_id": cat.ID, "name_ru": "Сапог X " + tag, "name_ky": "Өтүк", "base_price": 4200, "is_active": false})
	wantStatus(t, r, http.StatusOK)
	decodeJSON(t, r, &prod)
	if prod.IsActive {
		t.Fatal("is_active=false should deactivate")
	}
	wantStatus(t, call(http.MethodPut, "/admin/api/products/"+uuid.NewString(), map[string]any{"category_id": cat.ID, "name_ru": "x", "name_ky": "x", "base_price": 1}), http.StatusNotFound)

	// Variants.
	r = call(http.MethodPost, "/admin/api/products/"+prod.ID+"/variants", map[string]any{"size": "42", "color": "brown", "sku": "IT-" + tag, "price_override": 4500})
	wantStatus(t, r, http.StatusCreated)
	var v struct{ ID string }
	decodeJSON(t, r, &v)
	wantStatus(t, call(http.MethodPost, "/admin/api/products/"+prod.ID+"/variants", map[string]any{"size": "42", "color": "brown"}), http.StatusConflict)
	wantStatus(t, call(http.MethodPost, "/admin/api/products/"+uuid.NewString()+"/variants", map[string]any{"size": "1", "color": "x"}), http.StatusNotFound)
	r = call(http.MethodPut, "/admin/api/products/"+prod.ID+"/variants/"+v.ID, map[string]any{"size": "43", "color": "brown"})
	wantStatus(t, r, http.StatusOK)
	wantBody(t, r, `"43"`)

	// Images: replace twice, the second set wins.
	wantStatus(t, call(http.MethodPut, "/admin/api/products/"+prod.ID+"/images", []map[string]any{{"object_key": "products/a.jpg", "sort_order": 0}, {"object_key": "products/b.jpg", "sort_order": 1, "color": "brown"}}), http.StatusOK)
	wantStatus(t, call(http.MethodPut, "/admin/api/products/"+prod.ID+"/images", []map[string]any{{"object_key": "products/c.jpg", "sort_order": 0}}), http.StatusOK)
	var nImages int
	if err := testDB.QueryRowContext(ctxT(t), `SELECT count(*) FROM product_images WHERE product_id = $1`, prod.ID).Scan(&nImages); err != nil {
		t.Fatal(err)
	}
	if nImages != 1 {
		t.Fatalf("images after replace = %d, want 1", nImages)
	}
	wantStatus(t, call(http.MethodPut, "/admin/api/products/"+uuid.NewString()+"/images", []map[string]any{}), http.StatusNotFound)

	// Stock: set, optimistic conflict, point_staff limited to own point.
	stockPath := fmt.Sprintf("/admin/api/stock/%s/%s", v.ID, f.PointA)
	wantStatus(t, call(http.MethodPut, stockPath, map[string]any{"quantity": 7}), http.StatusOK)
	r = call(http.MethodPut, stockPath, map[string]any{"quantity": 9, "expected_quantity": 3})
	wantStatus(t, r, http.StatusConflict)
	wantBody(t, r, `"current_quantity":7`)
	wantStatus(t, call(http.MethodPut, stockPath, map[string]any{"quantity": -1}), http.StatusBadRequest)
	if got := stockQty(t, v.ID, f.PointA); got != 7 {
		t.Fatalf("stock = %d, want 7", got)
	}
	psc := []*http.Cookie{ps}
	wantStatus(t, a.do(t, req{method: http.MethodPut, path: stockPath, json: map[string]any{"quantity": 8}, cookies: psc}), http.StatusOK)
	wantStatus(t, a.do(t, req{method: http.MethodPut, path: fmt.Sprintf("/admin/api/stock/%s/%s", v.ID, f.PointB), json: map[string]any{"quantity": 1}, cookies: psc}), http.StatusForbidden)

	// Deletes: variant, product (soft), category with products refused.
	wantStatus(t, call(http.MethodDelete, "/admin/api/products/"+prod.ID+"/variants/"+uuid.NewString(), nil), http.StatusNotFound)
	wantStatus(t, call(http.MethodDelete, "/admin/api/products/"+prod.ID+"/variants/"+v.ID, nil), http.StatusNoContent)
	wantStatus(t, call(http.MethodDelete, "/admin/api/categories/"+cat.ID, nil), http.StatusConflict)
	wantStatus(t, call(http.MethodDelete, "/admin/api/products/"+prod.ID, nil), http.StatusNoContent)
	wantStatus(t, call(http.MethodDelete, "/admin/api/products/"+uuid.NewString(), nil), http.StatusNotFound)

	// An empty category can be deleted.
	r = call(http.MethodPost, "/admin/api/categories", map[string]any{"name_ru": "Пустая " + tag, "name_ky": "Бош", "slug": "it-empty-" + tag, "parent_id": cat.ID})
	wantStatus(t, r, http.StatusCreated)
	var empty struct{ ID string }
	decodeJSON(t, r, &empty)
	wantStatus(t, call(http.MethodDelete, "/admin/api/categories/"+empty.ID, nil), http.StatusNoContent)

	// Every write left an audit row.
	var nAudit int
	if err := testDB.QueryRowContext(ctxT(t), `SELECT count(*) FROM audit_log WHERE entity_id IN ($1, $2)`, prod.ID, cat.ID).Scan(&nAudit); err != nil {
		t.Fatal(err)
	}
	if nAudit < 5 {
		t.Fatalf("audit rows = %d, want >= 5", nAudit)
	}
}

func TestStaffAPILoginCRUDAndRBAC(t *testing.T) {
	t.Parallel()
	a := app(t)
	f := newFixture(t, 1, 1)
	owner, _ := a.newStaffSession(t, staff.RoleOwner, nil)
	mgr, _ := a.newStaffSession(t, staff.RoleManager, nil)
	oc := []*http.Cookie{owner}

	// JSON login: bad body, wrong password, then success sets the cookie.
	local, _ := uniqueKGPhone()
	r := a.do(t, req{method: http.MethodPost, path: "/admin/api/staff", cookies: oc, json: map[string]any{
		"phone": local, "password": "point-pass-1", "name": "Кассир", "role": "point_staff", "point_id": f.PointA,
	}})
	wantStatus(t, r, http.StatusCreated)
	if strings.Contains(r.Body.String(), "password") {
		t.Fatal("staff response must not expose the password hash")
	}
	var created struct{ ID string }
	decodeJSON(t, r, &created)

	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/admin/api/login", body: strings.NewReader("{"), ctype: "application/json"}), http.StatusBadRequest)
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/admin/api/login", json: map[string]any{"phone": local, "password": "nope-nope-1"}}), http.StatusUnauthorized)
	r = a.do(t, req{method: http.MethodPost, path: "/admin/api/login", json: map[string]any{"phone": local, "password": "point-pass-1"}})
	wantStatus(t, r, http.StatusOK)
	var session *http.Cookie
	for _, ck := range r.Result().Cookies() {
		if ck.Name == staff.SessionCookieName {
			session = ck
		}
	}
	if session == nil || session.Value == "" {
		t.Fatal("login must set the staff session cookie")
	}

	// Staff CRUD is owner-only.
	wantStatus(t, a.do(t, req{method: http.MethodGet, path: "/admin/api/staff", cookies: []*http.Cookie{mgr}}), http.StatusForbidden)
	r = a.do(t, req{method: http.MethodGet, path: "/admin/api/staff", cookies: oc})
	wantStatus(t, r, http.StatusOK)
	wantBody(t, r, created.ID)

	invalid := []map[string]any{
		{"phone": "123", "password": "long-enough-1", "name": "x", "role": "manager"},
		{"phone": local, "password": "long-enough-1", "name": "dup", "role": "manager"},
		{"phone": "0700000001", "password": "short", "name": "x", "role": "manager"},
		{"phone": "0700000002", "password": "long-enough-1", "name": "x", "role": "point_staff"},
		{"phone": "0700000003", "password": "long-enough-1", "name": "x", "role": "emperor"},
	}
	for i, body := range invalid {
		w := a.do(t, req{method: http.MethodPost, path: "/admin/api/staff", cookies: oc, json: body})
		if w.Code < 400 || w.Code >= 500 {
			t.Fatalf("invalid create #%d: status %d, want 4xx; body %s", i, w.Code, w.Body.String())
		}
	}
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/admin/api/staff", cookies: oc, body: strings.NewReader("["), ctype: "application/json"}), http.StatusBadRequest)

	// Update: rename + new password; deactivation kills the session.
	pw := "new-point-pass-2"
	r = a.do(t, req{method: http.MethodPut, path: "/admin/api/staff/" + created.ID, cookies: oc, json: map[string]any{
		"name": "Кассир 2", "role": "point_staff", "point_id": f.PointA, "is_active": true, "password": pw,
	}})
	wantStatus(t, r, http.StatusOK)
	wantBody(t, r, "Кассир 2")
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/admin/api/login", json: map[string]any{"phone": local, "password": pw}}), http.StatusOK)
	wantStatus(t, a.do(t, req{method: http.MethodPut, path: "/admin/api/staff/" + uuid.NewString(), cookies: oc, json: map[string]any{"name": "x", "role": "manager", "is_active": true}}), http.StatusNotFound)
	wantStatus(t, a.do(t, req{method: http.MethodPut, path: "/admin/api/staff/" + created.ID, cookies: oc, body: strings.NewReader("{"), ctype: "application/json"}), http.StatusBadRequest)
	r = a.do(t, req{method: http.MethodPut, path: "/admin/api/staff/" + created.ID, cookies: oc, json: map[string]any{
		"name": "Кассир 2", "role": "point_staff", "point_id": f.PointA, "is_active": false,
	}})
	wantStatus(t, r, http.StatusOK)
	wantStatus(t, a.do(t, req{method: http.MethodGet, path: "/admin/api/orders", cookies: []*http.Cookie{session}}), http.StatusUnauthorized)

	// Logout revokes the session; logging out without a cookie is fine too.
	w := a.do(t, req{method: http.MethodPost, path: "/admin/api/logout", cookies: []*http.Cookie{mgr}})
	wantStatus(t, w, http.StatusOK)
	wantStatus(t, a.do(t, req{method: http.MethodGet, path: "/admin/api/orders", cookies: []*http.Cookie{mgr}}), http.StatusUnauthorized)
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/admin/api/logout"}), http.StatusOK)
}

func TestPointsAPICRUD(t *testing.T) {
	t.Parallel()
	a := app(t)
	owner, _ := a.newStaffSession(t, staff.RoleOwner, nil)
	mgr, _ := a.newStaffSession(t, staff.RoleManager, nil)
	oc := []*http.Cookie{owner}
	tag := uuid.NewString()[:8]

	wantStatus(t, a.do(t, req{method: http.MethodGet, path: "/admin/api/points", cookies: []*http.Cookie{mgr}}), http.StatusForbidden)

	r := a.do(t, req{method: http.MethodPost, path: "/admin/api/points", cookies: oc, json: map[string]any{
		"name": "Точка API " + tag, "city": "Бишкек", "address": "ул. API, 1", "working_hours": "10-20",
		"latitude": 42.87, "longitude": 74.59,
	}})
	wantStatus(t, r, http.StatusCreated)
	var p struct{ ID string }
	decodeJSON(t, r, &p)

	r = a.do(t, req{method: http.MethodGet, path: "/admin/api/points", cookies: oc})
	wantStatus(t, r, http.StatusOK)
	wantBody(t, r, p.ID)

	for _, body := range []any{
		map[string]any{"name": "", "city": "c", "address": "x"},
		map[string]any{"name": "x", "city": "", "address": "x"},
		map[string]any{"name": "x", "city": "c", "address": ""},
		map[string]any{"name": "x", "city": "c", "address": "x", "latitude": 1.0},
		map[string]any{"name": "x", "city": "c", "address": "x", "latitude": 100.0, "longitude": 1.0},
		"nope",
	} {
		w := a.do(t, req{method: http.MethodPost, path: "/admin/api/points", cookies: oc, json: body})
		wantStatus(t, w, http.StatusBadRequest)
	}

	r = a.do(t, req{method: http.MethodPut, path: "/admin/api/points/" + p.ID, cookies: oc, json: map[string]any{
		"name": "Точка API 2 " + tag, "city": "Ош", "address": "ул. API, 2", "is_active": false,
	}})
	wantStatus(t, r, http.StatusOK)
	wantBody(t, r, "ул. API, 2")
	wantStatus(t, a.do(t, req{method: http.MethodPut, path: "/admin/api/points/" + uuid.NewString(), cookies: oc, json: map[string]any{"name": "x", "city": "c", "address": "y"}}), http.StatusNotFound)
	wantStatus(t, a.do(t, req{method: http.MethodPut, path: "/admin/api/points/" + p.ID, cookies: oc, json: map[string]any{"name": "", "city": "c", "address": "y"}}), http.StatusBadRequest)

	// A point a staff member is assigned to can't be deleted (FK, 409).
	f := newFixture(t, 1, 1)
	a.newStaffSession(t, staff.RolePointStaff, &f.PointB)
	wantStatus(t, a.do(t, req{method: http.MethodDelete, path: "/admin/api/points/" + f.PointB, cookies: oc}), http.StatusConflict)

	wantStatus(t, a.do(t, req{method: http.MethodDelete, path: "/admin/api/points/" + p.ID, cookies: oc}), http.StatusNoContent)
	wantStatus(t, a.do(t, req{method: http.MethodDelete, path: "/admin/api/points/" + uuid.NewString(), cookies: oc}), http.StatusNotFound)
}
