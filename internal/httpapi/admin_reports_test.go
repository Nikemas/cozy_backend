package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/reports"
)

// fakeSalesRepo is an in-memory salesRepo for handler tests — no database
// involved, mirrors fakeStockUpserter in admin_catalog_test.go.
type fakeSalesRepo struct {
	orders       []orders.Order
	pointNames   map[string]string
	loadErr      error
	pointNameErr error
	categoryRows []reports.Row

	lastFrom, lastTo time.Time
}

// Sales mirrors reports.Repo.Sales over the fake's orders, using the
// in-memory reference aggregation.
func (f *fakeSalesRepo) Sales(ctx context.Context, from, to time.Time, groupBy reports.GroupBy, pointNames map[string]string) ([]reports.Row, error) {
	list, err := f.LoadOrders(ctx, from, to)
	if err != nil {
		return nil, err
	}
	return reports.AggregateSales(list, groupBy, pointNames)
}

func (f *fakeSalesRepo) LoadOrders(_ context.Context, from, to time.Time) ([]orders.Order, error) {
	f.lastFrom, f.lastTo = from, to
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	return f.orders, nil
}

func (f *fakeSalesRepo) PointNames(_ context.Context) (map[string]string, error) {
	if f.pointNameErr != nil {
		return nil, f.pointNameErr
	}
	return f.pointNames, nil
}

func (f *fakeSalesRepo) CategorySales(_ context.Context, from, to time.Time) ([]reports.Row, error) {
	f.lastFrom, f.lastTo = from, to
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	return f.categoryRows, nil
}

func salesReportRequest(query string) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/admin/api/reports/sales?"+query, nil)
}

// --- JSON handler ---

func TestSalesReportJSONHandlerEmptyRangeReturns200WithZeroRows(t *testing.T) {
	repo := &fakeSalesRepo{}
	handler := salesReportJSONHandler(repo)

	rec := httptest.NewRecorder()
	err := handler(rec, salesReportRequest("from=2026-01-01&to=2026-01-31&group_by=day"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp salesReportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad JSON body: %v", err)
	}
	if resp.From != "2026-01-01" || resp.To != "2026-01-31" || resp.GroupBy != "day" {
		t.Fatalf("unexpected envelope: %+v", resp)
	}
	if len(resp.Rows) != 0 {
		t.Fatalf("expected zero rows, got %+v", resp.Rows)
	}
}

func TestSalesReportJSONHandlerAggregatesOrders(t *testing.T) {
	repo := &fakeSalesRepo{
		orders: []orders.Order{
			{
				ID: "o1", Status: orders.StatusDelivered, TotalAmount: 100,
				CreatedAt: time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC),
				Items:     []orders.OrderItem{{ProductNameSnapshot: "Кроссовки", Quantity: 2, Price: 50}},
			},
		},
	}
	handler := salesReportJSONHandler(repo)

	rec := httptest.NewRecorder()
	err := handler(rec, salesReportRequest("from=2026-01-01&to=2026-01-31&group_by=day"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp salesReportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad JSON body: %v", err)
	}
	if len(resp.Rows) != 1 {
		t.Fatalf("expected 1 row, got %+v", resp.Rows)
	}
	row := resp.Rows[0]
	if row.Key != "2026-01-05" || row.OrderCount != 1 || row.ItemCount != 2 || row.Revenue != 100 {
		t.Fatalf("unexpected row: %+v", row)
	}
}

func TestSalesReportJSONHandlerLoadsToExclusiveDayAfterTo(t *testing.T) {
	// LoadOrders takes a [from, to) range, but the "to" query param is
	// inclusive from the caller's point of view — the handler must push the
	// upper bound one day out so the whole "to" calendar day is covered.
	repo := &fakeSalesRepo{}
	handler := salesReportJSONHandler(repo)

	rec := httptest.NewRecorder()
	if err := handler(rec, salesReportRequest("from=2026-01-01&to=2026-01-31&group_by=day")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Report days are Bishkek calendar days (reports.Location), not UTC.
	wantFrom := time.Date(2026, 1, 1, 0, 0, 0, 0, reports.Location)
	wantTo := time.Date(2026, 2, 1, 0, 0, 0, 0, reports.Location)
	if !repo.lastFrom.Equal(wantFrom) {
		t.Errorf("LoadOrders from = %v, want %v", repo.lastFrom, wantFrom)
	}
	if !repo.lastTo.Equal(wantTo) {
		t.Errorf("LoadOrders to = %v, want %v (to + 1 day)", repo.lastTo, wantTo)
	}
}

func TestSalesReportJSONHandlerInvalidGroupBy(t *testing.T) {
	handler := salesReportJSONHandler(&fakeSalesRepo{})

	rec := httptest.NewRecorder()
	err := handler(rec, salesReportRequest("from=2026-01-01&to=2026-01-31&group_by=week"))
	if err == nil {
		t.Fatal("expected an error for an invalid group_by, got nil")
	}
}

func TestSalesReportJSONHandlerFromAfterTo(t *testing.T) {
	handler := salesReportJSONHandler(&fakeSalesRepo{})

	rec := httptest.NewRecorder()
	err := handler(rec, salesReportRequest("from=2026-02-01&to=2026-01-01&group_by=day"))
	if err == nil {
		t.Fatal("expected an error for from > to, got nil")
	}
}

func TestSalesReportJSONHandlerMissingParams(t *testing.T) {
	handler := salesReportJSONHandler(&fakeSalesRepo{})

	cases := []string{
		"to=2026-01-31&group_by=day",
		"from=2026-01-01&group_by=day",
		"from=2026-01-01&to=2026-01-31",
	}
	for _, q := range cases {
		t.Run(q, func(t *testing.T) {
			rec := httptest.NewRecorder()
			err := handler(rec, salesReportRequest(q))
			if err == nil {
				t.Fatalf("expected an error for query %q, got nil", q)
			}
		})
	}
}

func TestSalesReportJSONHandlerOnlyFetchesPointNamesForPointGrouping(t *testing.T) {
	// A repo whose PointNames always fails: group_by=day must not call it at
	// all (so the request still succeeds), while group_by=point must
	// propagate the failure.
	repo := &fakeSalesRepo{pointNameErr: errors.New("boom")}
	handler := salesReportJSONHandler(repo)

	rec := httptest.NewRecorder()
	if err := handler(rec, salesReportRequest("from=2026-01-01&to=2026-01-31&group_by=day")); err != nil {
		t.Fatalf("group_by=day should not call PointNames, got error: %v", err)
	}

	rec2 := httptest.NewRecorder()
	if err := handler(rec2, salesReportRequest("from=2026-01-01&to=2026-01-31&group_by=point")); err == nil {
		t.Fatal("expected group_by=point to propagate a PointNames error")
	}
}

// --- XLSX handler ---

func TestSalesReportXLSXHandlerHeaders(t *testing.T) {
	repo := &fakeSalesRepo{}
	handler := salesReportXLSXHandler(repo)

	rec := httptest.NewRecorder()
	err := handler(rec, salesReportRequest("from=2026-01-01&to=2026-01-31&group_by=day"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	const wantType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	if ct := rec.Header().Get("Content-Type"); ct != wantType {
		t.Errorf("Content-Type = %q, want %q", ct, wantType)
	}
	const wantDisposition = `attachment; filename="sales_report.xlsx"`
	if cd := rec.Header().Get("Content-Disposition"); cd != wantDisposition {
		t.Errorf("Content-Disposition = %q, want %q", cd, wantDisposition)
	}
}

func TestSalesReportXLSXHandlerProducesReadableWorkbook(t *testing.T) {
	repo := &fakeSalesRepo{
		orders: []orders.Order{
			{
				ID: "o1", Status: orders.StatusDelivered, TotalAmount: 100,
				CreatedAt: time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC),
				Items:     []orders.OrderItem{{ProductNameSnapshot: "Кроссовки", Quantity: 2, Price: 50}},
			},
			{
				ID: "o2", Status: orders.StatusDelivered, TotalAmount: 70,
				CreatedAt: time.Date(2026, 1, 6, 10, 0, 0, 0, time.UTC),
				Items:     []orders.OrderItem{{ProductNameSnapshot: "Ботинки", Quantity: 1, Price: 70}},
			},
		},
	}
	handler := salesReportXLSXHandler(repo)

	rec := httptest.NewRecorder()
	if err := handler(rec, salesReportRequest("from=2026-01-01&to=2026-01-31&group_by=day")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Real end-to-end check: parse the bytes back with excelize, the same
	// library that wrote them, to confirm the output is a well-formed
	// workbook with the expected header/row content (no live Postgres on
	// this machine, so this is as close to "open it in Excel" as this task
	// can verify).
	f, err := excelize.OpenReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("excelize could not open the generated file: %v", err)
	}
	defer func() { _ = f.Close() }()

	sheet := f.GetSheetName(0)
	header, err := f.GetRows(sheet)
	if err != nil {
		t.Fatalf("GetRows: %v", err)
	}
	if len(header) != 3 { // 1 header row + 2 data rows
		t.Fatalf("expected 3 rows (header + 2 data), got %d: %+v", len(header), header)
	}
	if header[0][0] != "Дата" {
		t.Fatalf("expected first column header %q, got %q", "Дата", header[0][0])
	}
	if header[1][0] != "2026-01-05" || header[1][1] != "1" || header[1][2] != "2" || header[1][3] != "100" {
		t.Fatalf("unexpected data row 1: %+v", header[1])
	}
	if header[2][0] != "2026-01-06" || header[2][1] != "1" || header[2][2] != "1" || header[2][3] != "70" {
		t.Fatalf("unexpected data row 2: %+v", header[2])
	}
}

func TestSalesReportXLSXHandlerInvalidParamsPropagated(t *testing.T) {
	handler := salesReportXLSXHandler(&fakeSalesRepo{})

	rec := httptest.NewRecorder()
	err := handler(rec, salesReportRequest("from=2026-01-01&to=2026-01-31&group_by=bogus"))
	if err == nil {
		t.Fatal("expected an error for an invalid group_by, got nil")
	}
}

func TestKeyColumnHeaderPerGroupBy(t *testing.T) {
	cases := map[reports.GroupBy]string{
		reports.GroupByDay:     "Дата",
		reports.GroupByProduct: "Товар",
		reports.GroupByPoint:   "Точка",
		groupByCategory:        "Категория",
	}
	for gb, want := range cases {
		if got := keyColumnHeader(i18n.LangRU, gb); got != want {
			t.Errorf("keyColumnHeader(ru, %q) = %q, want %q", gb, got, want)
		}
		if got := keyColumnHeader(i18n.LangKY, gb); got == "" || got == "admin.export.col."+string(gb) {
			t.Errorf("keyColumnHeader(ky, %q) = %q, want a translation", gb, got)
		}
	}
}

func TestSalesXLSXHeadersFollowAdminLanguage(t *testing.T) {
	// Arrange: the admin chose Kyrgyz; the browser itself still says ru.
	handler := salesReportXLSXHandler(&fakeSalesRepo{})
	req := salesReportRequest("from=2026-01-01&to=2026-01-31&group_by=day")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9")
	req.AddCookie(&http.Cookie{Name: "admin_lang", Value: "ky"})
	rec := httptest.NewRecorder()

	// Act
	if err := handler(rec, req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Assert
	f, err := excelize.OpenReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("open workbook: %v", err)
	}
	defer func() { _ = f.Close() }()
	rows, err := f.GetRows(f.GetSheetName(0))
	if err != nil || len(rows) == 0 {
		t.Fatalf("GetRows: %v (%d rows)", err, len(rows))
	}
	want := []string{
		adminText(i18n.LangKY, "admin.export.col.day"),
		adminText(i18n.LangKY, "admin.export.col.orders"),
		adminText(i18n.LangKY, "admin.export.col.items"),
		adminText(i18n.LangKY, "admin.export.col.revenue"),
	}
	if strings.Join(rows[0], "|") != strings.Join(want, "|") {
		t.Errorf("header = %q, want %q", rows[0], want)
	}
	if rows[0][1] == "Заказы" {
		t.Errorf("header still Russian: %q", rows[0])
	}
}

func TestAdminRequestLang(t *testing.T) {
	cases := []struct {
		name, query, cookie, accept, want string
	}{
		{"default", "", "", "", i18n.LangRU},
		{"query wins", "lang=ky", "ru", "ru", i18n.LangKY},
		{"cookie beats accept-language", "", "ky", "ru-RU", i18n.LangKY},
		{"accept-language without cookie", "", "", "ky", i18n.LangKY},
		{"bad cookie falls through", "", "xx", "ky", i18n.LangKY},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/admin/api/reports/sales.xlsx?"+c.query, nil)
			if c.cookie != "" {
				r.AddCookie(&http.Cookie{Name: "admin_lang", Value: c.cookie})
			}
			if c.accept != "" {
				r.Header.Set("Accept-Language", c.accept)
			}
			if got := adminRequestLang(r); got != c.want {
				t.Errorf("adminRequestLang = %q, want %q", got, c.want)
			}
		})
	}
}

func TestSalesReportXLSXEscapesFormulaLikeNames(t *testing.T) {
	malicious := []string{`=HYPERLINK("http://x","y")`, "+1", "-2+3", "@SUM(A1)"}
	var ords []orders.Order
	for i, name := range malicious {
		ords = append(ords, orders.Order{
			ID: fmt.Sprintf("o%d", i), Status: orders.StatusDelivered, TotalAmount: 10,
			CreatedAt: time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC),
			Items:     []orders.OrderItem{{ProductNameSnapshot: name, Quantity: 1, Price: 10}},
		})
	}
	handler := salesReportXLSXHandler(&fakeSalesRepo{orders: ords})

	rec := httptest.NewRecorder()
	if err := handler(rec, salesReportRequest("from=2026-01-01&to=2026-01-31&group_by=product")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()

	sheet := f.GetSheetName(0)
	rows, err := f.GetRows(sheet)
	if err != nil {
		t.Fatalf("GetRows: %v", err)
	}
	got := map[string]bool{}
	for r, row := range rows[1:] {
		got[row[0]] = true
		cell := fmt.Sprintf("A%d", r+2)
		if formula, _ := f.GetCellFormula(sheet, cell); formula != "" {
			t.Errorf("%s holds a formula %q", cell, formula)
		}
		// Counts and revenue stay numeric cells.
		if typ, _ := f.GetCellType(sheet, fmt.Sprintf("D%d", r+2)); typ == excelize.CellTypeSharedString || typ == excelize.CellTypeInlineString {
			t.Errorf("revenue D%d written as text", r+2)
		}
	}
	for _, name := range malicious {
		if !got["'"+name] {
			t.Errorf("name %q not escaped; rows = %q", name, rows)
		}
	}
}
