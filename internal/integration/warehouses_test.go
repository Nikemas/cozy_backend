//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/points"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// newWarehouse inserts an active warehouse and returns its id.
func newWarehouse(t *testing.T) string {
	t.Helper()
	var id string
	err := testDB.QueryRowContext(ctxT(t),
		`INSERT INTO points_of_sale (name, address, kind) VALUES ($1, 'ул. Складская, 1', 'warehouse') RETURNING id`,
		"Склад IT "+uuid.NewString()[:8]).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	ae, ok := err.(*apperr.AppError)
	if !ok || ae.Code != code {
		t.Fatalf("err = %v, want apperr %q", err, code)
	}
}

func stockLocation(t *testing.T, variantID, pointID string) string {
	t.Helper()
	var loc *string
	err := testDB.QueryRowContext(ctxT(t),
		`SELECT location_id::text FROM stock WHERE variant_id = $1 AND point_id = $2`, variantID, pointID).Scan(&loc)
	if err != nil {
		t.Fatal(err)
	}
	if loc == nil {
		return ""
	}
	return *loc
}

func TestStorageLocationsRepo(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	f := newFixture(t, 1, 0)
	wh := newWarehouse(t)
	repo := points.NewLocationsRepo(testDB)

	shelf, err := repo.Create(ctx, wh, points.LocationInput{Name: "  Стеллаж  A, полка 3 "})
	if err != nil {
		t.Fatal(err)
	}
	if shelf.Name != "Стеллаж A, полка 3" || shelf.SortOrder != 1 || !shelf.IsActive {
		t.Errorf("created = %+v", shelf)
	}
	second, err := repo.Create(ctx, wh, points.LocationInput{Name: "Полка B"})
	if err != nil {
		t.Fatal(err)
	}
	if second.SortOrder != 2 {
		t.Errorf("second sort_order = %d, want 2", second.SortOrder)
	}

	// Names are unique per warehouse, case-insensitively; blank is refused.
	_, err = repo.Create(ctx, wh, points.LocationInput{Name: "стеллаж a, ПОЛКА 3"})
	wantCode(t, err, "location_name_taken")
	_, err = repo.Update(ctx, wh, second.ID, points.LocationInput{Name: "СТЕЛЛАЖ A, полка 3"})
	wantCode(t, err, "location_name_taken")
	_, err = repo.Create(ctx, wh, points.LocationInput{Name: "   "})
	wantCode(t, err, "invalid_location_name")
	// Only warehouses have locations.
	_, err = repo.Create(ctx, f.PointA, points.LocationInput{Name: "Полка"})
	wantCode(t, err, "not_a_warehouse")
	// Another warehouse may reuse the name.
	if _, err := repo.Create(ctx, newWarehouse(t), points.LocationInput{Name: "Стеллаж A, полка 3"}); err != nil {
		t.Fatalf("same name in another warehouse: %v", err)
	}
	// A location is only reachable through its own warehouse.
	_, err = repo.Get(ctx, f.PointA, shelf.ID)
	wantCode(t, err, "location_not_found")

	// Goods at the location: delete refused, deactivate allowed.
	if _, err := testDB.ExecContext(ctx,
		`INSERT INTO stock (variant_id, point_id, quantity, location_id) VALUES ($1, $2, 3, $3)`, f.VariantA, wh, shelf.ID); err != nil {
		t.Fatal(err)
	}
	wantCode(t, repo.Delete(ctx, wh, shelf.ID), "location_in_use")
	if err := repo.SetActive(ctx, wh, shelf.ID, false); err != nil {
		t.Fatal(err)
	}
	list, err := repo.ListByPoint(ctx, wh)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != shelf.ID || list[0].IsActive || list[0].StockQty != 3 {
		t.Errorf("list = %+v", list[0])
	}

	// Empty again: delete works and the stock row just loses its location.
	if _, err := testDB.ExecContext(ctx, `UPDATE stock SET quantity = 0 WHERE variant_id = $1 AND point_id = $2`, f.VariantA, wh); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, wh, shelf.ID); err != nil {
		t.Fatalf("delete empty location: %v", err)
	}
	if got := stockLocation(t, f.VariantA, wh); got != "" {
		t.Errorf("stock.location_id = %q after the location was deleted, want NULL", got)
	}
	if got := stockQty(t, f.VariantA, wh); got != 0 {
		t.Errorf("stock row changed: qty %d", got)
	}
	wantCode(t, repo.Delete(ctx, wh, shelf.ID), "location_not_found")
}

// The composite FK keeps a stock row from pointing at another point's
// location.
func TestStockLocationMustBelongToSamePoint(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	f := newFixture(t, 1, 0)
	whA, whB := newWarehouse(t), newWarehouse(t)
	loc, err := points.NewLocationsRepo(testDB).Create(ctx, whB, points.LocationInput{Name: "B-1"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = testDB.ExecContext(ctx,
		`INSERT INTO stock (variant_id, point_id, quantity, location_id) VALUES ($1, $2, 1, $3)`, f.VariantA, whA, loc.ID)
	if err == nil {
		t.Fatal("stock row accepted a location of another warehouse")
	}
}

func TestAdminStockPageSavesLocation(t *testing.T) {
	t.Parallel()
	a := app(t)
	ctx := ctxT(t)
	f := newFixture(t, 1, 0)
	wh, other := newWarehouse(t), newWarehouse(t)
	locs := points.NewLocationsRepo(testDB)
	shelf, err := locs.Create(ctx, wh, points.LocationInput{Name: "A-1"})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := locs.Create(ctx, other, points.LocationInput{Name: "X-1"})
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := a.newStaffSession(t, staff.RoleOwner, nil)
	c := []*http.Cookie{owner}

	w := a.get(t, "/admin/stock?point="+wh+"&q="+url.QueryEscape(productName(t, f.ProductID)), c...)
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, `name="loc_`+f.VariantA+`_`+wh+`"`, "A-1")

	// Location only (qty untouched): a stock row with 0 is created at the place.
	form := url.Values{
		"point": {wh}, "variant_id": {f.VariantA},
		"qty_" + f.VariantA + "_" + wh: {"0"}, "orig_" + f.VariantA + "_" + wh: {""},
		"loc_" + f.VariantA + "_" + wh: {shelf.ID}, "orig_loc_" + f.VariantA + "_" + wh: {""},
	}
	w = a.do(t, req{method: http.MethodPost, path: "/admin/stock", cookies: c, form: form})
	wantStatus(t, w, http.StatusSeeOther)
	if got := stockLocation(t, f.VariantA, wh); got != shelf.ID {
		t.Fatalf("location = %q, want %q", got, shelf.ID)
	}

	// Another warehouse's location is refused, nothing written.
	form.Set("qty_"+f.VariantA+"_"+wh, "4")
	form.Set("orig_"+f.VariantA+"_"+wh, "0")
	form.Set("loc_"+f.VariantA+"_"+wh, foreign.ID)
	form.Set("orig_loc_"+f.VariantA+"_"+wh, shelf.ID)
	w = a.do(t, req{method: http.MethodPost, path: "/admin/stock", cookies: c, form: form})
	wantStatus(t, w, http.StatusOK)
	if got := stockLocation(t, f.VariantA, wh); got != shelf.ID {
		t.Errorf("location = %q after a refused save", got)
	}
	if got := stockQty(t, f.VariantA, wh); got != 0 {
		t.Errorf("qty = %d after a refused save, want 0", got)
	}

	// Filter by location.
	w = a.get(t, "/admin/stock?point="+wh+"&loc="+shelf.ID, c...)
	wantBody(t, w, productName(t, f.ProductID))
	w = a.get(t, "/admin/stock?point="+wh+"&loc=none&q="+url.QueryEscape(productName(t, f.ProductID)), c...)
	if strings.Contains(w.Body.String(), `name="loc_`+f.VariantA+`_`) {
		t.Error("loc=none lists a variant that has a location")
	}
}

func TestAdminProductFormSavesWarehouseLocation(t *testing.T) {
	t.Parallel()
	a := app(t)
	ctx := ctxT(t)
	f := newFixture(t, 2, 0)
	wh := newWarehouse(t)
	shelf, err := points.NewLocationsRepo(testDB).Create(ctx, wh, points.LocationInput{Name: "P-1"})
	if err != nil {
		t.Fatal(err)
	}
	mgr, _ := a.newStaffSession(t, staff.RoleManager, nil)
	c := []*http.Cookie{mgr}

	w := a.get(t, "/admin/products/"+f.ProductID, c...)
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, `name="loc_`+f.VariantA+`_`+wh+`"`, `name="orig_loc_`+f.VariantA+`_`+wh+`" value=""`, "P-1")

	form := url.Values{
		"category_id": {f.CategoryID}, "name_ru": {productName(t, f.ProductID)}, "name_ky": {"IT"}, "base_price": {"2500"},
		"variant_key": {f.VariantA}, "variant_id": {f.VariantA}, "variant_size": {"40"}, "variant_color": {"black"},
		"qty_" + f.VariantA + "_" + f.PointA: {"2"}, "orig_" + f.VariantA + "_" + f.PointA: {"2"},
		"qty_" + f.VariantA + "_" + wh: {"7"}, "orig_" + f.VariantA + "_" + wh: {""},
		"loc_" + f.VariantA + "_" + wh: {shelf.ID}, "orig_loc_" + f.VariantA + "_" + wh: {""},
	}
	// VariantB is dropped from the form only if it has no orders — keep it.
	form["variant_key"] = append(form["variant_key"], f.VariantB)
	form["variant_id"] = append(form["variant_id"], f.VariantB)
	form["variant_size"] = append(form["variant_size"], "41")
	form["variant_color"] = append(form["variant_color"], "black")
	w = a.do(t, req{method: http.MethodPost, path: "/admin/products/" + f.ProductID, cookies: c, form: form})
	wantStatus(t, w, http.StatusSeeOther)

	if got := stockQty(t, f.VariantA, wh); got != 7 {
		t.Errorf("warehouse qty = %d, want 7", got)
	}
	if got := stockLocation(t, f.VariantA, wh); got != shelf.ID {
		t.Errorf("location = %q, want %q", got, shelf.ID)
	}
	if got := stockQty(t, f.VariantA, f.PointA); got != 2 {
		t.Errorf("untouched branch qty = %d, want 2", got)
	}
}

func TestStaffRolePointKind(t *testing.T) {
	t.Parallel()
	a := app(t)
	ctx := ctxT(t)
	f := newFixture(t, 0, 0)
	wh := newWarehouse(t)

	phone, _ := uniqueKGPhone()
	_, err := a.staff.CreateStaff(ctx, staff.CreateStaffInput{Phone: phone, Password: "storekeeper-1", Name: "К", Role: staff.RoleStorekeeper, PointID: &f.PointA})
	wantCode(t, err, "storekeeper_needs_warehouse")
	_, err = a.staff.CreateStaff(ctx, staff.CreateStaffInput{Phone: phone, Password: "storekeeper-1", Name: "К", Role: staff.RoleStorekeeper})
	wantCode(t, err, "point_id_required")
	_, err = a.staff.CreateStaff(ctx, staff.CreateStaffInput{Phone: phone, Password: "storekeeper-1", Name: "К", Role: staff.RolePointStaff, PointID: &wh})
	wantCode(t, err, "point_staff_needs_branch")

	sk, err := a.staff.CreateStaff(ctx, staff.CreateStaffInput{Phone: phone, Password: "storekeeper-1", Name: "К", Role: staff.RoleStorekeeper, PointID: &wh})
	if err != nil {
		t.Fatal(err)
	}
	if sk.Role != staff.RoleStorekeeper || sk.PointID == nil || *sk.PointID != wh {
		t.Errorf("created = %+v", sk)
	}

	// The staff page's form: a storekeeper takes its warehouse from warehouse_id.
	owner, _ := a.newStaffSession(t, staff.RoleOwner, nil)
	phone2, _ := uniqueKGPhone()
	w := a.do(t, req{method: http.MethodPost, path: "/admin/staff", cookies: []*http.Cookie{owner}, form: url.Values{
		"name": {"Кладовщик IT"}, "phone": {phone2}, "role": {"storekeeper"}, "point_id": {f.PointA}, "warehouse_id": {wh}, "password": {"storekeeper-2"}}})
	wantRedirect(t, w, "/admin/staff?done=created")
	w = a.get(t, "/admin/staff", owner)
	wantBody(t, w, "Кладовщик IT", `value="storekeeper"`, `name="warehouse_id"`)
}

// A storekeeper reaches only Остатки and Склады of its own warehouse —
// every other admin page/route, by menu or by URL, is closed.
func TestStorekeeperAccess(t *testing.T) {
	t.Parallel()
	a := app(t)
	ctx := ctxT(t)
	f := newFixture(t, 1, 0)
	wh, other := newWarehouse(t), newWarehouse(t)
	sk, _ := a.newStaffSession(t, staff.RoleStorekeeper, &wh)
	c := []*http.Cookie{sk}

	// Closed HTML pages redirect to its first page (Остатки).
	for _, path := range []string{
		"/admin/orders", "/admin/orders/badge", "/admin/orders/" + uuid.NewString(), "/admin/products", "/admin/products/new",
		"/admin/products/" + f.ProductID, "/admin/products/import", "/admin/categories", "/admin/reports", "/admin/broadcasts",
		"/admin/banner", "/admin/points", "/admin/delivery", "/admin/staff", "/admin/audit",
	} {
		wantRedirect(t, a.get(t, path, c...), "/admin/stock")
	}
	for _, path := range []string{"/admin/orders/bulk-status", "/admin/products", "/admin/points", "/admin/staff", "/admin/warehouses", "/admin/warehouses/" + wh, "/admin/warehouses/" + wh + "/toggle", "/admin/categories"} {
		wantRedirect(t, a.do(t, req{method: http.MethodPost, path: path, cookies: c, form: url.Values{"name": {"x"}, "city": {"x"}, "address": {"x"}}}), "/admin/stock")
	}
	// JSON admin API: forbidden.
	for _, path := range []string{"/admin/api/orders", "/admin/api/staff", "/admin/api/points", "/admin/api/reports/sales"} {
		if w := a.get(t, path, c...); w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 403", path, w.Code)
		}
	}

	// Menu: only Остатки and Склады.
	w := a.get(t, "/admin/stock", c...)
	wantStatus(t, w, http.StatusOK)
	body := w.Body.String()
	for _, closed := range []string{`href="/admin/orders"`, `href="/admin/products"`, `href="/admin/reports"`, `href="/admin/staff"`, `href="/admin/points"`} {
		if strings.Contains(body, closed) {
			t.Errorf("storekeeper menu has %s", closed)
		}
	}
	wantBody(t, w, `href="/admin/warehouses"`, `name="loc_`)

	// Its warehouse only, whatever ?point= says; product links hidden.
	w = a.get(t, "/admin/stock?point="+f.PointA, c...)
	wantBody(t, w, "_"+wh+`"`)
	if strings.Contains(w.Body.String(), "_"+f.PointA+`"`) || strings.Contains(w.Body.String(), "/admin/products/") {
		t.Error("storekeeper saw another point's stock or product links")
	}
	form := url.Values{"point": {f.PointA}, "variant_id": {f.VariantA},
		"qty_" + f.VariantA + "_" + f.PointA: {"50"}, "orig_" + f.VariantA + "_" + f.PointA: {"1"},
		"qty_" + f.VariantA + "_" + wh: {"6"}, "orig_" + f.VariantA + "_" + wh: {""}}
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: "/admin/stock", cookies: c, form: form}), http.StatusSeeOther)
	if got := stockQty(t, f.VariantA, f.PointA); got != 1 {
		t.Errorf("branch stock = %d, want untouched 1", got)
	}
	if got := stockQty(t, f.VariantA, wh); got != 6 {
		t.Errorf("own warehouse stock = %d, want 6", got)
	}

	// Склады: straight to its own warehouse; locations managed there only.
	wantRedirect(t, a.get(t, "/admin/warehouses", c...), "/admin/warehouses/"+wh)
	wantStatus(t, a.get(t, "/admin/warehouses/"+wh, c...), http.StatusOK)
	if w := a.get(t, "/admin/warehouses/"+other, c...); w.Code != http.StatusSeeOther {
		t.Errorf("foreign warehouse page: %d, want a redirect", w.Code)
	}
	w = a.do(t, req{method: http.MethodPost, path: "/admin/warehouses/" + wh + "/locations", cookies: c, form: url.Values{"name": {"Своя полка"}}})
	wantStatus(t, w, http.StatusSeeOther)
	a.do(t, req{method: http.MethodPost, path: "/admin/warehouses/" + other + "/locations", cookies: c, form: url.Values{"name": {"Чужая полка"}}})
	var own, foreign int
	if err := testDB.QueryRowContext(ctx, `SELECT COUNT(*) FILTER (WHERE point_id = $1), COUNT(*) FILTER (WHERE point_id = $2) FROM storage_locations`, wh, other).Scan(&own, &foreign); err != nil {
		t.Fatal(err)
	}
	if own != 1 || foreign != 0 {
		t.Errorf("locations own=%d foreign=%d, want 1 and 0", own, foreign)
	}
}

// Owner: warehouses live under «Склады», branches under «Точки продаж».
func TestOwnerWarehousesSection(t *testing.T) {
	t.Parallel()
	a := app(t)
	ctx := ctxT(t)
	f := newFixture(t, 0, 0)
	wh := newWarehouse(t)
	owner, _ := a.newStaffSession(t, staff.RoleOwner, nil)
	c := []*http.Cookie{owner}
	whName, branchName := pointName(t, wh), pointName(t, f.PointA)

	w := a.get(t, "/admin/points", c...)
	wantBody(t, w, branchName)
	if strings.Contains(w.Body.String(), whName) {
		t.Error("warehouse listed under «Точки продаж»")
	}
	w = a.get(t, "/admin/warehouses", c...)
	wantBody(t, w, whName)
	if strings.Contains(w.Body.String(), branchName) {
		t.Error("branch listed under «Склады»")
	}

	tag := uuid.NewString()[:8]
	w = a.do(t, req{method: http.MethodPost, path: "/admin/warehouses", cookies: c, form: url.Values{"name": {"Новый склад " + tag}, "city": {"Ош"}, "address": {"ул. 5"}}})
	wantStatus(t, w, http.StatusSeeOther)
	var newID, kind string
	if err := testDB.QueryRowContext(ctx, `SELECT id, kind FROM points_of_sale WHERE name = $1`, "Новый склад "+tag).Scan(&newID, &kind); err != nil {
		t.Fatal(err)
	}
	if kind != "warehouse" {
		t.Errorf("created kind = %q", kind)
	}
	wantRedirectPath(t, w, "/admin/warehouses/"+newID)

	// A branch can't be edited as a warehouse and vice versa.
	a.do(t, req{method: http.MethodPost, path: "/admin/warehouses/" + f.PointA + "/toggle", cookies: c})
	a.do(t, req{method: http.MethodPost, path: "/admin/points/" + wh + "/toggle", cookies: c})
	var activeA, activeW bool
	if err := testDB.QueryRowContext(ctx, `SELECT (SELECT is_active FROM points_of_sale WHERE id = $1), (SELECT is_active FROM points_of_sale WHERE id = $2)`, f.PointA, wh).Scan(&activeA, &activeW); err != nil {
		t.Fatal(err)
	}
	if !activeA || !activeW {
		t.Error("a point was toggled from the other section")
	}

	// Locations: add, rename, deactivate, delete.
	base := "/admin/warehouses/" + wh + "/locations"
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: base, cookies: c, form: url.Values{"name": {"A-1"}}}), http.StatusSeeOther)
	w = a.do(t, req{method: http.MethodPost, path: base, cookies: c, form: url.Values{"name": {"a-1"}}})
	wantStatus(t, w, http.StatusOK) // duplicate: re-rendered with the error
	wantBody(t, w, "уже есть")
	var locID string
	if err := testDB.QueryRowContext(ctx, `SELECT id FROM storage_locations WHERE point_id = $1`, wh).Scan(&locID); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: base + "/" + locID, cookies: c, form: url.Values{"name": {"B-2"}, "sort_order": {"5"}}}), http.StatusSeeOther)
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: base + "/" + locID + "/toggle", cookies: c, form: url.Values{"active": {"0"}}}), http.StatusSeeOther)
	var name string
	var sortOrder int
	var active bool
	if err := testDB.QueryRowContext(ctx, `SELECT name, sort_order, is_active FROM storage_locations WHERE id = $1`, locID).Scan(&name, &sortOrder, &active); err != nil {
		t.Fatal(err)
	}
	if name != "B-2" || sortOrder != 5 || active {
		t.Errorf("location = %q/%d/%v", name, sortOrder, active)
	}
	w = a.get(t, "/admin/warehouses/"+wh, c...)
	wantBody(t, w, "B-2")
	wantStatus(t, a.do(t, req{method: http.MethodPost, path: base + "/" + locID + "/delete", cookies: c}), http.StatusSeeOther)
	var n int
	if err := testDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM storage_locations WHERE point_id = $1`, wh).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d locations left after delete", n)
	}
}

func pointName(t *testing.T, id string) string {
	t.Helper()
	var name string
	if err := testDB.QueryRowContext(ctxT(t), `SELECT name FROM points_of_sale WHERE id = $1`, id).Scan(&name); err != nil {
		t.Fatal(err)
	}
	return name
}

// Rolling back 000050 turns storekeepers into deactivated, logged-out
// point_staff and leaves the enum without the value.
func TestStorekeeperMigrationDown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, drop, err := createTempDB(ctx, os.Getenv("TEST_DATABASE_URL"), "skdown")
	if err != nil {
		t.Fatal(err)
	}
	defer drop()
	if err := migrate(ctx, db, "up"); err != nil {
		t.Fatal(err)
	}

	var wh, sk string
	if err := db.QueryRowContext(ctx, `INSERT INTO points_of_sale (name, address, kind) VALUES ('W', 'a', 'warehouse') RETURNING id`).Scan(&wh); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `INSERT INTO staff (phone, password_hash, name, role, point_id) VALUES ('+996700111222', 'x', 'K', 'storekeeper', $1) RETURNING id`, wh).Scan(&sk); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO staff_sessions (staff_id, token_hash, expires_at) VALUES ($1, 'h', now() + interval '1 day')`, sk); err != nil {
		t.Fatalf("session: %v", err)
	}

	body, err := os.ReadFile(migrationsDir + "/000050_staff_role_storekeeper.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(body)); err != nil {
		t.Fatalf("down: %v", err)
	}

	var role string
	var active bool
	var sessions int
	if err := db.QueryRowContext(ctx, `SELECT role::text, is_active, (SELECT COUNT(*) FROM staff_sessions WHERE staff_id = $1) FROM staff WHERE id = $1`, sk).Scan(&role, &active, &sessions); err != nil {
		t.Fatal(err)
	}
	if role != "point_staff" || active || sessions != 0 {
		t.Errorf("after down: role=%s active=%v sessions=%d", role, active, sessions)
	}
	var hasValue bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_enum e JOIN pg_type t ON t.oid = e.enumtypid WHERE t.typname = 'staff_role' AND e.enumlabel = 'storekeeper')`).Scan(&hasValue); err != nil {
		t.Fatal(err)
	}
	if hasValue {
		t.Error("staff_role still has storekeeper")
	}
}
