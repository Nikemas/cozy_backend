package admin

import (
	"bytes"
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/xuri/excelize/v2"

	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/reports"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

const testCategoryID = "11111111-1111-1111-1111-111111111111"

type fakeReportDetails struct {
	products   []reports.ProductSalesRow
	codeSales  reports.ProductCodeSales
	stock      []reports.StockCell
	ref        *reports.ProductRef
	lastArg    string
	lastFrom   time.Time
	lastTo     time.Time
	stockCalls int
}

func (f *fakeReportDetails) CategoryProductSales(_ context.Context, id string, from, to time.Time) ([]reports.ProductSalesRow, error) {
	f.lastArg, f.lastFrom, f.lastTo = id, from, to
	return f.products, nil
}

func (f *fakeReportDetails) BrandProductSales(_ context.Context, brand string, from, to time.Time) ([]reports.ProductSalesRow, error) {
	f.lastArg, f.lastFrom, f.lastTo = brand, from, to
	return f.products, nil
}

func (f *fakeReportDetails) ProductCodeSales(_ context.Context, id string, from, to time.Time, _ map[string]string) (reports.ProductCodeSales, error) {
	f.lastArg, f.lastFrom, f.lastTo = id, from, to
	return f.codeSales, nil
}

func (f *fakeReportDetails) ProductByCode(_ context.Context, code string) (*reports.ProductRef, error) {
	if f.ref != nil && strings.EqualFold(f.ref.Code, code) {
		return f.ref, nil
	}
	return nil, nil
}

func (f *fakeReportDetails) ProductStock(context.Context, string) ([]reports.StockCell, error) {
	f.stockCalls++
	return f.stock, nil
}

func (f *fakeReportDetails) PointNames(context.Context) (map[string]string, error) {
	return map[string]string{"p1": "ЦУМ"}, nil
}

func reportTestTree() []*catalog.Category {
	sub := &catalog.Category{ID: "22222222-2222-2222-2222-222222222222", NameRu: "Кроссовки", Slug: "sneakers"}
	return []*catalog.Category{{ID: testCategoryID, NameRu: "Мужская обувь", Slug: "men", Children: []*catalog.Category{sub}}}
}

func serveReport(t *testing.T, h *handlers, kind string, xlsx bool, target string) *httptest.ResponseRecorder {
	t.Helper()
	st := &staff.Staff{ID: "s1", Name: "Айгерим Б.", Role: staff.RoleManager, IsActive: true}
	r := httptest.NewRequest("GET", target, nil)
	r = r.WithContext(staff.NewContextWithStaff(r.Context(), st))
	w := httptest.NewRecorder()
	h.reportDetailPage(kind, xlsx)(w, r)
	return w
}

func TestCategoryReportPage(t *testing.T) {
	details := &fakeReportDetails{products: []reports.ProductSalesRow{
		{ProductID: "p1", Code: "CZ-00001", Name: "Air Max", Brand: "Nike", IsActive: true, OrderCount: 2, ItemCount: 3, Revenue: 13500, CancelledCount: 1},
		{ProductID: "p2", Code: "ART-7", Name: "Old Boot", Brand: "", IsActive: false, ItemCount: 1, Revenue: 2000},
	}}
	h := &handlers{render: newTestRenderer(t), reportDetails: details, categories: &fakeCategoryRepo{tree: reportTestTree()}}

	w := serveReport(t, h, reportKindCategory, false, "/admin/reports/category?id="+testCategoryID+"&period=custom&from=2026-09-01&to=2026-09-10")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if details.lastArg != testCategoryID {
		t.Errorf("category arg = %q", details.lastArg)
	}
	if want := time.Date(2026, 9, 11, 0, 0, 0, 0, reports.Location); !details.lastTo.Equal(want) {
		t.Errorf("to = %v, want %v (exclusive day after)", details.lastTo, want)
	}
	body := w.Body.String()
	for _, want := range []string{"Мужская обувь", "CZ-00001", "Air Max", "13 500 сом", "15 500 сом", "Без бренда", "(неактивен)",
		"/admin/reports/product?code=CZ-00001", "/admin/reports/category.xlsx?"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q", want)
		}
	}
}

func TestCategoryReportRejectsUnknownCategory(t *testing.T) {
	details := &fakeReportDetails{}
	h := &handlers{render: newTestRenderer(t), reportDetails: details, categories: &fakeCategoryRepo{tree: reportTestTree()}}
	for _, id := range []string{"not-a-uuid", "33333333-3333-3333-3333-333333333333"} {
		w := serveReport(t, h, reportKindCategory, false, "/admin/reports/category?id="+id)
		if !strings.Contains(w.Body.String(), "Категория не найдена") || details.lastArg != "" {
			t.Errorf("id %q: no error shown or query ran (arg %q)", id, details.lastArg)
		}
		if x := serveReport(t, h, reportKindCategory, true, "/admin/reports/category.xlsx?id="+id); x.Code != 400 {
			t.Errorf("id %q: xlsx status %d, want 400", id, x.Code)
		}
	}
}

func TestBrandReportNoBrandBucket(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectQuery("SELECT DISTINCT brand").WillReturnRows(sqlmock.NewRows([]string{"brand"}).AddRow("Nike").AddRow("Nike "))

	details := &fakeReportDetails{}
	h := &handlers{render: newTestRenderer(t), reportDetails: details, products: catalog.NewProductRepo(db)}
	w := serveReport(t, h, reportKindBrand, false, "/admin/reports/brand?brand="+noBrandParam)
	if details.lastArg != "" {
		t.Errorf("brand arg = %q, want \"\" (no brand)", details.lastArg)
	}
	if body := w.Body.String(); strings.Count(body, `<option value="Nike"`) != 1 || !strings.Contains(body, "Без бренда") {
		t.Errorf("brand options wrong:\n%s", body)
	}
}

func TestProductReportPage(t *testing.T) {
	details := &fakeReportDetails{
		ref: &reports.ProductRef{ID: "p1", Code: "CZ-00007", Name: "Air Max", Brand: "Nike", IsActive: true},
		codeSales: reports.ProductCodeSales{
			Variants: []reports.VariantSalesRow{
				{VariantID: "v1", Size: "42", Color: "Чёрный", OrderCount: 2, ItemCount: 2, Revenue: 9000},
				{VariantID: "v2", Size: "43", Color: "Белый"},
			},
			Points: []reports.Row{{Key: "ЦУМ", OrderCount: 2, ItemCount: 2, Revenue: 9000}},
		},
		stock: []reports.StockCell{
			{VariantID: "v1", PointID: "p1", PointName: "ЦУМ", Active: true, Qty: 4},
			{VariantID: "v2", PointID: "w1", PointName: "Главный", Warehouse: true, Active: true, Qty: 10},
		},
	}
	h := &handlers{render: newTestRenderer(t), reportDetails: details}

	w := serveReport(t, h, reportKindProduct, false, "/admin/reports/product?code=cz-00007")
	body := w.Body.String()
	for _, want := range []string{"CZ-00007", "Air Max", "9 000 сом", "Главный (склад)", "/admin/products/p1", "/admin/reports/product.xlsx?"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q", want)
		}
	}
	if details.lastArg != "p1" || details.stockCalls != 1 {
		t.Errorf("lastArg=%q stockCalls=%d", details.lastArg, details.stockCalls)
	}

	missing := serveReport(t, h, reportKindProduct, false, "/admin/reports/product?code=NOPE")
	if !strings.Contains(missing.Body.String(), "«NOPE» не найден") {
		t.Error("not-found message missing")
	}
}

func TestProductReportXLSX(t *testing.T) {
	details := &fakeReportDetails{
		ref: &reports.ProductRef{ID: "p1", Code: "=HYPERLINK(1)", Name: "Air", IsActive: true},
		codeSales: reports.ProductCodeSales{
			Variants: []reports.VariantSalesRow{{VariantID: "v1", Size: "42", Color: "Чёрный", ItemCount: 2, Revenue: 9000}},
			Points:   []reports.Row{{Key: "p9", ItemCount: 2, Revenue: 9000, OrderCount: 1}},
		},
		stock: []reports.StockCell{{VariantID: "v1", PointID: "p1", PointName: "ЦУМ", Active: true, Qty: 4}},
	}
	h := &handlers{render: newTestRenderer(t), reportDetails: details}
	w := serveReport(t, h, reportKindProduct, true, "/admin/reports/product.xlsx?code="+url.QueryEscape("=HYPERLINK(1)"))
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Disposition"), "report_product.xlsx") {
		t.Fatalf("status %d, headers %v", w.Code, w.Header())
	}
	f, err := excelize.OpenReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if got := f.GetSheetList(); len(got) != 3 {
		t.Fatalf("sheets = %v", got)
	}
	title, _ := f.GetCellValue("Вариации", "A1")
	if formula, _ := f.GetCellFormula("Вариации", "A1"); formula != "" || !strings.Contains(title, "HYPERLINK") {
		t.Errorf("title = %q (formula %q)", title, formula)
	}
	sold, _ := f.GetCellValue("Вариации", "C5")
	if sold != "2" {
		t.Errorf("sold cell = %q, want 2", sold)
	}
	stock, _ := f.GetCellValue("Остатки", "B4")
	if stock != "4" {
		t.Errorf("stock cell = %q, want 4", stock)
	}
}

func TestCategoryReportXLSXTotals(t *testing.T) {
	details := &fakeReportDetails{products: []reports.ProductSalesRow{
		{Code: "A", Name: "=cmd", IsActive: true, ItemCount: 3, Revenue: 100},
		{Code: "B", Name: "Two", IsActive: true, ItemCount: 1, Revenue: 50, CancelledCount: 2},
	}}
	h := &handlers{render: newTestRenderer(t), reportDetails: details, categories: &fakeCategoryRepo{tree: reportTestTree()}}
	w := serveReport(t, h, reportKindCategory, true, "/admin/reports/category.xlsx?id="+testCategoryID)
	f, err := excelize.OpenReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("status %d: %v", w.Code, err)
	}
	defer func() { _ = f.Close() }()
	sheet := f.GetSheetName(0)
	name, _ := f.GetCellValue(sheet, "B5")
	if name == "=cmd" || !strings.HasSuffix(name, "=cmd") {
		t.Errorf("formula-like name not escaped: %q", name)
	}
	rows, _ := f.GetRows(sheet)
	last := rows[len(rows)-1]
	if last[0] != "Итого" || last[3] != "4" || last[4] != "150" || last[6] != "2" {
		t.Errorf("total row = %v", last)
	}
}

func TestBuildStockMatrix(t *testing.T) {
	variants := []reports.VariantSalesRow{{VariantID: "v1", Size: "42", Color: "Чёрный"}, {VariantID: "v2", Size: "43", Color: "Белый"}}
	cells := []reports.StockCell{
		{VariantID: "v1", PointID: "a", PointName: "ЦУМ", Active: true, Qty: 2},
		{VariantID: "v2", PointID: "a", PointName: "ЦУМ", Active: true, Qty: 1},
		{VariantID: "v2", PointID: "w", PointName: "Склад", Warehouse: true, Active: false, Qty: 5},
	}
	m := buildStockMatrix(ruTr, variants, cells)
	if len(m.Points) != 2 || m.Points[1] != "Склад (склад) (неактивна)" {
		t.Fatalf("points = %v", m.Points)
	}
	if m.Rows[0].Cells[0] != 2 || m.Rows[0].Cells[1] != 0 || m.Rows[1].Total != 6 || m.Totals[0] != 3 || m.Total != 8 {
		t.Fatalf("matrix = %+v", m)
	}
}

func TestSummaryLinksToDetailReports(t *testing.T) {
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, reports.Location)
	to := time.Date(2026, 9, 10, 0, 0, 0, 0, reports.Location)
	cats := withCategoryLinks([]CategoryBar{{Name: "Мужская"}}, []reports.Row{{Key: "Мужская", ID: testCategoryID}}, "custom", from, to)
	if !strings.Contains(cats[0].URL, "id="+testCategoryID) || !strings.Contains(cats[0].URL, "from=2026-09-01") {
		t.Errorf("category url = %q", cats[0].URL)
	}
	brands := withBrandLinks([]BrandBar{{Name: "Без бренда"}, {Name: "Nike"}}, []reports.Row{{Key: ""}, {Key: "Nike"}}, "week", from, to)
	if !strings.Contains(brands[0].URL, "brand="+noBrandParam) || !strings.Contains(brands[1].URL, "brand=Nike") || strings.Contains(brands[1].URL, "from=") {
		t.Errorf("brand urls = %q, %q", brands[0].URL, brands[1].URL)
	}
}
