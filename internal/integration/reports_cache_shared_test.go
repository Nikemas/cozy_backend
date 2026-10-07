//go:build integration

package integration

import (
	"testing"

	"github.com/Nikemas/cozy_backend/internal/staff"
)

// TestReportsPageAndExportShareOneCache: the admin HTML reports page and
// the /admin/api/reports export run over the single reports.CachedRepo
// cmd/server builds — the page warming a range makes the export of the
// same range a cache hit instead of a second aggregate query.
func TestReportsPageAndExportShareOneCache(t *testing.T) {
	a := app(t)
	owner, _ := a.newStaffSession(t, staff.RoleOwner, nil)

	// A range no other test reports on, so its cache entries are fresh.
	const from, to = "2019-03-01", "2019-03-10"

	before := a.reports.Len()
	wantStatus(t, a.get(t, "/admin/reports?period=custom&from="+from+"&to="+to, owner), 200)
	afterPage := a.reports.Len()
	if afterPage <= before {
		t.Fatalf("reports page did not populate the shared cache: len %d -> %d", before, afterPage)
	}

	for _, groupBy := range []string{"product", "day", "point", "category"} {
		wantStatus(t, a.get(t, "/admin/api/reports/sales?from="+from+"&to="+to+"&group_by="+groupBy, owner), 200)
	}
	if got := a.reports.Len(); got != afterPage {
		t.Fatalf("export missed the cache the page warmed: len %d -> %d", afterPage, got)
	}

	// And the other way round: an export of a fresh range lands in the
	// same shared cache.
	wantStatus(t, a.get(t, "/admin/api/reports/sales?from=2019-04-01&to=2019-04-10&group_by=product", owner), 200)
	if got := a.reports.Len(); got != afterPage+1 {
		t.Fatalf("export of a fresh range did not populate the shared cache: len %d -> %d", afterPage, got)
	}
}
