package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/audit"
	"github.com/Nikemas/cozy_backend/internal/points"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

const (
	uuid1 = "11111111-1111-1111-1111-111111111111"
	uuid2 = "22222222-2222-2222-2222-222222222222"
	catID = "cccccccc-cccc-cccc-cccc-cccccccccccc"
)

func TestBulkIDs(t *testing.T) {
	ids, msg := bulkIDs([]string{uuid1, " " + uuid1, "junk", uuid2})
	if msg != "" || len(ids) != 2 {
		t.Fatalf("ids=%v msg=%q", ids, msg)
	}
	if _, msg := bulkIDs([]string{"junk"}); msg == "" {
		t.Error("no valid ids accepted")
	}
	many := make([]string, maxBulkItems+1)
	for i := range many {
		many[i] = fmt.Sprintf("%08d-1111-1111-1111-111111111111", i)
	}
	if _, msg := bulkIDs(many); msg == "" {
		t.Error("over the cap accepted")
	}
}

func TestSafeReturnURL(t *testing.T) {
	cases := map[string]string{
		"/admin/products?cat=men&toast=x&point=p1": "/admin/products?cat=men&point=p1",
		"https://evil.example/admin/products":      "/admin/products",
		"//evil.example/admin/products":            "/admin/products",
		"/admin/orders?status=placed":              "/admin/products",
		"":                                         "/admin/products",
		"/admin/products?toast=only":               "/admin/products",
	}
	for in, want := range cases {
		if got := safeReturnURL(in, "/admin/products"); got != want {
			t.Errorf("safeReturnURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveProductsPointAndChipParams(t *testing.T) {
	pts := []*points.Point{{ID: "pA", Name: "Центр", IsActive: true}, {ID: "pB", Name: "Дордой"}}
	sel := resolveProductsPoint(pts, "pB", true)
	if sel.ID != "pB" || !sel.OOS || sel.oosPointID() != "pB" || len(sel.Options) != 3 || !sel.Options[2].Selected {
		t.Fatalf("sel = %+v", sel)
	}
	if !strings.Contains(sel.Options[2].Name, "неактивна") {
		t.Errorf("inactive point not marked: %q", sel.Options[2].Name)
	}
	if sel := resolveProductsPoint(pts, "unknown", true); sel.ID != "" || sel.OOS {
		t.Errorf("unknown point kept: %+v", sel)
	}
	chips := withPointParams([]ChipVM{{URL: "/admin/products?cat=men"}}, sel)
	if chips[0].URL != "/admin/products?cat=men&oos=1&point=pB" {
		t.Errorf("chip URL = %q", chips[0].URL)
	}
}

func TestProductOpsBulkSetActiveJournalsChangedOnly(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.ValueConverterOption(stringSliceConverter{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, name_ru, is_active, category_id FROM products WHERE id = ANY\(\$1\) ORDER BY id FOR UPDATE`).
		WithArgs([]string{uuid1, uuid2}).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name_ru", "is_active", "category_id"}).
			AddRow(uuid1, "Кеды", true, catID).AddRow(uuid2, "Сапоги", false, catID))
	mock.ExpectExec(`UPDATE products SET is_active = \$2, updated_at = now\(\) WHERE id = ANY\(\$1\)`).
		WithArgs([]string{uuid1}, false).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`SAVEPOINT audit_log_write`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO audit_log`).
		WithArgs("s1", audit.ActionProductDeactivate, audit.EntityProduct, uuid1, "Товар «Кеды» деактивирован (массово)",
			`{"bulk":true,"is_active":{"from":true,"to":false}}`, nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`RELEASE SAVEPOINT audit_log_write`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	s := &productOpsStore{db: db, audit: audit.New(db)}
	n, err := s.BulkSetActive(ownerCtx(), []string{uuid1, uuid2}, false)
	if err != nil || n != 1 {
		t.Fatalf("BulkSetActive = %d, %v", n, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestProductOpsBulkSetCategory(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.ValueConverterOption(stringSliceConverter{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT name_ru FROM categories WHERE id = \$1`).WithArgs(catID).
		WillReturnRows(sqlmock.NewRows([]string{"name_ru"}).AddRow("Кеды"))
	mock.ExpectQuery(`FROM products WHERE id = ANY`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name_ru", "is_active", "category_id"}).
			AddRow(uuid1, "Vans Old Skool", true, "old-cat").AddRow(uuid2, "Converse", true, catID))
	mock.ExpectExec(`UPDATE products SET category_id = \$2`).WithArgs([]string{uuid1}, catID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`SAVEPOINT audit_log_write`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT id, name_ru FROM categories WHERE id = ANY`).WithArgs([]string{"old-cat"}).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name_ru"}).AddRow("old-cat", "Кроссовки"))
	mock.ExpectExec(`INSERT INTO audit_log`).
		WithArgs("s1", audit.ActionProductCategory, audit.EntityProduct, uuid1,
			"Товар «Vans Old Skool»: категория «Кроссовки» → «Кеды» (массово)", sqlmock.AnyArg(), nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`RELEASE SAVEPOINT audit_log_write`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	s := &productOpsStore{db: db, audit: audit.New(db)}
	n, err := s.BulkSetCategory(ownerCtx(), []string{uuid1, uuid2}, catID)
	if err != nil || n != 1 {
		t.Fatalf("BulkSetCategory = %d, %v", n, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestProductOpsBulkSetCategoryUnknownCategory(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.ValueConverterOption(stringSliceConverter{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT name_ru FROM categories`).WillReturnRows(sqlmock.NewRows([]string{"name_ru"}))
	mock.ExpectRollback()

	_, err = (&productOpsStore{db: db}).BulkSetCategory(context.Background(), []string{uuid1}, catID)
	if ae, ok := err.(*apperr.AppError); !ok || ae.Status != http.StatusBadRequest {
		t.Fatalf("err = %v, want 400", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestProductOpsStockAtPoint(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.ValueConverterOption(stringSliceConverter{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectQuery(`LEFT JOIN stock s ON s.variant_id = pv.id AND s.point_id = \$2`).
		WithArgs([]string{uuid1, uuid2}, "pA").
		WillReturnRows(sqlmock.NewRows([]string{"product_id", "sum"}).AddRow(uuid1, 3).AddRow(uuid2, 0))
	got, err := (&productOpsStore{db: db}).StockAtPoint(context.Background(), "pA", []string{uuid1, uuid2})
	if err != nil || got[uuid1] != 3 || got[uuid2] != 0 {
		t.Fatalf("StockAtPoint = %v, %v", got, err)
	}
}

// fakeProductOps records bulk calls.
type fakeProductOps struct {
	ids      []string
	active   *bool
	category string
	err      error
}

func (f *fakeProductOps) BulkSetActive(_ context.Context, ids []string, active bool) (int, error) {
	f.ids, f.active = ids, &active
	return len(ids), f.err
}

func (f *fakeProductOps) BulkSetCategory(_ context.Context, ids []string, categoryID string) (int, error) {
	f.ids, f.category = ids, categoryID
	return len(ids), f.err
}

func (f *fakeProductOps) StockAtPoint(context.Context, string, []string) (map[string]int, error) {
	return map[string]int{}, nil
}

func postBulk(h *handlers, form url.Values) *httptest.ResponseRecorder {
	r := requestAs(http.MethodPost, "/admin/products/bulk", &staff.Staff{ID: "s1", Role: staff.RoleManager}, form)
	w := httptest.NewRecorder()
	h.productsBulk(w, r)
	return w
}

func TestProductsBulkHandler(t *testing.T) {
	ops := &fakeProductOps{}
	h := &handlers{productOps: ops}

	w := postBulk(h, url.Values{"action": {"deactivate"}, "id": {uuid1, uuid2}, "back": {"/admin/products?cat=men"}})
	if w.Code != http.StatusSeeOther || ops.active == nil || *ops.active || len(ops.ids) != 2 {
		t.Fatalf("deactivate: code=%d ops=%+v", w.Code, ops)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/admin/products?cat=men&toast=") || !strings.Contains(loc, url.QueryEscape("изменено 2 из 2")) {
		t.Errorf("Location = %q", loc)
	}

	w = postBulk(h, url.Values{"action": {"category"}, "category_id": {catID}, "id": {uuid1}})
	if ops.category != catID || !strings.HasPrefix(w.Header().Get("Location"), "/admin/products?toast=") {
		t.Errorf("category: ops=%+v loc=%q", ops, w.Header().Get("Location"))
	}

	ops.category = ""
	postBulk(h, url.Values{"action": {"category"}, "category_id": {""}, "id": {uuid1}})
	if ops.category != "" {
		t.Error("empty category accepted")
	}

	w = postBulk(h, url.Values{"action": {"activate"}, "back": {"https://evil.example/"}})
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, "/admin/products?toast=") {
		t.Errorf("no selection / foreign back: Location = %q", loc)
	}
}

func TestRenderProductsListWithPointAndBulkBar(t *testing.T) {
	rr := newTestRenderer(t)
	owner := &staff.Staff{ID: "s1", Name: "Айгерим Б.", Role: staff.RoleOwner, IsActive: true}
	h := &handlers{}
	data := ProductsPageData{
		CanEdit: true, CanDelete: true,
		CategoryChips: []ChipVM{{Label: "Все", URL: "/admin/products", Active: true}},
		Products: []ProductRowVM{{
			ID: uuid1, Name: "Кеды", PriceText: "1 сом", StockLabel: "5 шт", StatusLabel: "Активен",
			PointStockLabel: "Нет в наличии", PointStockFG: "#C62828", PointStockBG: "#FFEBEE",
			EditURL: "/admin/products/" + uuid1, ToggleActiveURL: "/t", DeleteURL: "/d", DeactivateLabel: "Деактивировать",
		}},
		CountLabel: "1 товар", PageSize: 25,
		PointOptions:   []PointOptionVM{{ID: "", Name: "Все точки"}, {ID: "pA", Name: "Центр", Selected: true}},
		PointID:        "pA",
		PointName:      "Центр",
		OutOfStockOnly: true,
		BulkURL:        "/admin/products/bulk",
		ReturnURL:      "/admin/products?point=pA&oos=1",
		BulkCategories: []BulkCategoryOption{{ID: catID, Label: "Кеды"}},
	}
	pd := h.productsShellData("products", "Товары", owner)
	pd.Data = data
	w := httptest.NewRecorder()
	if err := rr.Render(w, "products", pd); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := w.Body.String()
	for _, want := range []string{
		`id="products-bulk"`, `data-bulk hidden`, `name="back" value="/admin/products?point=pA&amp;oos=1"`,
		`class="js-bulk-item" form="products-bulk" name="id" value="` + uuid1 + `"`,
		`В точке: Центр`, `Только нет в наличии в этой точке`, `name="oos" value="1" checked`,
		`<option value="` + catID + `">Кеды</option>`, `/admin/static/js/bulk.js`,
		`name="point" value="pA"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q", want)
		}
	}
}
