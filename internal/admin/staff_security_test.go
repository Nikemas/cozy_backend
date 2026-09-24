package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/staff"
)

// TestLoginSubmitHidesInternalErrors: a database failure during login
// must not print the driver error on the (unauthenticated) login page.
func TestLoginSubmitHidesInternalErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectQuery(`FROM staff`).WillReturnError(errors.New(`pq: relation "staff" does not exist SECRET-DETAIL`))

	h := &handlers{staffSvc: staff.NewService(db), render: newTestRenderer(t)}
	form := url.Values{"phone": {"+996700000001"}, "password": {"whatever-pass"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.loginSubmit(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "SECRET-DETAIL") || strings.Contains(body, "relation") {
		t.Fatalf("login page leaks the internal error: %s", body)
	}
	if !strings.Contains(body, "произошла ошибка") {
		t.Errorf("expected the generic error message on the page")
	}
}

func TestRenderStaffHasPasswordResetAndDeactivateConfirm(t *testing.T) {
	rr := newTestRenderer(t)
	owner := &staff.Staff{ID: "s1", Name: "Айгерим Б.", Role: staff.RoleOwner, IsActive: true}
	data := PageData{
		Screen: "staff", PageTitle: "Сотрудники", ShowSidebar: true, Staff: owner,
		Initials: initialsFor(owner.Name), RoleLabel: roleLabel(owner.Role),
		NavItems: navItemsForRole(owner.Role, "staff"),
		Data: staffPageData{
			Notice:            staffNotices["password"],
			MinPasswordLength: staff.MinPasswordLength,
			Rows: []staffRow{
				newStaffRow(staff.Staff{ID: "s2", Name: "Данияр К.", Role: staff.RoleManager, IsActive: true}, nil),
				newStaffRow(staff.Staff{ID: "s3", Name: "Нурлан Т.", Role: staff.RoleManager, IsActive: false}, nil),
			},
		},
	}
	w := httptest.NewRecorder()
	if err := rr.Render(w, "staff", data); err != nil {
		t.Fatal(err)
	}
	body := w.Body.String()

	for _, want := range []string{
		`id="staff-password-form"`,
		`name="password_confirm"`,
		`data-staff-id="s2"`,
		`minlength="8"`,
		staffNotices["password"],
	} {
		if !strings.Contains(body, want) {
			t.Errorf("staff page missing %q", want)
		}
	}
	// Only the active row's toggle (deactivate) asks for confirmation.
	if n := strings.Count(body, "data-confirm="); n != 1 {
		t.Errorf("data-confirm count = %d, want 1 (deactivate only)", n)
	}
	if strings.Contains(body, "Math.random") {
		t.Error("password generator must use crypto.getRandomValues")
	}
}
