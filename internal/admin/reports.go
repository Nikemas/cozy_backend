// reports.go implements Task 4 (Отчёты, Wave 4) — Cozy Admin.dc.html lines
// 558-618: a period selector + "Экспорт в Excel" link, 4 stat cards, a
// pure-CSS day-by-day bar chart, a "Топ товаров" ranked list and a "По
// брендам" list with mini progress bars. All of it is a presentation over
// internal/reports.AggregateSales' output (Wave 3, Task O) grouped
// different ways, plus reports.Repo.BrandSales for the one dimension
// AggregateSales doesn't group by.
package admin

import (
	"context"
	"database/sql"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/reports"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// reportsBackend is the subset of behavior reportsPage depends on — an
// interface (mirroring salesRepo in internal/httpapi/admin_reports.go) so
// the handler can be tested with a fake instead of a live database.
// *reports.CachedRepo satisfies it in production (see newReportsBackend).
type reportsBackend interface {
	Sales(ctx context.Context, from, to time.Time, groupBy reports.GroupBy, pointNames map[string]string) ([]reports.Row, error)
	BrandSales(ctx context.Context, from, to time.Time) ([]reports.Row, error)
	CategorySales(ctx context.Context, from, to time.Time) ([]reports.Row, error)
	PointNames(ctx context.Context) (map[string]string, error)
}

// newReportsBackend is the reports screen's query layer: reports.Repo's
// aggregates behind an in-process TTL cache (reports.CachedRepo) with
// concurrent identical requests sharing one query — a year-long report
// aggregates every order row of the year.
func newReportsBackend(db *sql.DB) reportsBackend {
	return reports.NewCachedRepo(reports.NewRepo(db), reports.CacheConfig{})
}

// --- Period selection ---

// reportPeriod is one option in the period <select> (design canvas's
// `period`/`onPeriod` binding). The canvas mocks specific calendar months
// ("01.09.2026 — 30.09.2026"); Task 4's acceptance criteria explicitly
// leave the exact set to this implementation, so relative, always-valid
// ranges are used instead of hardcoded months.
type reportPeriod struct {
	key   string
	label string // locale key
}

var reportPeriods = []reportPeriod{
	{"week", "admin.range.last7"},
	{"month", "admin.range.this_month"},
	{"prev_month", "admin.range.prev_month"},
	{"custom", "admin.range.custom"},
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
			rangeErr = appErrMessage(h.tr(r), err)
			period = defaultReportPeriod()
		}
	}
	if period != "custom" {
		from, to = reportRange(period, time.Now())
	}

	reportData, err := h.buildReportsData(r.Context(), period, from, to)
	if err != nil {
		http.Error(w, h.tr(r).T("admin.reports.build_failed"), http.StatusInternalServerError)
		return
	}
	reportData.Custom = period == "custom"
	reportData.From = from.Format("2006-01-02")
	reportData.To = to.Format("2006-01-02")
	reportData.Err = rangeErr

	data := h.shellPageData("reports", "admin.nav.reports", st)
	data.Data = reportData
	if err := h.render.Render(w, "reports", data); err != nil {
		http.Error(w, h.tr(r).T("admin.err.render"), http.StatusInternalServerError)
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

// buildReportsData aggregates sales for [from, to] (both inclusive from
// the caller's point of view) and shapes them into everything the template
// needs. Every grouping is aggregated in Postgres (reports.Repo.Sales,
// BrandSales, CategorySales) — loading a year of orders with their items
// into memory took ~1 s per page view on the perf dataset
// (docs/performance.md).
func (h *handlers) buildReportsData(ctx context.Context, period string, from, to time.Time) (ReportsData, error) {
	loadTo := to.AddDate(0, 0, 1) // Sales' range is [from, to) — see reports.Repo.LoadOrders

	dayRows, err := h.reports.Sales(ctx, from, loadTo, reports.GroupByDay, nil)
	if err != nil {
		return ReportsData{}, err
	}
	productRows, err := h.reports.Sales(ctx, from, loadTo, reports.GroupByProduct, nil)
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
	pointNames, err := h.reports.PointNames(ctx)
	if err != nil {
		return ReportsData{}, err
	}
	pointRows, err := h.reports.Sales(ctx, from, loadTo, reports.GroupByPoint, pointNames)
	if err != nil {
		return ReportsData{}, err
	}

	t := trFromContext(ctx)
	return ReportsData{
		Periods:   reportPeriodOptions(t, period),
		ExportURL: reportExportURL(from, to),
		// fix/admin-ops: the category report as xlsx (same numbers).
		CategoryExportURL: strings.Replace(reportExportURL(from, to), "group_by=day", "group_by=category", 1),
		PointExportURL:    strings.Replace(reportExportURL(from, to), "group_by=day", "group_by=point", 1),
		Stats:             buildStatCards(t, dayRows),
		ChartNote:         from.Format("02.01.2006") + " — " + to.Format("02.01.2006"),
		Bars:              buildBars(dayRows, from, to),
		TopProducts:       buildTopProducts(t, productRows),
		TopBrands:         buildTopBrands(t, brandRows),
		Categories:        buildCategoryBars(t, categoryRows),
		Points:            buildPointBars(t, pointRows),
	}, nil
}

// --- View types (ReportsData is the "reports" screen's PageData.Data) ---

// ReportsData is the reports screen's Data payload. Section by section it
// mirrors Cozy Admin.dc.html lines 558-618: Periods+ExportURL is the
// toolbar, Stats the 4-card grid, ChartNote+Bars the day-by-day bar chart,
// TopProducts/TopBrands the two ranked lists.
type ReportsData struct {
	Periods           []PeriodOption
	ExportURL         string
	CategoryExportURL string
	PointExportURL    string

	Stats []StatCard

	ChartNote string
	Bars      []ReportBar

	TopProducts []RankedRow
	TopBrands   []BrandBar
	Categories  []CategoryBar
	Points      []CategoryBar // "По точкам продаж": same columns as categories

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

func reportPeriodOptions(t tr, selected string) []PeriodOption {
	out := make([]PeriodOption, len(reportPeriods))
	for i, p := range reportPeriods {
		out[i] = PeriodOption{Value: p.key, Label: t.T(p.label), Selected: p.key == selected}
	}
	return out
}

// reportExportURL builds the "Экспорт в Excel" link's target — the
// already-working /admin/api/reports/sales.xlsx endpoint (Wave 3), with
// the currently selected period's date range. group_by=day matches the
// "Продажи по дням" section this page leads with; the category and point
// sections link to the same endpoint with group_by=category / point.
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
func buildStatCards(t tr, dayRows []reports.Row) []StatCard {
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
		{Label: t.T("admin.reports.revenue"), Value: formatMoney(totalRevenue), Note: t.T("admin.reports.for_period")},
		{Label: t.T("admin.reports.orders"), Value: strconv.Itoa(orderCount), Note: t.T("admin.reports.for_period")},
		{Label: t.T("admin.reports.avg_check"), Value: formatMoney(avgOrder), Note: t.T("admin.reports.per_order")},
		{Label: t.T("admin.reports.items_sold"), Value: t.F("admin.reports.pcs", itemCount), Note: t.T("admin.reports.for_period")},
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
func buildTopProducts(t tr, productRows []reports.Row) []RankedRow {
	rows := append([]reports.Row(nil), productRows...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ItemCount > rows[j].ItemCount })
	if len(rows) > reportsTopLimit {
		rows = rows[:reportsTopLimit]
	}

	out := make([]RankedRow, len(rows))
	for i, row := range rows {
		out[i] = RankedRow{Rank: i + 1, Name: row.Key, Qty: t.F("admin.reports.pcs", row.ItemCount)}
	}
	return out
}

// buildTopBrands takes BrandSales' rows (already revenue-sorted by the
// SQL query's ORDER BY) and derives each mini progress bar's width
// relative to the top brand's revenue.
func buildTopBrands(t tr, brandRows []reports.Row) []BrandBar {
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
		name := row.Key
		if name == "" {
			name = t.T("admin.reports.no_brand")
		}
		out[i] = BrandBar{Name: name, Sum: formatMoney(row.Revenue), WidthPct: pct}
	}
	return out
}

// formatMoney is defined once for the package in products_view.go (the
// same KGS thousands-grouped, no-decimals formatting internal/web's own
// formatMoney uses) — reused here rather than redefined.

// buildCategoryBars shapes reports.Repo.CategorySales' rows (already
// revenue-sorted) — every category, not capped like the top-5 lists, since
// a shoe shop has only a handful of top-level categories.
func buildCategoryBars(t tr, rows []reports.Row) []CategoryBar {
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
			Qty:      t.F("admin.reports.pcs", row.ItemCount),
			Orders:   t.N(row.OrderCount, "admin.plural.order"),
			WidthPct: pct,
		}
	}
	return out
}

// buildPointBars shapes AggregateSales' GroupByPoint rows (key-sorted) into
// the "По точкам продаж" table: re-ranked by revenue, every point listed
// (a shop has a handful), orders with no point labelled in the page
// language. Revenue here is the orders' total_amount — the same figure the
// group_by=point JSON/xlsx export reports.
func buildPointBars(t tr, pointRows []reports.Row) []CategoryBar {
	rows := append([]reports.Row(nil), pointRows...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Revenue > rows[j].Revenue })
	out := buildCategoryBars(t, rows)
	for i, row := range rows {
		if row.Key == reports.UnknownPointKey {
			out[i].Name = t.T("admin.reports.no_point")
		}
	}
	return out
}
