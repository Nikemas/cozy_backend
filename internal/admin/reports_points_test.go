package admin

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/reports"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

func TestBuildPointBarsSortsByRevenueAndLabelsUnknown(t *testing.T) {
	// Arrange: AggregateSales returns rows key-sorted, not revenue-sorted.
	rows := []reports.Row{
		{Key: "Дордой", OrderCount: 1, ItemCount: 2, Revenue: 3000},
		{Key: reports.UnknownPointKey, OrderCount: 2, ItemCount: 2, Revenue: 1500},
		{Key: "ЦУМ", OrderCount: 3, ItemCount: 5, Revenue: 6000},
	}

	// Act
	got := buildPointBars(ruTr, rows)

	// Assert
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].Name != "ЦУМ" || got[0].WidthPct != 100 || got[0].Sum != "6 000 сом" {
		t.Errorf("row 0 = %+v, want ЦУМ / 100%% / 6 000 сом", got[0])
	}
	if got[1].Name != "Дордой" || got[1].WidthPct != 50 {
		t.Errorf("row 1 = %+v, want Дордой / 50%%", got[1])
	}
	if got[2].Name != ruTr.T("admin.reports.no_point") {
		t.Errorf("unknown point label = %q, want localized no_point", got[2].Name)
	}
	if got[0].Qty != "5 шт." {
		t.Errorf("qty = %q, want 5 шт.", got[0].Qty)
	}
	if got[0].Orders == "" {
		t.Error("orders label is empty")
	}
}

func TestBuildPointBarsEmpty(t *testing.T) {
	if got := buildPointBars(ruTr, nil); len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestReportsPageRendersPointsSection(t *testing.T) {
	pointA, pointB := "p-a", "p-b"
	backend := &fakeReportsBackend{
		orders: []orders.Order{
			{ID: "o1", Status: orders.StatusDelivered, TotalAmount: 4200, PointID: &pointA, CreatedAt: time.Now().UTC(),
				Items: []orders.OrderItem{{ProductNameSnapshot: "Кеды", Quantity: 3, Price: 1400}}},
			{ID: "o2", Status: orders.StatusDelivered, TotalAmount: 999, PointID: &pointB, CreatedAt: time.Now().UTC(),
				Items: []orders.OrderItem{{ProductNameSnapshot: "Тапки", Quantity: 1, Price: 999}}},
			{ID: "o3", Status: orders.StatusCancelled, TotalAmount: 50000, PointID: &pointB, CreatedAt: time.Now().UTC(),
				Items: []orders.OrderItem{{ProductNameSnapshot: "Тапки", Quantity: 9, Price: 999}}},
		},
		pointNames: map[string]string{pointA: "Точка Дордой", pointB: "Точка ЦУМ"},
	}
	st := &staff.Staff{ID: "s1", Name: "Айгерим Б.", Role: staff.RoleOwner, IsActive: true}
	h := &handlers{render: newTestRenderer(t), reports: backend}

	r := httptest.NewRequest("GET", "/admin/reports?period=week", nil)
	r = r.WithContext(staff.NewContextWithStaff(r.Context(), st))
	w := httptest.NewRecorder()
	h.reportsPage(w, r)

	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"По точкам продаж", "Точка Дордой", "Точка ЦУМ", "4 200 сом", "group_by=point"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(body, "50 000 сом") {
		t.Error("cancelled order counted in the points report")
	}
	if i, j := strings.Index(body, "Точка Дордой"), strings.Index(body, "Точка ЦУМ"); i < 0 || j < 0 || i > j {
		t.Error("points not ordered by revenue (Дордой 4 200 should precede ЦУМ 999)")
	}
}

func TestReportsPageKyrgyzPointsLabels(t *testing.T) {
	backend := &fakeReportsBackend{}
	st := &staff.Staff{ID: "s1", Name: "A", Role: staff.RoleOwner, IsActive: true}
	h := &handlers{render: newTestRenderer(t), reports: backend}

	r := httptest.NewRequest("GET", "/admin/reports", nil)
	ctx := staff.NewContextWithStaff(r.Context(), st)
	r = r.WithContext(contextWithLang(ctx, i18n.LangKY))
	rec := httptest.NewRecorder()
	h.reportsPage(withLang(rec, r), r)

	body := rec.Body.String()
	if !strings.Contains(body, kyTr.T("admin.reports.by_point")) {
		t.Error("ky points heading missing")
	}
	if kyTr.T("admin.reports.by_point") == ruTr.T("admin.reports.by_point") {
		t.Error("ky by_point is not translated")
	}
}

func TestReportsPagePointNamesErrorIs500(t *testing.T) {
	backend := &fakeReportsBackend{pointErr: errors.New("db down")}
	st := &staff.Staff{ID: "s1", Name: "A", Role: staff.RoleOwner, IsActive: true}
	h := &handlers{render: newTestRenderer(t), reports: backend}

	r := httptest.NewRequest("GET", "/admin/reports", nil)
	r = r.WithContext(staff.NewContextWithStaff(r.Context(), st))
	w := httptest.NewRecorder()
	h.reportsPage(w, r)

	if w.Code != 500 {
		t.Errorf("status = %d, want 500", w.Code)
	}
}
