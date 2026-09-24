package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/Nikemas/cozy_backend/internal/reports"
)

func TestSalesReportJSONGroupByCategory(t *testing.T) {
	repo := &fakeSalesRepo{categoryRows: []reports.Row{
		{Key: "Кроссовки", OrderCount: 3, ItemCount: 5, Revenue: 25000},
		{Key: "Сапоги", OrderCount: 1, ItemCount: 1, Revenue: 7000},
	}}
	rec := httptest.NewRecorder()
	if err := salesReportJSONHandler(repo)(rec, salesReportRequest("from=2026-09-01&to=2026-09-30&group_by=category")); err != nil {
		t.Fatal(err)
	}
	var body salesReportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.GroupBy != "category" || len(body.Rows) != 2 || body.Rows[0].Key != "Кроссовки" || body.Rows[0].Revenue != 25000 {
		t.Errorf("body = %+v", body)
	}
	// [from, to] calendar days -> [from, to+1) in Bishkek time.
	if repo.lastFrom.Format("2006-01-02") != "2026-09-01" || repo.lastTo.Format("2006-01-02") != "2026-10-01" {
		t.Errorf("range = %v .. %v", repo.lastFrom, repo.lastTo)
	}
}

func TestSalesReportJSONGroupByCategoryEmptyIsArray(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := salesReportJSONHandler(&fakeSalesRepo{})(rec, salesReportRequest("from=2026-09-01&to=2026-09-30&group_by=category")); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"rows":[]`)) {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestSalesReportXLSXGroupByCategory(t *testing.T) {
	repo := &fakeSalesRepo{categoryRows: []reports.Row{{Key: "Кроссовки", OrderCount: 3, ItemCount: 5, Revenue: 25000}}}
	rec := httptest.NewRecorder()
	if err := salesReportXLSXHandler(repo)(rec, salesReportRequest("from=2026-09-01&to=2026-09-30&group_by=category")); err != nil {
		t.Fatal(err)
	}
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="sales_by_category.xlsx"` {
		t.Errorf("Content-Disposition = %q", got)
	}
	f, err := excelize.OpenReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := f.GetRows(f.GetSheetName(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0][0] != "Категория" || rows[1][0] != "Кроссовки" || rows[1][3] != "25000" {
		t.Errorf("rows = %v", rows)
	}
}
