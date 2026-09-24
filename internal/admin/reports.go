// reports.go implements Task 4 (Отчёты, Wave 4) — Cozy Admin.dc.html lines
// 558-618: a period selector + "Экспорт в Excel" link, 4 stat cards, a
// pure-CSS day-by-day bar chart, a "Топ товаров" ranked list and a "По
// брендам" list with mini progress bars. All of it is a presentation over
// internal/reports.AggregateSales' output (Wave 3, Task O) grouped
// different ways, plus one extra query (BrandSales, below) for the one
// dimension AggregateSales doesn't group by.
package admin

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/reports"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// reportsBackend is the subset of behavior reportsPage depends on — an
// interface (mirroring salesRepo in internal/httpapi/admin_reports.go) so
// the handler can be tested with a fake instead of a live database.
// *reportsRepo satisfies it against a real *sql.DB.
type reportsBackend interface {
	LoadOrders(ctx context.Context, from, to time.Time) ([]orders.Order, error)
	BrandSales(ctx context.Context, from, to time.Time) ([]reports.Row, error)
	CategorySales(ctx context.Context, from, to time.Time) ([]reports.Row, error)
}

// reportsRepo adapts *reports.Repo (Wave 3, Task O) for this screen,
// adding BrandSales — a grouping AggregateSales doesn't offer today. See
// BrandSales' doc comment for why.
type reportsRepo struct {
	*reports.Repo
	db *sql.DB
}

func newReportsRepo(db *sql.DB) *reportsRepo {
	return &reportsRepo{Repo: reports.NewRepo(db), db: db}
}

// BrandSales aggregates order line items by product brand over [from, to)
// — the "По брендам" list's data source (Cozy Admin.dc.html lines
// 597-608). AggregateSales can't produce this from an already-loaded
// []orders.Order: orders.OrderItem only snapshots a product name, size and
// color at checkout time — not brand — so brand can't be recovered from
// those in-memory rows. Brand IS a real, stored column
// (internal/catalog/product.go's Product.Brand), reachable via
// order_items.variant_id -> product_variants.product_id -> products.brand,
// so this queries that join directly instead of substituting a fake
// breakdown. Revenue is computed the same way AggregateSales's
// GroupByProduct does — line price × quantity, not the order's
// total_amount — so brand rows sum to the same total as the "Топ товаров"
// rows. Only orders that count as sales are included (reports.
// SaleConditionSQL: not cancelled, online-card only once paid), same as
// AggregateSales. Products
// with no brand set (or an all-whitespace one) roll up into a "Без
// бренда" bucket rather than being dropped.
func (r *reportsRepo) BrandSales(ctx context.Context, from, to time.Time) ([]reports.Row, error) {
	q := `
		SELECT COALESCE(NULLIF(TRIM(p.brand), ''), 'Без бренда') AS brand,
		       COUNT(DISTINCT oi.order_id) AS order_count,
		       COALESCE(SUM(oi.quantity), 0) AS item_count,
		       COALESCE(SUM(oi.price * oi.quantity), 0) AS revenue
		FROM order_items oi
		JOIN orders o ON o.id = oi.order_id
		JOIN product_variants pv ON pv.id = oi.variant_id
		JOIN products p ON p.id = pv.product_id
		WHERE o.created_at >= $1 AND o.created_at < $2 AND ` + reports.SaleConditionSQL + `
		GROUP BY brand
		ORDER BY revenue DESC`

	rows, err := r.db.QueryContext(ctx, q, from, to)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []reports.Row
	for rows.Next() {
		var row reports.Row
		if err := rows.Scan(&row.Key, &row.OrderCount, &row.ItemCount, &row.Revenue); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// --- Period selection ---

// reportPeriod is one option in the period <select> (design canvas's
// `period`/`onPeriod` binding). The canvas mocks specific calendar months
// ("01.09.2026 — 30.09.2026"); Task 4's acceptance criteria explicitly
// leave the exact set to this implementation, so relative, always-valid
// ranges are used instead of hardcoded months.
type reportPeriod struct {
	key   string
	label string
}

var reportPeriods = []reportPeriod{
	{"week", "Последние 7 дней"},
	{"month", "Текущий месяц"},
	{"prev_month", "Прошлый месяц"},
	{"custom", "Произвольный период"},
}

// maxCustomReportDays caps a custom range (one year) so a typo'd year
// doesn't load a decade of orders into memory.
const maxCustomReportDays = 366

func defaultReportPeriod() string { return reportPeriods[0].key }

func isValidReportPeriod(key string) bool {
	for _, p := range reportPeriods {
		if p.key == key {
			return true
		}
	}
	return false
}

// reportRange resolves period into a [from, to] calendar-day range, both
// bounds inclusive midnights in the shop's timezone (reports.Location —
// Asia/Bishkek), anchored to now. Callers loading orders with it must push
// "to" one day out first — see reports.Repo.LoadOrders's doc comment.
func reportRange(period string, now time.Time) (from, to time.Time) {
	loc := reports.Location
	now = now.In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	switch period {
	case "month":
		from = time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, loc)
		to = today
	case "prev_month":
		firstOfThisMonth := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, loc)
		to = firstOfThisMonth.AddDate(0, 0, -1)
		from = time.Date(to.Year(), to.Month(), 1, 0, 0, 0, 0, loc)
	default: // "week"
		from = today.AddDate(0, 0, -6)
		to = today
	}
	return from, to
}

// --- Handler ---

// reportsPage handles GET /admin/reports (owner/manager only, per
// RegisterRoutes' ownerOrManager gate) — replaces Foundation's stub. See
// the package doc comment above and Cozy Admin.dc.html lines 558-618 for
// the screen this renders.
func (h *handlers) reportsPage(w http.ResponseWriter, r *http.Request) {
	st, _ := staff.FromContext(r.Context())

	q := r.URL.Query()
	period := q.Get("period")
	if !isValidReportPeriod(period) {
		period = defaultReportPeriod()
	}

	var from, to time.Time
	var rangeErr string
	if period == "custom" {
		var err error
		from, to, err = parseCustomReportRange(q.Get("from"), q.Get("to"))
		if err != nil {
			rangeErr = appErrMessage(err)
			period = defaultReportPeriod()
		}
	}
	if period != "custom" {
		from, to = reportRange(period, time.Now())
	}

	reportData, err := h.buildReportsData(r.Context(), period, from, to)
	if err != nil {
		http.Error(w, "не удалось построить отчёт", http.StatusInternalServerError)
		return
	}
	reportData.Custom = period == "custom"
	reportData.From = from.Format("2006-01-02")
	reportData.To = to.Format("2006-01-02")
	reportData.Err = rangeErr

	data := h.shellPageData("reports", "Отчёты", st)
	data.Data = reportData
	if err := h.render.Render(w, "reports", data); err != nil {
		http.Error(w, "ошибка рендеринга страницы", http.StatusInternalServerError)
	}
}

// parseCustomReportRange validates the custom period's from/to dates
// (YYYY-MM-DD, Bishkek days, from <= to, at most maxCustomReportDays).
func parseCustomReportRange(fromStr, toStr string) (from, to time.Time, err error) {
	from, err = reports.ParseReportDate(fromStr)
	if err != nil {
		return from, to, err
	}
	to, err = reports.ParseReportDate(toStr)
	if err != nil {
		return from, to, err
	}
	if err := reports.ValidateRange(from, to); err != nil {
		return from, to, err
	}
	if to.Sub(from) > maxCustomReportDays*24*time.Hour {
		return from, to, apperr.BadRequest("range_too_long", "период не может быть длиннее года")
	}
	return from, to, nil
}

// buildReportsData loads orders for [from, to] (both inclusive from the
// caller's point of view) and shapes them into everything the template
// needs. Day/product groupings reuse the exact same []orders.Order load
// (one query), since the stat cards, bar chart and "Топ товаров" are all
// just different views of it; BrandSales is the one extra query, for the
// one dimension AggregateSales doesn't group by.
func (h *handlers) buildReportsData(ctx context.Context, period string, from, to time.Time) (ReportsData, error) {
	loadTo := to.AddDate(0, 0, 1) // LoadOrders' range is [from, to) — see reports.Repo.LoadOrders

	ordersList, err := h.reports.LoadOrders(ctx, from, loadTo)
	if err != nil {
		return ReportsData{}, err
	}

	dayRows, err := reports.AggregateSales(ordersList, reports.GroupByDay, nil)
	if err != nil {
		return ReportsData{}, err
	}
	productRows, err := reports.AggregateSales(ordersList, reports.GroupByProduct, nil)
	if err != nil {
		return ReportsData{}, err
	}
	brandRows, err := h.reports.BrandSales(ctx, from, loadTo)
	if err != nil {
		return ReportsData{}, err
	}
	categoryRows, err := h.reports.CategorySales(ctx, from, loadTo)
	if err != nil {
		return ReportsData{}, err
	}

	return ReportsData{
		Periods:     reportPeriodOptions(period),
		ExportURL:   reportExportURL(from, to),
		Stats:       buildStatCards(dayRows),
		ChartNote:   from.Format("02.01.2006") + " — " + to.Format("02.01.2006"),
		Bars:        buildBars(dayRows, from, to),
		TopProducts: buildTopProducts(productRows),
		TopBrands:   buildTopBrands(brandRows),
		Categories:  buildCategoryBars(categoryRows),
	}, nil
}

// --- View types (ReportsData is the "reports" screen's PageData.Data) ---

// ReportsData is the reports screen's Data payload. Section by section it
// mirrors Cozy Admin.dc.html lines 558-618: Periods+ExportURL is the
// toolbar, Stats the 4-card grid, ChartNote+Bars the day-by-day bar chart,
// TopProducts/TopBrands the two ranked lists.
type ReportsData struct {
	Periods   []PeriodOption
	ExportURL string

	Stats []StatCard

	ChartNote string
	Bars      []ReportBar

	TopProducts []RankedRow
	TopBrands   []BrandBar
	Categories  []CategoryBar

	// Custom-period state: Custom shows the from/to inputs, From/To are
	// the resolved range (YYYY-MM-DD), Err a rejected custom range.
	Custom bool
	From   string
	To     string
	Err    string
}

// CategoryBar is one row of the "По категориям" report.
type CategoryBar struct {
	Name     string
	Sum      string
	Qty      string
	Orders   string
	WidthPct int
}

// PeriodOption is one <option> in the period <select>.
type PeriodOption struct {
	Value    string
	Label    string
	Selected bool
}

// StatCard is one of the 4 stat-card-grid tiles.
type StatCard struct {
	Label string
	Value string
	Note  string
}

// ReportBar is one column of the day-by-day bar chart — HeightPct is
// computed server-side (0-100, relative to the period's tallest day) so
// the template can render it as a plain CSS height with no JS charting
// library involved.
type ReportBar struct {
	Day       string // short day label, e.g. "05.09"
	HeightPct int    // 0-100
	Revenue   string // formatted revenue, shown as the bar's title attr
}

// RankedRow is one row of the "Топ товаров" list.
type RankedRow struct {
	Rank int
	Name string
	Qty  string
}

// BrandBar is one row of the "По брендам" list — WidthPct drives its mini
// progress bar, relative to the period's top-selling brand.
type BrandBar struct {
	Name     string
	Sum      string
	WidthPct int
}

// --- Builders (pure functions over reports.Row, unit-testable) ---

func reportPeriodOptions(selected string) []PeriodOption {
	out := make([]PeriodOption, len(reportPeriods))
	for i, p := range reportPeriods {
		out[i] = PeriodOption{Value: p.key, Label: p.label, Selected: p.key == selected}
	}
	return out
}

// reportExportURL builds the "Экспорт в Excel" link's target — the
// already-working /admin/api/reports/sales.xlsx endpoint (Wave 3), with
// the currently selected period's date range. group_by=day matches the
// "Продажи по дням" section this page leads with; a manager wanting the
// product/point breakdown instead can still hit the JSON/xlsx API's own
// group_by param directly.
func reportExportURL(from, to time.Time) string {
	v := url.Values{}
	v.Set("from", from.Format("2006-01-02"))
	v.Set("to", to.Format("2006-01-02"))
	v.Set("group_by", "day")
	return "/admin/api/reports/sales.xlsx?" + v.Encode()
}

// buildStatCards derives the 4 stat-card numbers from the same day-grouped
// rows the bar chart uses — no separate query needed (per the task brief's
// "Data shaping notes").
func buildStatCards(dayRows []reports.Row) []StatCard {
	var totalRevenue float64
	var orderCount, itemCount int
	for _, row := range dayRows {
		totalRevenue += row.Revenue
		orderCount += row.OrderCount
		itemCount += row.ItemCount
	}
	var avgOrder float64
	if orderCount > 0 {
		avgOrder = totalRevenue / float64(orderCount)
	}

	return []StatCard{
		{Label: "Выручка", Value: formatMoney(totalRevenue), Note: "за период"},
		{Label: "Заказы", Value: strconv.Itoa(orderCount), Note: "за период"},
		{Label: "Средний чек", Value: formatMoney(avgOrder), Note: "на заказ"},
		{Label: "Продано товаров", Value: strconv.Itoa(itemCount) + " шт.", Note: "за период"},
	}
}

// buildBars renders one bar per calendar day in [from, to] — AggregateSales
// only emits a Row for days that actually had orders, so this fills the
// gaps with zero-height bars rather than skipping them (a day with no
// sales should still show up as an empty column, not disappear from the
// chart).
func buildBars(dayRows []reports.Row, from, to time.Time) []ReportBar {
	byDay := make(map[string]reports.Row, len(dayRows))
	maxRevenue := 0.0
	for _, row := range dayRows {
		byDay[row.Key] = row
		if row.Revenue > maxRevenue {
			maxRevenue = row.Revenue
		}
	}

	var bars []ReportBar
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		row := byDay[d.Format("2006-01-02")]

		pct := 0
		if maxRevenue > 0 {
			pct = int(math.Round(row.Revenue / maxRevenue * 100))
		}
		if row.Revenue > 0 && pct < 2 {
			pct = 2 // keep a visible sliver for a nonzero day dwarfed by the period's peak
		}

		bars = append(bars, ReportBar{
			Day:       d.Format("02.01"),
			HeightPct: pct,
			Revenue:   formatMoney(row.Revenue),
		})
	}
	return bars
}

// reportsTopLimit caps both ranked lists at 5 rows, matching the design
// canvas's hint-placeholder-count for topProducts/topBrands.
const reportsTopLimit = 5

// buildTopProducts re-ranks AggregateSales' product rows (which come back
// key-sorted, i.e. alphabetical by product name) by units sold descending
// — "Топ товаров" is a ranking, not an alphabetical list.
func buildTopProducts(productRows []reports.Row) []RankedRow {
	rows := append([]reports.Row(nil), productRows...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ItemCount > rows[j].ItemCount })
	if len(rows) > reportsTopLimit {
		rows = rows[:reportsTopLimit]
	}

	out := make([]RankedRow, len(rows))
	for i, row := range rows {
		out[i] = RankedRow{Rank: i + 1, Name: row.Key, Qty: fmt.Sprintf("%d шт.", row.ItemCount)}
	}
	return out
}

// buildTopBrands takes BrandSales' rows (already revenue-sorted by the
// SQL query's ORDER BY) and derives each mini progress bar's width
// relative to the top brand's revenue.
func buildTopBrands(brandRows []reports.Row) []BrandBar {
	rows := brandRows
	if len(rows) > reportsTopLimit {
		rows = rows[:reportsTopLimit]
	}

	maxRevenue := 0.0
	if len(rows) > 0 {
		maxRevenue = rows[0].Revenue
	}

	out := make([]BrandBar, len(rows))
	for i, row := range rows {
		pct := 0
		if maxRevenue > 0 {
			pct = int(math.Round(row.Revenue / maxRevenue * 100))
		}
		out[i] = BrandBar{Name: row.Key, Sum: formatMoney(row.Revenue), WidthPct: pct}
	}
	return out
}

// formatMoney is defined once for the package in products_view.go (the
// same KGS thousands-grouped, no-decimals formatting internal/web's own
// formatMoney uses) — reused here rather than redefined.

// buildCategoryBars shapes reports.Repo.CategorySales' rows (already
// revenue-sorted) — every category, not capped like the top-5 lists, since
// a shoe shop has only a handful of top-level categories.
func buildCategoryBars(rows []reports.Row) []CategoryBar {
	maxRevenue := 0.0
	if len(rows) > 0 {
		maxRevenue = rows[0].Revenue
	}
	out := make([]CategoryBar, len(rows))
	for i, row := range rows {
		pct := 0
		if maxRevenue > 0 {
			pct = int(math.Round(row.Revenue / maxRevenue * 100))
		}
		out[i] = CategoryBar{
			Name:     row.Key,
			Sum:      formatMoney(row.Revenue),
			Qty:      fmt.Sprintf("%d шт.", row.ItemCount),
			Orders:   fmt.Sprintf("%d %s", row.OrderCount, pluralRu(row.OrderCount, "заказ", "заказа", "заказов")),
			WidthPct: pct,
		}
	}
	return out
}
