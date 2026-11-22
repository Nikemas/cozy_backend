package admin

import (
	"fmt"
	"net/http"

	"github.com/xuri/excelize/v2"

	"github.com/Nikemas/cozy_backend/internal/xlsxsafe"
)

// writeReportDetailXLSX streams a detail report as .xlsx: the category/
// brand report as one sheet of products plus a total row; the product
// report as three sheets (variants, points of sale, current stock). Text
// cells (names, codes, brands, colors) go through xlsxsafe — they are
// staff/import-entered data; counts and money stay numeric.
func writeReportDetailXLSX(w http.ResponseWriter, t tr, d ReportDetailData) error {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	first := f.GetSheetName(0)
	var err error
	if d.Kind == reportKindProduct {
		err = writeProductReportSheets(f, first, t, d)
	} else {
		err = writeProductListSheet(f, first, t, d)
	}
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="report_`+d.Kind+`.xlsx"`)
	return f.Write(w)
}

// sheetWriter appends rows to one sheet, remembering the first error.
type sheetWriter struct {
	f     *excelize.File
	sheet string
	row   int
	err   error
}

func (s *sheetWriter) add(values ...any) {
	if s.err != nil {
		return
	}
	s.row++
	safe := xlsxsafe.Row(values)
	s.err = s.f.SetSheetRow(s.sheet, fmt.Sprintf("A%d", s.row), &safe)
}

// title writes the report's heading lines (subject, period) and a blank row.
func (s *sheetWriter) title(t tr, d ReportDetailData) {
	s.add(t.T(reportTitleKey(d.Kind)) + ": " + d.Subject.Title)
	s.add(d.PeriodLabel)
	s.add()
}

func writeProductListSheet(f *excelize.File, sheet string, t tr, d ReportDetailData) error {
	s := &sheetWriter{f: f, sheet: sheet}
	s.title(t, d)
	s.add(t.T("admin.reports.col_code"), t.T("admin.reports.col_product"), t.T("admin.reports.col_brand"),
		t.T("admin.reports.col_sold"), t.T("admin.reports.revenue"), t.T("admin.reports.col_orders"), t.T("admin.reports.col_cancelled"))
	for _, p := range d.Products {
		s.add(p.Code, p.Name, p.Brand, p.Sold, p.Revenue, p.Orders, p.Cancelled)
	}
	tot := d.ProductTotal
	s.add(t.T("admin.reports.total"), "", "", tot.Sold, tot.Revenue, "", tot.Cancelled)
	return s.err
}

func writeProductReportSheets(f *excelize.File, first string, t tr, d ReportDetailData) error {
	variants := t.T("admin.reports.sheet_variants")
	if err := f.SetSheetName(first, variants); err != nil {
		return err
	}
	s := &sheetWriter{f: f, sheet: variants}
	s.title(t, d)
	s.add(t.T("admin.reports.col_size"), t.T("admin.reports.col_color"),
		t.T("admin.reports.col_sold"), t.T("admin.reports.revenue"), t.T("admin.reports.col_orders"), t.T("admin.reports.col_cancelled"))
	for _, v := range d.Variants {
		s.add(v.Size, v.Color, v.Sold, v.Revenue, v.Orders, v.Cancelled)
	}
	s.add(t.T("admin.reports.total"), "", d.VariantTotal.Sold, d.VariantTotal.Revenue, "", d.VariantTotal.Cancelled)
	if s.err != nil {
		return s.err
	}

	points := t.T("admin.reports.sheet_points")
	if _, err := f.NewSheet(points); err != nil {
		return err
	}
	s = &sheetWriter{f: f, sheet: points}
	s.title(t, d)
	s.add(t.T("admin.common.point_of_sale"), t.T("admin.reports.col_orders"), t.T("admin.reports.col_sold"), t.T("admin.reports.revenue"))
	for i, p := range d.Points {
		if i < len(d.pointRows) {
			r := d.pointRows[i]
			s.add(p.Name, r.OrderCount, r.ItemCount, r.Revenue)
		}
	}
	if s.err != nil {
		return s.err
	}

	stock := t.T("admin.reports.sheet_stock")
	if _, err := f.NewSheet(stock); err != nil {
		return err
	}
	s = &sheetWriter{f: f, sheet: stock}
	s.add(t.T("admin.reports.stock_now") + ": " + d.Subject.Title)
	s.add()
	header := []any{t.T("admin.reports.col_variant")}
	for _, p := range d.Stock.Points {
		header = append(header, p)
	}
	s.add(append(header, t.T("admin.reports.total"))...)
	for _, r := range d.Stock.Rows {
		row := []any{r.Label}
		for _, n := range r.Cells {
			row = append(row, n)
		}
		s.add(append(row, r.Total)...)
	}
	totals := []any{t.T("admin.reports.total")}
	for _, n := range d.Stock.Totals {
		totals = append(totals, n)
	}
	s.add(append(totals, d.Stock.Total)...)
	return s.err
}
