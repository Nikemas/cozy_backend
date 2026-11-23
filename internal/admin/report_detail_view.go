package admin

import (
	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/internal/reports"
)

// ReportDetailData backs report_detail.gohtml (screen "report_detail").
type ReportDetailData struct {
	Kind string // reportKindCategory / Brand / Product

	// Toolbar: period (as on the summary page) and the subject picker.
	Periods      []PeriodOption
	Custom       bool
	From, To     string
	PeriodLabel  string
	PeriodParams []HiddenParam
	Action       string        // the form's GET target
	Subject      reportSubject // chosen category / brand / code
	Options      []PickOption  // category/brand select ("" for the code report)

	SummaryURL string
	ExportURL  string
	Err        string
	// HasReport: a subject was resolved and the tables below are filled.
	HasReport bool

	// Category / brand reports.
	Products     []ProductReportRowVM
	ProductTotal ProductReportRowVM

	// Product (code) report.
	Product      *ProductReportHeadVM
	Variants     []VariantReportRowVM
	VariantTotal VariantReportRowVM
	Points       []CategoryBar
	Stock        StockMatrixVM
	// pointRows are Points' raw numbers, same order (Excel export).
	pointRows []reports.Row
}

// ProductReportRowVM is one product line of a category/brand report.
type ProductReportRowVM struct {
	Code      string
	Name      string
	Brand     string
	Inactive  bool
	URL       string // product (code) report
	Orders    int
	Sold      int
	Revenue   float64
	Cancelled int
	SoldLabel string
	SumLabel  string
}

// ProductReportHeadVM is the product report's header.
type ProductReportHeadVM struct {
	Code     string
	Name     string
	Brand    string
	Inactive bool
	EditURL  string
}

// VariantReportRowVM is one size/color line of the product report.
type VariantReportRowVM struct {
	Size      string
	Color     string // in the admin's language
	RawColor  string
	Orders    int
	Sold      int
	Revenue   float64
	Cancelled int
	SoldLabel string
	SumLabel  string
}

// StockMatrixVM is the product's current stock: one row per variant, one
// column per point that holds a stock row for the product.
type StockMatrixVM struct {
	Points []string // column headers
	Rows   []StockMatrixRowVM
	Totals []int // per column
	Total  int
}

// StockMatrixRowVM is one variant's stock across Points.
type StockMatrixRowVM struct {
	Label string // "42 · Чёрный"
	Cells []int
	Total int
}

func productReportRow(t tr, row reports.ProductSalesRow) ProductReportRowVM {
	brand := row.Brand
	if brand == "" && row.Name != "" {
		brand = t.T("admin.reports.no_brand")
	}
	return ProductReportRowVM{
		Code:      row.Code,
		Name:      row.Name,
		Brand:     brand,
		Inactive:  row.Name != "" && !row.IsActive,
		Orders:    row.OrderCount,
		Sold:      row.ItemCount,
		Revenue:   row.Revenue,
		Cancelled: row.CancelledCount,
		SoldLabel: t.F("admin.reports.pcs", row.ItemCount),
		SumLabel:  t.Som(row.Revenue),
	}
}

func buildVariantReportRows(t tr, rows []reports.VariantSalesRow) ([]VariantReportRowVM, VariantReportRowVM) {
	toVM := func(v reports.VariantSalesRow) VariantReportRowVM {
		return VariantReportRowVM{
			Size:      v.Size,
			Color:     i18n.ColorLabel(t.Lang(), v.Color),
			RawColor:  v.Color,
			Orders:    v.OrderCount,
			Sold:      v.ItemCount,
			Revenue:   v.Revenue,
			Cancelled: v.CancelledCount,
			SoldLabel: t.F("admin.reports.pcs", v.ItemCount),
			SumLabel:  t.Som(v.Revenue),
		}
	}
	out := make([]VariantReportRowVM, len(rows))
	for i, v := range rows {
		out[i] = toVM(v)
	}
	return out, toVM(reports.TotalVariantSales(rows))
}

// buildStockMatrix lays out stock cells as variants × points, in the
// variants' order and the cells' point order (warehouses last). Points
// get a "(склад)" / inactive suffix so the columns read unambiguously.
func buildStockMatrix(t tr, variants []reports.VariantSalesRow, cells []reports.StockCell) StockMatrixVM {
	var m StockMatrixVM
	col := map[string]int{}
	for _, c := range cells {
		if _, ok := col[c.PointID]; ok {
			continue
		}
		col[c.PointID] = len(m.Points)
		name := c.PointName
		if c.Warehouse {
			name += " " + t.T("admin.reports.warehouse_suffix")
		}
		if !c.Active {
			name += " " + t.T("admin.common.inactive_suffix")
		}
		m.Points = append(m.Points, name)
	}
	qty := map[string]map[string]int{}
	for _, c := range cells {
		if qty[c.VariantID] == nil {
			qty[c.VariantID] = map[string]int{}
		}
		qty[c.VariantID][c.PointID] = c.Qty
	}

	m.Totals = make([]int, len(m.Points))
	for _, v := range variants {
		row := StockMatrixRowVM{Label: v.Size + " · " + i18n.ColorLabel(t.Lang(), v.Color), Cells: make([]int, len(m.Points))}
		for pointID, i := range col {
			n := qty[v.VariantID][pointID]
			row.Cells[i] = n
			row.Total += n
			m.Totals[i] += n
		}
		m.Total += row.Total
		m.Rows = append(m.Rows, row)
	}
	return m
}
