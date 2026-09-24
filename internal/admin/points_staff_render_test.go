package admin

import (
	"net/http/httptest"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/points"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// TestRenderPointsExecutesWithRows exercises points.gohtml with a
// populated pointsPageData (both an active and an inactive row, plus an
// error banner) — TestRenderShellScreensExecute (render_exec_test.go)
// only covers the nil-.Data case, which isn't representative of what
// pointsPage/pointsCreate/pointsToggle actually render in production.
func TestRenderPointsExecutesWithRows(t *testing.T) {
	rr := newTestRenderer(t)
	owner := &staff.Staff{ID: "s1", Name: "Айгерим Б.", Role: staff.RoleOwner, IsActive: true}

	data := PageData{
		Screen:      "points",
		PageTitle:   "Склад и точки",
		ShowSidebar: true,
		Staff:       owner,
		Initials:    initialsFor(owner.Name),
		RoleLabel:   roleLabel(owner.Role),
		NavItems:    navItemsForRole(owner.Role, "points"),
		Data: pointsPageData{
			Error: "название обязательно",
			Rows: []pointRow{
				newPointRow(ruTr, &points.Point{ID: "p1", Name: "Дордой-Центр", Address: "ул. Дордой, 1", IsActive: true}),
				newPointRow(ruTr, &points.Point{ID: "p2", Name: "Склад", Address: "ул. Ленина, 5", IsActive: false}),
			},
		},
	}

	w := httptest.NewRecorder()
	if err := rr.Render(w, "points", data); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

// TestRenderStaffExecutesWithRows is TestRenderPointsExecutesWithRows'
// counterpart for staff.gohtml — covers all three roles' chip/scope
// rendering plus the modal's point select populated from Points.
func TestRenderStaffExecutesWithRows(t *testing.T) {
	rr := newTestRenderer(t)
	owner := &staff.Staff{ID: "s1", Name: "Айгерим Б.", Role: staff.RoleOwner, IsActive: true}

	pointID := "p1"
	pts := []*points.Point{{ID: "p1", Name: "Дордой-Центр", Address: "ул. Дордой, 1", IsActive: true}}
	pointNames := map[string]string{"p1": "Дордой-Центр"}

	data := PageData{
		Screen:      "staff",
		PageTitle:   "Сотрудники",
		ShowSidebar: true,
		Staff:       owner,
		Initials:    initialsFor(owner.Name),
		RoleLabel:   roleLabel(owner.Role),
		NavItems:    navItemsForRole(owner.Role, "staff"),
		Data: staffPageData{
			Error:  "нельзя понизить или деактивировать последнего владельца",
			Points: pts,
			Rows: []staffRow{
				newStaffRow(ruTr, staff.Staff{ID: "s1", Name: "Айгерим Б.", Phone: "+996555000001", Role: staff.RoleOwner, IsActive: true}, pointNames),
				newStaffRow(ruTr, staff.Staff{ID: "s2", Name: "Данияр К.", Phone: "+996555000002", Role: staff.RoleManager, IsActive: true}, pointNames),
				newStaffRow(ruTr, staff.Staff{ID: "s3", Name: "Нурлан Т.", Phone: "+996555000003", Role: staff.RolePointStaff, PointID: &pointID, IsActive: false}, pointNames),
			},
		},
	}

	w := httptest.NewRecorder()
	if err := rr.Render(w, "staff", data); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}
