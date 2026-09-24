package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/reports"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// salesRepo is the subset of *reports.Repo the report handlers depend on, so
// handler-level tests can inject a fake instead of a live database —
// mirrors stockUpserter in admin_catalog.go.
type salesRepo interface {
	LoadOrders(ctx context.Context, from, to time.Time) ([]orders.Order, error)
	PointNames(ctx context.Context) (map[string]string, error)
	CategorySales(ctx context.Context, from, to time.Time) ([]reports.Row, error)
}

// groupByCategory is the fix/admin-ops addition to reports.GroupBy for
// these endpoints: sales per top-level catalog category (a subcategory
// counts toward its parent), via reports.Repo.CategorySales — the same
// numbers as the HTML report's "По категориям" block.
const groupByCategory reports.GroupBy = "category"

// RegisterAdminReportsRoutes mounts the admin sales report endpoints under
// /admin/api/reports/*, per Task O / §8 and §14 of the ТЗ. Owner/manager
// only — a point_staff member has no business use case for a cross-point
// sales report, so they get 403 (via staffSvc.RequireRole, same as every
// other admin-only route in this package).
func RegisterAdminReportsRoutes(mux *http.ServeMux, db *sql.DB, staffSvc *staff.Service) {
	repo := reports.NewRepo(db)
	ownerOrManager := staffSvc.RequireRole(staff.RoleOwner, staff.RoleManager)

	mux.Handle("GET /admin/api/reports/sales", ownerOrManager(apperr.Wrap(salesReportJSONHandler(repo))))
	mux.Handle("GET /admin/api/reports/sales.xlsx", ownerOrManager(apperr.Wrap(salesReportXLSXHandler(repo))))
}

// salesReportJSONHandler and salesReportXLSXHandler are the JSON and Excel
// variants of the same report — same query params, same underlying rows,
// different rendering. Keeping them as two thin handlers (rather than one
// handler branching on an Accept header) matches this codebase's one
// route-per-representation convention (see e.g. sales.xlsx being its own
// route rather than a ?format=xlsx query param).

func salesReportJSONHandler(repo salesRepo) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		from, to, groupBy, err := parseSalesReportQuery(r)
		if err != nil {
			return err
		}

		rows, err := loadSalesRows(r.Context(), repo, from, to, groupBy)
		if err != nil {
			return err
		}

		return writeJSON(w, http.StatusOK, salesReportResponse{
			From:    from.Format(reportDateLayout),
			To:      to.Format(reportDateLayout),
			GroupBy: string(groupBy),
			Rows:    toRowResponses(rows),
		})
	}
}

func salesReportXLSXHandler(repo salesRepo) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		from, to, groupBy, err := parseSalesReportQuery(r)
		if err != nil {
			return err
		}

		rows, err := loadSalesRows(r.Context(), repo, from, to, groupBy)
		if err != nil {
			return err
		}

		return writeSalesXLSX(w, groupBy, rows)
	}
}

const reportDateLayout = "2006-01-02"

// parseSalesReportQuery parses and validates the from/to/group_by query
// params shared by both report endpoints. All the actual validation rules
// live in package reports as pure functions (ParseReportDate/ValidateRange/
// ParseGroupBy) — this just wires the query string to them.
func parseSalesReportQuery(r *http.Request) (from, to time.Time, groupBy reports.GroupBy, err error) {
	q := r.URL.Query()

	from, err = reports.ParseReportDate(q.Get("from"))
	if err != nil {
		return time.Time{}, time.Time{}, "", err
	}
	to, err = reports.ParseReportDate(q.Get("to"))
	if err != nil {
		return time.Time{}, time.Time{}, "", err
	}
	if err := reports.ValidateRange(from, to); err != nil {
		return time.Time{}, time.Time{}, "", err
	}

	if q.Get("group_by") == string(groupByCategory) {
		return from, to, groupByCategory, nil
	}
	groupBy, err = reports.ParseGroupBy(q.Get("group_by"))
	if err != nil {
		return time.Time{}, time.Time{}, "", apperr.BadRequest("invalid_group_by", "group_by должен быть day, product, point или category")
	}

	return from, to, groupBy, nil
}

// loadSalesRows loads orders in [from, to] (from/to are calendar dates, both
// inclusive from the caller's point of view — LoadOrders itself takes a
// [from, to) range, so the upper bound is pushed one day out here to cover
// all of the "to" day) and aggregates them. Point names are only fetched
// when grouping by point, since that's the only mode that needs them.
func loadSalesRows(ctx context.Context, repo salesRepo, from, to time.Time, groupBy reports.GroupBy) ([]reports.Row, error) {
	loadTo := to.AddDate(0, 0, 1)

	if groupBy == groupByCategory {
		rows, err := repo.CategorySales(ctx, from, loadTo)
		if rows == nil && err == nil {
			rows = []reports.Row{}
		}
		return rows, err
	}

	ordersList, err := repo.LoadOrders(ctx, from, loadTo)
	if err != nil {
		return nil, err
	}

	var pointNames map[string]string
	if groupBy == reports.GroupByPoint {
		pointNames, err = repo.PointNames(ctx)
		if err != nil {
			return nil, err
		}
	}

	return reports.AggregateSales(ordersList, groupBy, pointNames)
}

// --- JSON representation ---

type salesReportResponse struct {
	From    string         `json:"from"`
	To      string         `json:"to"`
	GroupBy string         `json:"group_by"`
	Rows    []salesRowJSON `json:"rows"`
}

type salesRowJSON struct {
	Key        string  `json:"key"`
	OrderCount int     `json:"order_count"`
	ItemCount  int     `json:"item_count"`
	Revenue    float64 `json:"revenue"`
}

func toRowResponses(rows []reports.Row) []salesRowJSON {
	out := make([]salesRowJSON, len(rows))
	for i, row := range rows {
		out[i] = salesRowJSON{Key: row.Key, OrderCount: row.OrderCount, ItemCount: row.ItemCount, Revenue: row.Revenue}
	}
	return out
}

// --- Excel representation ---

// keyColumnHeader returns groupBy's column header for the first column of
// the Excel export. ParseGroupBy already rejects anything else before this
// is ever called, so the default case is unreachable in practice.
func keyColumnHeader(groupBy reports.GroupBy) string {
	switch groupBy {
	case reports.GroupByDay:
		return "Дата"
	case reports.GroupByProduct:
		return "Товар"
	case reports.GroupByPoint:
		return "Точка"
	case groupByCategory:
		return "Категория"
	default:
		return "Ключ"
	}
}

// writeSalesXLSX renders rows as a single-sheet .xlsx workbook straight
// into the response body, with the headers an admin's browser needs to
// download it as a file rather than render it inline.
func writeSalesXLSX(w http.ResponseWriter, groupBy reports.GroupBy, rows []reports.Row) error {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	sheet := f.GetSheetName(0)
	header := []interface{}{keyColumnHeader(groupBy), "Заказы", "Товары", "Выручка"}
	if err := f.SetSheetRow(sheet, "A1", &header); err != nil {
		return err
	}

	for i, row := range rows {
		cell := fmt.Sprintf("A%d", i+2)
		values := []interface{}{row.Key, row.OrderCount, row.ItemCount, row.Revenue}
		if err := f.SetSheetRow(sheet, cell, &values); err != nil {
			return err
		}
	}

	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	filename := "sales_report.xlsx"
	if groupBy == groupByCategory {
		filename = "sales_by_category.xlsx"
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	return f.Write(w)
}
