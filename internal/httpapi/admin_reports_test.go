package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

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
	}
	for gb, want := range cases {
		if got := keyColumnHeader(gb); got != want {
			t.Errorf("keyColumnHeader(%q) = %q, want %q", gb, got, want)
		}
	}
}
