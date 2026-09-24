package admin

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/audit"
	"github.com/Nikemas/cozy_backend/internal/points"
)

// Editing a point journals only the fields that changed; the staff
// password reset never carries the password.
func TestAuditPointUpdateAndStaffPassword(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	h := &handlers{audit: audit.New(db)}

	mock.ExpectExec(`INSERT INTO audit_log`).
		WithArgs("s1", audit.ActionPointUpdate, audit.EntityPoint, "p1", "Изменена точка «Дордой»",
			`{"name":{"from":"Старое","to":"Дордой"}}`, nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
	h.auditPoint(ownerCtx(), audit.ActionPointUpdate, "p1",
		&points.Point{ID: "p1", Name: "Старое", Address: "ул. 1"}, points.PointInput{Name: "Дордой", Address: "ул. 1", IsActive: true})

	mock.ExpectExec(`INSERT INTO audit_log`).
		WithArgs("s1", audit.ActionStaffPassword, audit.EntityStaff, "st2", "Сброшен пароль сотрудника «Нурлан»", "{}", nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
	h.auditStaff(ownerCtx(), audit.ActionStaffPassword, "st2", "Нурлан", nil)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
