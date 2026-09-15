package admin

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/reports"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// fakeReportsBackend is an in-memory reportsBackend for handler tests — no
// database involved, mirrors fakeSalesRepo in
// internal/httpapi/admin_reports_test.go.
type fakeReportsBackend struct {
	orders     []orders.Order
	brandRows  []reports.Row
	loadErr    error
	brandErr   error
	lastFrom   time.Time
	lastTo     time.Time
	brandCalls int
}

func (f *fakeReportsBackend) LoadOrders(_ context.Context, from, to time.Time) ([]orders.Order, error) {
	f.lastFrom, f.lastTo = from, to
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	return f.orders, nil
}

func (f *fakeReportsBackend) BrandSales(_ context.Context, _, _ time.Time) ([]reports.Row, error) {
	f.brandCalls++
	if f.brandErr != nil {
		return nil, f.brandErr
	}
	return f.brandRows, nil
}

// --- reportRange ---

func TestReportRange(t *testing.T) {
	now := time.Date(2026, 9, 15, 14, 30, 0, 0, time.UTC) // a Tuesday mid-September

	cases := []struct {
		period   string
		wantFrom time.Time
		wantTo   time.Time
	}{
		{"week", time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)},
		{"month", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)},
		{"prev_month", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)},
		{"bogus", time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)}, // falls back to "week"
	}
	for _, c := range cases {
		t.Run(c.period, func(t *testing.T) {
			from, to := reportRange(c.period, now)
			if !from.Equal(c.wantFrom) {
				t.Errorf("from = %v, want %v", from, c.wantFrom)
			}
			if !to.Equal(c.wantTo) {
				t.Errorf("to = %v, want %v", to, c.wantTo)
			}
		})
	}
}

func TestIsValidReportPeriod(t *testing.T) {
	if !isValidReportPeriod("week") {
		t.Error("week should be valid")
	}
	if isValidReportPeriod("") {
		t.Error("empty string should be invalid")
	}
	if isValidReportPeriod("year") {
		t.Error("year should be invalid (not one of reportPeriods)")
	}
}

// --- buildStatCards ---

func TestBuildStatCards(t *testing.T) {
	rows := []reports.Row{
		{Key: "2026-09-01", OrderCount: 2, ItemCount: 3, Revenue: 300},
		{Key: "2026-09-02", OrderCount: 1, ItemCount: 1, Revenue: 100},
	}
	stats := buildStatCards(rows)
	if len(stats) != 4 {
		t.Fatalf("expected 4 stat cards, got %d", len(stats))
	}
	if stats[0].Value != "400 сом" {
		t.Errorf("revenue = %q, want %q", stats[0].Value, "400 сом")
	}
	if stats[1].Value != "3" {
		t.Errorf("order count = %q, want %q", stats[1].Value, "3")
	}
	if stats[2].Value != "133 сом" { // 400/3 = 133.33... rounds to 133
		t.Errorf("avg order = %q, want %q", stats[2].Value, "133 сом")
	}
	if stats[3].Value != "4 шт." {
		t.Errorf("item count = %q, want %q", stats[3].Value, "4 шт.")
	}
}

func TestBuildStatCardsEmptyRangeNoDivideByZero(t *testing.T) {
	stats := buildStatCards(nil)
	if stats[2].Value != "0 сом" {
		t.Errorf("avg order on empty range = %q, want %q", stats[2].Value, "0 сом")
	}
}

// --- buildBars ---

func TestBuildBarsFillsEveryDayIncludingZeroSalesDays(t *testing.T) {
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	rows := []reports.Row{
		{Key: "2026-09-01", Revenue: 100},
		{Key: "2026-09-03", Revenue: 200},
	}

	bars := buildBars(rows, from, to)
	if len(bars) != 3 {
		t.Fatalf("expected 3 bars (one per day in range), got %d", len(bars))
	}
	if bars[0].HeightPct != 50 {
		t.Errorf("day 1 height = %d, want 50 (100/200)", bars[0].HeightPct)
	}
	if bars[1].HeightPct != 0 {
		t.Errorf("day 2 (no sales) height = %d, want 0", bars[1].HeightPct)
	}
	if bars[2].HeightPct != 100 {
		t.Errorf("day 3 height = %d, want 100 (peak day)", bars[2].HeightPct)
	}
}

func TestBuildBarsAllZeroRevenueNoDivideByZero(t *testing.T) {
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	bars := buildBars(nil, from, from)
	if len(bars) != 1 || bars[0].HeightPct != 0 {
		t.Fatalf("unexpected bars: %+v", bars)
	}
}

// --- buildTopProducts ---

func TestBuildTopProductsRanksByQuantityDescending(t *testing.T) {
	// AggregateSales returns product rows key-sorted (alphabetical), not
	// ranked — buildTopProducts must re-rank by ItemCount descending.
	rows := []reports.Row{
		{Key: "Ботинки", ItemCount: 5},
		{Key: "Кроссовки", ItemCount: 20},
		{Key: "Сандалии", ItemCount: 10},
	}
	top := buildTopProducts(rows)
	if len(top) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(top))
	}
	if top[0].Name != "Кроссовки" || top[0].Rank != 1 || top[0].Qty != "20 шт." {
		t.Errorf("rank 1 = %+v, want Кроссовки/1/20 шт.", top[0])
	}
	if top[1].Name != "Сандалии" || top[1].Rank != 2 {
		t.Errorf("rank 2 = %+v, want Сандалии/2", top[1])
	}
	if top[2].Name != "Ботинки" || top[2].Rank != 3 {
		t.Errorf("rank 3 = %+v, want Ботинки/3", top[2])
	}
}

func TestBuildTopProductsCapsAtLimit(t *testing.T) {
	var rows []reports.Row
	for i := 0; i < 10; i++ {
		rows = append(rows, reports.Row{Key: string(rune('a' + i)), ItemCount: i})
	}
	top := buildTopProducts(rows)
	if len(top) != reportsTopLimit {
		t.Fatalf("expected %d rows, got %d", reportsTopLimit, len(top))
	}
}

// --- buildTopBrands ---

func TestBuildTopBrandsWidthRelativeToTopBrand(t *testing.T) {
	// BrandSales already returns rows revenue-sorted descending (its SQL
	// query's ORDER BY) — buildTopBrands must not re-sort, just size bars.
	rows := []reports.Row{
		{Key: "Nike", Revenue: 1000},
		{Key: "Adidas", Revenue: 500},
		{Key: "Без бренда", Revenue: 250},
	}
	brands := buildTopBrands(rows)
	if len(brands) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(brands))
	}
	if brands[0].WidthPct != 100 {
		t.Errorf("top brand width = %d, want 100", brands[0].WidthPct)
	}
	if brands[1].WidthPct != 50 {
		t.Errorf("second brand width = %d, want 50", brands[1].WidthPct)
	}
	if brands[2].WidthPct != 25 {
		t.Errorf("third brand width = %d, want 25", brands[2].WidthPct)
	}
}

// --- formatMoney ---

func TestFormatMoney(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0 сом"},
		{200, "200 сом"},
		{7900, "7 900 сом"},
		{1234567, "1 234 567 сом"},
		{133.33, "133 сом"},
	}
	for _, c := range cases {
		if got := formatMoney(c.in); got != c.want {
			t.Errorf("formatMoney(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// --- reportExportURL ---

func TestReportExportURL(t *testing.T) {
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	got := reportExportURL(from, to)
	want := "/admin/api/reports/sales.xlsx?from=2026-09-01&group_by=day&to=2026-09-15"
	if got != want {
		t.Errorf("reportExportURL = %q, want %q", got, want)
	}
}

// --- reportsPage handler (full HTTP round trip against the real templates) ---

func TestReportsPageRendersAndAggregates(t *testing.T) {
	restore := chdir(t, repoRoot(t))
	defer restore()

	renderer, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}

	backend := &fakeReportsBackend{
		orders: []orders.Order{
			{
				ID: "o1", Status: orders.StatusDelivered, TotalAmount: 100,
				CreatedAt: time.Now().UTC(),
				Items:     []orders.OrderItem{{ProductNameSnapshot: "Кроссовки", Quantity: 2, Price: 50}},
			},
		},
		brandRows: []reports.Row{{Key: "Nike", OrderCount: 1, ItemCount: 2, Revenue: 100}},
	}
	st := &staff.Staff{ID: "s1", Name: "Айгерим Б.", Role: staff.RoleOwner, IsActive: true}

	h := &handlers{render: renderer, reports: backend}

	r := httptest.NewRequest("GET", "/admin/reports?period=month", nil)
	r = r.WithContext(staff.NewContextWithStaff(r.Context(), st))
	w := httptest.NewRecorder()

	h.reportsPage(w, r)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if backend.brandCalls != 1 {
		t.Errorf("BrandSales calls = %d, want 1", backend.brandCalls)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Кроссовки") {
		t.Error("expected rendered page to contain the top product name")
	}
	if !strings.Contains(body, "Nike") {
		t.Error("expected rendered page to contain the top brand name")
	}
	if !strings.Contains(body, "/admin/api/reports/sales.xlsx") {
		t.Error("expected rendered page to contain the Excel export link")
	}
}

func TestReportsPageInvalidPeriodFallsBackToDefault(t *testing.T) {
	restore := chdir(t, repoRoot(t))
	defer restore()

	renderer, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}

	backend := &fakeReportsBackend{}
	st := &staff.Staff{ID: "s1", Name: "Данияр К.", Role: staff.RoleManager, IsActive: true}
	h := &handlers{render: renderer, reports: backend}

	r := httptest.NewRequest("GET", "/admin/reports?period=bogus", nil)
	r = r.WithContext(staff.NewContextWithStaff(r.Context(), st))
	w := httptest.NewRecorder()

	h.reportsPage(w, r)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	// "week" is the default period, spanning 7 days ending today.
	wantFrom := time.Now().UTC()
	wantFrom = time.Date(wantFrom.Year(), wantFrom.Month(), wantFrom.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -6)
	if !backend.lastFrom.Equal(wantFrom) {
		t.Errorf("LoadOrders from = %v, want %v (default week period)", backend.lastFrom, wantFrom)
	}
}
