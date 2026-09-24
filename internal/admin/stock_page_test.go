package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/points"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

type fakeStockStore struct {
	listPoint string
	rows      []stockPageRow
	applied   []stockCellChange
	applyErr  error
}

func (f *fakeStockStore) List(_ context.Context, pointID, _ string, _ int) ([]stockPageRow, int, error) {
	f.listPoint = pointID
	return f.rows, len(f.rows), nil
}

func (f *fakeStockStore) Apply(_ context.Context, changes []stockCellChange) error {
	f.applied = changes
	return f.applyErr
}

func newStockHandlers(t *testing.T, store *fakeStockStore, pointListings int) *handlers {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for i := 0; i < pointListings; i++ {
		mock.ExpectQuery(`FROM points_of_sale`).WillReturnRows(
			sqlmock.NewRows([]string{"id", "name", "address", "is_active", "created_at"}).
				AddRow("pA", "Главный склад", "ул. 1", true, time.Now()).
				AddRow("pB", "Дордой", "ул. 2", true, time.Now()))
	}
	return &handlers{render: newTestRenderer(t), pointsRepo: points.NewPointsRepo(db), stockStore: store}
}

func TestStockPagePointStaffPinnedToOwnPoint(t *testing.T) {
	store := &fakeStockStore{rows: []stockPageRow{{VariantID: "v1", ProductID: "p1", ProductName: "Nike", Size: "42", Color: "Белый", Quantity: 3, HasStockRow: true}}}
	h := newStockHandlers(t, store, 1)

	w := httptest.NewRecorder()
	h.stockPage(w, requestAs(http.MethodGet, "/admin/stock?point=pA", pointStaff(strPtr("pB")), nil))

	if store.listPoint != "pB" {
		t.Fatalf("listed point = %q, want the staff member's own pB", store.listPoint)
	}
	body := w.Body.String()
	if strings.Contains(body, `id="stock-point"`) {
		t.Error("point_staff must not get a point selector")
	}
	if strings.Contains(body, "/admin/products/p1") {
		t.Error("point_staff must not get product edit links")
	}
	if !strings.Contains(body, `name="qty_v1_pB" value="3"`) || !strings.Contains(body, `name="orig_v1_pB" value="3"`) {
		t.Error("stock cell / original value not rendered")
	}
}

func TestStockSavePointStaffWritesOnlyChangedCellsOfOwnPoint(t *testing.T) {
	store := &fakeStockStore{}
	h := newStockHandlers(t, store, 1)

	form := url.Values{
		"point":      {"pA"}, // ignored for point_staff
		"variant_id": {"v1", "v2"},
		"qty_v1_pB":  {"5"}, "orig_v1_pB": {"3"},
		"qty_v2_pB": {"2"}, "orig_v2_pB": {"2"},
		"qty_v1_pA": {"99"}, "orig_v1_pA": {"0"}, // another point's cell: never read
	}
	w := httptest.NewRecorder()
	h.stockSave(w, requestAs(http.MethodPost, "/admin/stock", pointStaff(strPtr("pB")), form))

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want redirect", w.Code)
	}
	if len(store.applied) != 1 {
		t.Fatalf("applied = %+v, want exactly the one changed cell", store.applied)
	}
	c := store.applied[0]
	if c.RowKey != "v1" || c.PointID != "pB" || c.Qty != 5 || c.Orig == nil || *c.Orig != 3 {
		t.Errorf("change = %+v", c)
	}
}

func TestStockSaveInvalidQtyDoesNotApply(t *testing.T) {
	store := &fakeStockStore{rows: []stockPageRow{{VariantID: "v1", ProductName: "Nike", Quantity: 3, HasStockRow: true}}}
	h := newStockHandlers(t, store, 2)

	form := url.Values{"variant_id": {"v1"}, "qty_v1_pB": {"-1"}, "orig_v1_pB": {"3"}}
	w := httptest.NewRecorder()
	h.stockSave(w, requestAs(http.MethodPost, "/admin/stock", pointStaff(strPtr("pB")), form))

	if store.applied != nil {
		t.Fatal("Apply called with an invalid quantity")
	}
	body := w.Body.String()
	if !strings.Contains(body, "Ничего не сохранено") || !strings.Contains(body, "is-invalid") {
		t.Error("error / invalid mark not rendered")
	}
}

func TestStockSaveConflictShowsCurrentValue(t *testing.T) {
	store := &fakeStockStore{
		rows:     []stockPageRow{{VariantID: "v1", ProductName: "Nike", Quantity: 1, HasStockRow: true}},
		applyErr: &stockConflictError{Cells: []stockConflict{{RowKey: "v1", PointID: "pB", Current: 1, Exists: true}}},
	}
	h := newStockHandlers(t, store, 2)

	form := url.Values{"variant_id": {"v1"}, "qty_v1_pB": {"5"}, "orig_v1_pB": {"3"}}
	w := httptest.NewRecorder()
	h.stockSave(w, requestAs(http.MethodPost, "/admin/stock", pointStaff(strPtr("pB")), form))

	body := w.Body.String()
	if !strings.Contains(body, "Остаток изменился") || !strings.Contains(body, `name="qty_v1_pB" value="1"`) || !strings.Contains(body, "is-conflict") {
		t.Error("conflict not rendered with the current value")
	}
}

func TestStockPageOwnerChoosesPoint(t *testing.T) {
	store := &fakeStockStore{}
	h := newStockHandlers(t, store, 1)
	owner := &staff.Staff{ID: "o", Name: "Айгерим Б.", Role: staff.RoleOwner, IsActive: true}

	w := httptest.NewRecorder()
	h.stockPage(w, requestAs(http.MethodGet, "/admin/stock?point=pB", owner, nil))

	if store.listPoint != "pB" {
		t.Fatalf("listed point = %q, want pB", store.listPoint)
	}
	if !strings.Contains(w.Body.String(), `id="stock-point"`) {
		t.Error("owner should get the point selector")
	}
}

func TestStockPageRepoListQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(`SELECT COUNT\(\*\)\s+FROM product_variants v`).WithArgs("pA", `%50\%%`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`LEFT JOIN stock s ON s.variant_id = v.id AND s.point_id = \$1`).WithArgs("pA", `%50\%%`, stockPageSize, stockPageSize).
		WillReturnRows(sqlmock.NewRows([]string{"v.id", "p.id", "name_ru", "brand", "size", "color", "quantity"}).
			AddRow("v1", "p1", "Nike", "Nike", "42", "Белый", nil))

	rows, total, err := newStockPageRepo(db).List(context.Background(), "pA", "50%", 2)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 || rows[0].HasStockRow || rows[0].Quantity != 0 {
		t.Errorf("rows = %+v, total = %d", rows, total)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
