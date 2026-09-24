package audit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/httpmw"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

func actorCtx() context.Context {
	ctx := staff.NewContextWithStaff(context.Background(), &staff.Staff{ID: "11111111-1111-1111-1111-111111111111", Role: staff.RoleOwner})
	return httpmw.WithClientIP(ctx, "203.0.113.7")
}

func TestRecordWritesActorAndIP(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectExec(`INSERT INTO audit_log`).
		WithArgs("11111111-1111-1111-1111-111111111111", ActionPointCreate, EntityPoint, "p1", "Точка «А»",
			`{"name":"А"}`, "203.0.113.7").
		WillReturnResult(sqlmock.NewResult(0, 1))

	New(db).Record(actorCtx(), Entry{
		Action: ActionPointCreate, EntityType: EntityPoint, EntityID: "p1", Summary: "Точка «А»",
		Details: map[string]any{"name": "А"},
	})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestRecordWithoutActorWritesNulls(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectExec(`INSERT INTO audit_log`).
		WithArgs(nil, ActionCategoryDelete, EntityCategory, "c1", "", "{}", nil).
		WillReturnResult(sqlmock.NewResult(0, 1))

	New(db).Record(context.Background(), Entry{Action: ActionCategoryDelete, EntityType: EntityCategory, EntityID: "c1"})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A failing journal write must not surface to the caller.
func TestRecordSwallowsErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectExec(`INSERT INTO audit_log`).WillReturnError(errors.New("boom"))
	New(db).Record(context.Background(), Entry{Action: ActionStaffCreate, EntityType: EntityStaff})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestNilLogIsNoop(t *testing.T) {
	var l *Log
	l.Record(context.Background(), Entry{Action: "x"})
	l.RecordTx(context.Background(), nil, Entry{Action: "x"})
	if l.Enabled() {
		t.Error("nil log reports enabled")
	}
	rows, total, err := l.List(context.Background(), Filter{})
	if err != nil || total != 0 || len(rows) != 0 {
		t.Errorf("List on nil log = %v %d %v", rows, total, err)
	}
}

func TestRecordTxUsesSavepoint(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectExec(`SAVEPOINT audit_log_write`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO audit_log`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO audit_log`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`RELEASE SAVEPOINT audit_log_write`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	New(db).RecordTx(actorCtx(), tx,
		Entry{Action: ActionVariantCreate, EntityType: EntityVariant},
		Entry{Action: ActionStockUpdate, EntityType: EntityStock})
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A failed insert rolls back only to the savepoint: the caller's
// transaction still commits.
func TestRecordTxFailureRollsBackToSavepointOnly(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectExec(`SAVEPOINT audit_log_write`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO audit_log`).WillReturnError(errors.New("disk full"))
	mock.ExpectExec(`ROLLBACK TO SAVEPOINT audit_log_write`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	New(db).RecordTx(context.Background(), tx,
		Entry{Action: ActionVariantCreate, EntityType: EntityVariant},
		Entry{Action: ActionStockUpdate, EntityType: EntityStock})
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestFilterArgs(t *testing.T) {
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	got := filterArgs(Filter{StaffID: "not-a-uuid", EntityType: " product ", From: from, EntityQuery: " ABC "})
	if got[0] != nil {
		t.Errorf("invalid staff id should be ignored, got %v", got[0])
	}
	if got[1] != "product" || got[2] != from || got[3] != nil || got[4] != "ABC" {
		t.Errorf("filterArgs = %#v", got)
	}
	got = filterArgs(Filter{StaffID: "11111111-1111-1111-1111-111111111111"})
	if got[0] != "11111111-1111-1111-1111-111111111111" || got[2] != nil {
		t.Errorf("filterArgs = %#v", got)
	}
}

func TestListMergesOrderHistoryAndPaginates(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	at := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`(?s)UNION ALL.*FROM order_status_history h.*SELECT COUNT\(\*\) FROM entries e`).
		WithArgs(nil, "order", nil, nil, "").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(51))
	sid := "s1"
	mock.ExpectQuery(`(?s)SELECT e.id, e.at.*ORDER BY e.at DESC, e.id DESC\s+LIMIT \$6 OFFSET \$7`).
		WithArgs(nil, "order", nil, nil, "", 50, 50).
		WillReturnRows(sqlmock.NewRows([]string{"id", "at", "staff_id", "name", "action", "entity_type", "entity_id", "summary", "details", "ip"}).
			AddRow("h1", at, &sid, "Айгерим", ActionOrderStatus, EntityOrder, "o1", "Заказ 1001",
				[]byte(`{"order_number":"1001","from":"placed","to":"confirmed"}`), ""))

	rows, total, err := New(db).List(context.Background(), Filter{EntityType: "order", Page: 2})
	if err != nil {
		t.Fatal(err)
	}
	if total != 51 || len(rows) != 1 {
		t.Fatalf("total=%d rows=%d", total, len(rows))
	}
	r := rows[0]
	if r.StaffName != "Айгерим" || r.Details["to"] != "confirmed" || r.Action != ActionOrderStatus {
		t.Errorf("row = %+v", r)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
