package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/points"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

var pointCols = []string{"id", "name", "address", "is_active", "created_at"}

func newPointsHandlers(t *testing.T) (*handlers, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &handlers{render: newTestRenderer(t), pointsRepo: points.NewPointsRepo(db)}, mock
}

func ownerStaff() *staff.Staff {
	return &staff.Staff{ID: "o", Name: "Айгерим Б.", Role: staff.RoleOwner, IsActive: true}
}

// Editing name/address must keep the point's active flag as it is.
func TestPointsUpdateKeepsActiveFlag(t *testing.T) {
	h, mock := newPointsHandlers(t)
	mock.ExpectQuery(`FROM points_of_sale\s+WHERE id::text = \$1`).WithArgs("p1").
		WillReturnRows(sqlmock.NewRows(pointCols).AddRow("p1", "Старое", "ул. 1", false, time.Now()))
	mock.ExpectQuery(`UPDATE points_of_sale`).WithArgs("p1", "Дордой-Центр", "ул. Дордой, 1", false).
		WillReturnRows(sqlmock.NewRows(pointCols).AddRow("p1", "Дордой-Центр", "ул. Дордой, 1", false, time.Now()))

	r := requestAs(http.MethodPost, "/admin/points/p1", ownerStaff(), url.Values{"name": {" Дордой-Центр "}, "address": {"ул. Дордой, 1"}})
	r.SetPathValue("id", "p1")
	w := httptest.NewRecorder()
	h.pointsUpdate(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want redirect", w.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestPointsUpdateValidationErrorRerenders(t *testing.T) {
	h, mock := newPointsHandlers(t)
	mock.ExpectQuery(`WHERE id::text = \$1`).WillReturnRows(sqlmock.NewRows(pointCols).AddRow("p1", "Старое", "ул. 1", true, time.Now()))
	mock.ExpectQuery(`FROM points_of_sale\s+ORDER BY name`).WillReturnRows(sqlmock.NewRows(pointCols).AddRow("p1", "Старое", "ул. 1", true, time.Now()))

	r := requestAs(http.MethodPost, "/admin/points/p1", ownerStaff(), url.Values{"name": {""}, "address": {"ул. 1"}})
	r.SetPathValue("id", "p1")
	w := httptest.NewRecorder()
	h.pointsUpdate(w, r)

	if !strings.Contains(w.Body.String(), "name обязателен") {
		t.Error("validation error not shown")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestPointsPageHasEditAndDeactivateConfirm(t *testing.T) {
	h, mock := newPointsHandlers(t)
	mock.ExpectQuery(`FROM points_of_sale`).WillReturnRows(sqlmock.NewRows(pointCols).
		AddRow("p1", "Дордой", "ул. 1", true, time.Now()).
		AddRow("p2", "Склад", "ул. 2", false, time.Now()))

	w := httptest.NewRecorder()
	h.pointsPage(w, requestAs(http.MethodGet, "/admin/points", ownerStaff(), nil))
	body := w.Body.String()

	if !strings.Contains(body, `data-id="p1"`) || !strings.Contains(body, "Изменить") {
		t.Error("edit button not rendered")
	}
	// Deactivating an active point asks for confirmation; reactivating doesn't.
	if strings.Count(body, "data-confirm-title=") != 1 {
		t.Errorf("want exactly one confirm (for the active point), got %d", strings.Count(body, "data-confirm-title="))
	}
	if !strings.Contains(body, `role="dialog"`) {
		t.Error("point modal is not a dialog")
	}
}
