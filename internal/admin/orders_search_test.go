package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/points"
	"github.com/Nikemas/cozy_backend/internal/reports"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

func TestResolveOrderFilterInvalidStatusIsDroppedNot500(t *testing.T) {
	p := parseOrdersListParams(url.Values{"status": {"shipped'; --"}})
	f, notes := resolveOrderFilter(&p, time.Now())
	if f.Status != nil {
		t.Fatalf("Status = %v, want nil (never passed to SQL)", *f.Status)
	}
	if len(notes) != 1 || p.Status != "" {
		t.Errorf("notes = %v, params.Status = %q", notes, p.Status)
	}
}

func TestResolveOrderFilterCustomRangeIsBishkekDays(t *testing.T) {
	p := parseOrdersListParams(url.Values{"range": {"custom"}, "from": {"2026-09-01"}, "to": {"2026-09-10"}, "status": {"placed"}})
	f, notes := resolveOrderFilter(&p, time.Now())
	if len(notes) != 0 {
		t.Fatalf("notes = %v", notes)
	}
	if f.Status == nil || *f.Status != orders.StatusPlaced {
		t.Errorf("status = %v", f.Status)
	}
	if want := time.Date(2026, 9, 1, 0, 0, 0, 0, reports.Location); f.From == nil || !f.From.Equal(want) {
		t.Errorf("From = %v, want %v", f.From, want)
	}
	if want := time.Date(2026, 9, 11, 0, 0, 0, 0, reports.Location); f.To == nil || !f.To.Equal(want) {
		t.Errorf("To = %v, want %v (exclusive, day after)", f.To, want)
	}
}

func TestResolveOrderFilterBadDateAndPresetRange(t *testing.T) {
	p := parseOrdersListParams(url.Values{"range": {"custom"}, "from": {"01.09.2026"}})
	f, notes := resolveOrderFilter(&p, time.Now())
	if f.From != nil || len(notes) != 1 || p.From != "" {
		t.Errorf("bad date: From = %v, notes = %v", f.From, notes)
	}

	now := time.Date(2026, 9, 15, 20, 0, 0, 0, time.UTC) // Sep 16, 02:00 in Bishkek
	p = parseOrdersListParams(url.Values{"range": {"7"}})
	f, _ = resolveOrderFilter(&p, now)
	if want := time.Date(2026, 9, 10, 0, 0, 0, 0, reports.Location); f.From == nil || !f.From.Equal(want) {
		t.Errorf("7 days From = %v, want %v", f.From, want)
	}

	p = parseOrdersListParams(url.Values{"range": {"999"}})
	f, _ = resolveOrderFilter(&p, now)
	if f.From != nil || p.Range != "all" {
		t.Errorf("unknown range: From = %v, Range = %q", f.From, p.Range)
	}
}

func TestOrdersListParamsURLPreservesFilters(t *testing.T) {
	p := ordersListParams{Status: "placed", Range: "custom", From: "2026-09-01", To: "2026-09-02", Point: "pA", Q: "0555", Page: 2}
	want := "/admin/orders?from=2026-09-01&page=2&point=pA&q=0555&range=custom&status=placed&to=2026-09-02"
	if got := p.URL(); got != want {
		t.Errorf("URL = %q, want %q", got, want)
	}
	// from/to only matter for the custom range
	if got := (ordersListParams{Range: "7", From: "x"}).URL(); got != "/admin/orders?range=7" {
		t.Errorf("URL = %q", got)
	}
}

func TestPhoneDigits(t *testing.T) {
	cases := map[string]string{
		"+996 (555) 12-34": "9965551234",
		"0555":             "0555",
		"55":               "",
		"COZY-20260915":    "",
		"12a":              "",
	}
	for in, want := range cases {
		if got := phoneDigits(in); got != want {
			t.Errorf("phoneDigits(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOrderSearchSQLByPhoneAndNumber(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	point := "pA"
	status := orders.StatusPlaced
	f := orderSearchFilter{Status: &status, PointID: &point, Query: "+996 555", Page: 1}

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM orders o JOIN customers c ON c.id = o.customer_id WHERE TRUE AND o.status = \$1 AND o.point_id = \$2 AND \(o.order_number ILIKE \$3 OR regexp_replace\(c.phone, '\[\^0-9\]', '', 'g'\) LIKE \$4\)`).
		WithArgs("placed", "pA", "%+996 555%", "%996555%").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	now := time.Now()
	mock.ExpectQuery(`ORDER BY o.created_at DESC\s+LIMIT \$5 OFFSET \$6`).
		WithArgs("placed", "pA", "%+996 555%", "%996555%", orders.AdminPageSize, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "order_number", "customer_id", "address_id", "point_id", "status", "payment_method", "payment_status", "total_amount", "comment", "created_at", "updated_at"}).
			AddRow("o1", "COZY-1", "c1", nil, "pA", "placed", "cash_on_delivery", nil, 100.0, nil, now, now))

	list, total, err := newOrderListMetaRepo(db).Search(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(list) != 1 || list[0].OrderNumber != "COZY-1" {
		t.Errorf("list = %+v, total = %d", list, total)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func newOwnerOrdersHandlers(t *testing.T, meta *fakeOrderListMeta) *handlers {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(`FROM points_of_sale`).WillReturnRows(
		sqlmock.NewRows([]string{"id", "name", "address", "is_active", "created_at"}).
			AddRow("pA", "Главный склад", "ул. 1", true, time.Now()))
	return &handlers{render: newTestRenderer(t), orderMeta: meta, pointsRepo: points.NewPointsRepo(db)}
}

func TestOrdersListOwnerFiltersPassThrough(t *testing.T) {
	meta := &fakeOrderListMeta{}
	h := newOwnerOrdersHandlers(t, meta)
	owner := &staff.Staff{ID: "o", Name: "Айгерим Б.", Role: staff.RoleOwner, IsActive: true}

	w := httptest.NewRecorder()
	h.ordersListPage(w, requestAs(http.MethodGet, "/admin/orders?q=COZY-2026&point=pA&status=bogus", owner, nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (an invalid status used to 500)", w.Code)
	}
	f := meta.searchFilter
	if f == nil || f.Query != "COZY-2026" || f.PointID == nil || *f.PointID != "pA" || f.Status != nil {
		t.Fatalf("filter = %+v", f)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Неизвестный статус") || !strings.Contains(body, `id="orders-point"`) {
		t.Error("expected the status note and the point selector")
	}
}

func TestOrdersListOwnerUnknownPointIgnored(t *testing.T) {
	meta := &fakeOrderListMeta{}
	h := newOwnerOrdersHandlers(t, meta)
	owner := &staff.Staff{ID: "o", Name: "Айгерим Б.", Role: staff.RoleOwner, IsActive: true}

	w := httptest.NewRecorder()
	h.ordersListPage(w, requestAs(http.MethodGet, "/admin/orders?point=not-a-uuid", owner, nil))

	if meta.searchFilter == nil || meta.searchFilter.PointID != nil {
		t.Fatalf("filter = %+v, want no point filter for an unknown point id", meta.searchFilter)
	}
}
