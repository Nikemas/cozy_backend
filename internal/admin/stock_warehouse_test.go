package admin

import (
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

func storekeeper(pointID *string) *staff.Staff {
	return &staff.Staff{ID: "sk1", Name: "Бакыт К.", Role: staff.RoleStorekeeper, PointID: pointID, IsActive: true}
}

// newWarehouseStockHandlers: pA is a branch, wW a warehouse.
func newWarehouseStockHandlers(t *testing.T, store *fakeStockStore) *handlers {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(`FROM points_of_sale`).WillReturnRows(
		sqlmock.NewRows(pointCols).
			AddRow("pA", "Cozy ЦУМ", "Бишкек", "ул. 1", "", nil, nil, "branch", true, time.Now()).
			AddRow("wW", "Склад Ош", "Ош", "ул. 2", "", nil, nil, "warehouse", true, time.Now()))
	return &handlers{render: newTestRenderer(t), pointsRepo: points.NewPointsRepo(db), stockStore: store}
}

func TestStockPageStorekeeperPinnedToOwnWarehouseWithLocations(t *testing.T) {
	// Arrange
	store := &fakeStockStore{
		rows:      []stockPageRow{{VariantID: "v1", ProductID: "p1", ProductName: "Nike", ModelCode: "NK-42", Size: "42", Color: "Белый", Quantity: 3, HasStockRow: true, LocationID: "l1"}},
		locations: []stockLocation{{ID: "l1", Name: "Стеллаж A", IsActive: true}, {ID: "l2", Name: "Старая полка", IsActive: false}},
	}
	h := newWarehouseStockHandlers(t, store)

	// Act
	w := httptest.NewRecorder()
	h.stockPage(w, requestAs(http.MethodGet, "/admin/stock?point=pA&cat=c2&color=Белый&loc=l1&q=NK", storekeeper(strPtr("wW")), nil))

	// Assert
	if store.listPoint != "wW" {
		t.Fatalf("listed point = %q, want the storekeeper's own wW", store.listPoint)
	}
	if f := store.listFilter; f.CategoryID != "c2" || f.Color != "Белый" || f.LocationID != "l1" || f.Query != "NK" {
		t.Errorf("filter = %+v", f)
	}
	body := w.Body.String()
	for _, want := range []string{`name="loc_v1_wW"`, `name="orig_loc_v1_wW" value="l1"`, `<option value="l1" selected>Стеллаж A</option>`, "NK-42", `id="stock-loc"`, "— Кеды"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// The filter lists the inactive location with a suffix; the row select
	// (current l1) doesn't offer it at all.
	if strings.Count(body, "Старая полка") != 1 {
		t.Error("inactive location offered in the row select")
	}
	if strings.Contains(body, `id="stock-point"`) || strings.Contains(body, "/admin/products/p1") {
		t.Error("storekeeper must get neither a point selector nor product links")
	}
}

func TestStockPageBranchHasNoLocationColumn(t *testing.T) {
	store := &fakeStockStore{rows: []stockPageRow{{VariantID: "v1", ProductName: "Nike", Quantity: 1, HasStockRow: true}}}
	h := newWarehouseStockHandlers(t, store)

	w := httptest.NewRecorder()
	h.stockPage(w, requestAs(http.MethodGet, "/admin/stock?point=pA&loc=l1", ownerStaff(), nil))

	if store.listFilter.LocationID != "" {
		t.Errorf("location filter %q applied to a branch", store.listFilter.LocationID)
	}
	if strings.Contains(w.Body.String(), `name="loc_v1_pA"`) {
		t.Error("branch rows got a location select")
	}
}

func TestStockSaveStorekeeperWritesOnlyChangedLocations(t *testing.T) {
	// Arrange
	store := &fakeStockStore{}
	h := newWarehouseStockHandlers(t, store)
	form := url.Values{
		"point":      {"pA"}, // ignored for a storekeeper
		"variant_id": {"v1", "v2", "v3"},
		"qty_v1_wW":  {"3"}, "orig_v1_wW": {"3"}, "loc_v1_wW": {"l2"}, "orig_loc_v1_wW": {"l1"}, // moved
		"qty_v2_wW": {"2"}, "orig_v2_wW": {"2"}, "loc_v2_wW": {"l1"}, "orig_loc_v2_wW": {"l1"}, // untouched
		"qty_v3_wW": {"4"}, "orig_v3_wW": {"1"}, "loc_v3_wW": {""}, "orig_loc_v3_wW": {"l1"}, // qty + cleared
	}

	// Act
	w := httptest.NewRecorder()
	h.stockSave(w, requestAs(http.MethodPost, "/admin/stock", storekeeper(strPtr("wW")), form))

	// Assert
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want redirect", w.Code)
	}
	if len(store.applied) != 1 || store.applied[0].RowKey != "v3" || store.applied[0].PointID != "wW" {
		t.Errorf("qty changes = %+v", store.applied)
	}
	if len(store.appliedLoc) != 2 {
		t.Fatalf("location changes = %+v, want v1 and v3", store.appliedLoc)
	}
	if c := store.appliedLoc[0]; c.RowKey != "v1" || c.LocationID == nil || *c.LocationID != "l2" {
		t.Errorf("v1 change = %+v", c)
	}
	if c := store.appliedLoc[1]; c.RowKey != "v3" || c.LocationID != nil {
		t.Errorf("v3 change = %+v, want a cleared location", c)
	}
}

func TestParseLocationCell(t *testing.T) {
	cases := []struct {
		name        string
		form        url.Values
		wantChanged bool
		wantLoc     string
	}{
		{"not submitted", url.Values{}, false, ""},
		{"same as rendered", url.Values{"loc": {"l1"}, "orig": {"l1"}}, false, ""},
		{"picked", url.Values{"loc": {"l2"}, "orig": {"l1"}}, true, "l2"},
		{"cleared", url.Values{"loc": {""}, "orig": {"l1"}}, true, ""},
		{"new row, none picked", url.Values{"loc": {""}}, false, ""},
		{"new row, picked", url.Values{"loc": {"l1"}}, true, "l1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			loc, changed := parseLocationCell(c.form, "loc", "orig")
			if changed != c.wantChanged || deref(loc) != c.wantLoc {
				t.Errorf("got (%v, %v), want (%q, %v)", deref(loc), changed, c.wantLoc, c.wantChanged)
			}
		})
	}
}

func TestWarehouseDetailRefusesForeignWarehouseForStorekeeper(t *testing.T) {
	h, mock := newPointsHandlers(t)

	r := requestAs(http.MethodGet, "/admin/warehouses/w2", storekeeper(strPtr("w1")), nil)
	r.SetPathValue("id", "w2")
	w := httptest.NewRecorder()
	h.warehouseDetailPage(w, r)

	if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/admin/stock") {
		t.Fatalf("status = %d, Location = %q; want a redirect away", w.Code, w.Header().Get("Location"))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err) // not even looked up
	}
}

func TestWarehousesPageRedirectsStorekeeperToOwnWarehouse(t *testing.T) {
	h, _ := newPointsHandlers(t)

	w := httptest.NewRecorder()
	h.warehousesPage(w, requestAs(http.MethodGet, "/admin/warehouses", storekeeper(strPtr("w1")), nil))

	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin/warehouses/w1" {
		t.Fatalf("status = %d, Location = %q", w.Code, w.Header().Get("Location"))
	}
}

func TestParseProductFormWarehouseLocation(t *testing.T) {
	pts := []StockPointVM{{ID: "pA", Name: "ЦУМ"}, {ID: "wW", Name: "Склад", Warehouse: true, Locations: []LocationOptionVM{{ID: "l1", Name: "A"}}}}
	form := url.Values{
		"category_id": {"c1"}, "name_ru": {"Кеды"}, "name_ky": {"Кеддер"}, "base_price": {"1000"},
		"variant_key": {"v1", "n1"}, "variant_id": {"v1", ""}, "variant_size": {"42", "43"}, "variant_color": {"Белый", "Белый"},
		"qty_v1_pA": {"1"}, "orig_v1_pA": {"1"}, "qty_v1_wW": {"2"}, "orig_v1_wW": {"2"},
		"loc_v1_wW": {"l1"}, "orig_loc_v1_wW": {""}, // existing row: location picked
		"qty_n1_pA": {"0"}, "qty_n1_wW": {"5"}, "loc_n1_wW": {""}, // new row: none picked
	}

	got := parseProductForm(ruTr, form, "prod", true, pts)

	if len(got.Errs) > 0 {
		t.Fatalf("errs = %v", got.Errs)
	}
	if len(got.Input.Locations) != 1 || got.Input.Locations[0].RowKey != "v1" || deref(got.Input.Locations[0].LocationID) != "l1" {
		t.Errorf("locations = %+v", got.Input.Locations)
	}
	if len(got.Input.Stock) != 1 || got.Input.Stock[0].RowKey != "n1" || got.Input.Stock[0].PointID != "wW" {
		t.Errorf("stock = %+v, want only the new row's warehouse cell", got.Input.Stock)
	}
	if c := got.Rows[0].Cells[1]; !c.Warehouse || c.LocValue != "l1" || c.LocName != "loc_v1_wW" {
		t.Errorf("re-render cell = %+v", c)
	}
}
