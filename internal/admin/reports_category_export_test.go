package admin

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/reports"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// The "По категориям" block links to the same numbers as xlsx.
func TestReportsPageCategoryExportLink(t *testing.T) {
	backend := &fakeReportsBackend{categoryRows: []reports.Row{{Key: "Кеды", OrderCount: 1, ItemCount: 1, Revenue: 100}}}
	h := &handlers{render: newTestRenderer(t), reports: backend}
	r := httptest.NewRequest("GET", "/admin/reports?period=custom&from=2026-09-01&to=2026-09-10", nil)
	r = r.WithContext(staff.NewContextWithStaff(r.Context(), &staff.Staff{ID: "s1", Name: "А", Role: staff.RoleOwner}))
	w := httptest.NewRecorder()
	h.reportsPage(w, r)
	want := `href="/admin/api/reports/sales.xlsx?from=2026-09-01&amp;group_by=category&amp;to=2026-09-10"`
	if !strings.Contains(w.Body.String(), want) {
		t.Errorf("category xlsx link missing (%s)", want)
	}
}
