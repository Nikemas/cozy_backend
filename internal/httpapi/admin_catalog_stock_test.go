package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/audit"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

var ownerStaff = &staff.Staff{ID: "s1", Role: staff.RoleOwner}

// expected_quantity is passed through; a mismatch is a 409 with the
// current quantity in the body.
func TestUpdateStockHandlerExpectedQuantityConflict(t *testing.T) {
	fake := &fakeStockUpserter{err: &stockConflictError{Current: 1}}
	rec := httptest.NewRecorder()
	apperr.Wrap(updateStockHandler(fake)).ServeHTTP(rec,
		newStockRequest(t, ownerStaff, "variant-1", "point-1", `{"quantity": 5, "expected_quantity": 3}`))

	if fake.lastExpected == nil || *fake.lastExpected != 3 {
		t.Fatalf("expected_quantity not passed: %+v", fake.lastExpected)
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	var body stockConflictResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "stock_conflict" || body.CurrentQuantity != 1 {
		t.Errorf("body = %+v", body)
	}
}

func TestUpdateStockHandlerWithoutExpectedStaysAbsolute(t *testing.T) {
	fake := &fakeStockUpserter{}
	rec := httptest.NewRecorder()
	apperr.Wrap(updateStockHandler(fake)).ServeHTTP(rec, newStockRequest(t, ownerStaff, "v", "p", `{"quantity": 2}`))
	if rec.Code != http.StatusOK || fake.lastExpected != nil {
		t.Fatalf("code=%d expected=%v", rec.Code, fake.lastExpected)
	}
}

var stockCols = []string{"variant_id", "point_id", "quantity", "updated_at"}

func TestAdminStockStoreConflictOnMismatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT quantity FROM stock WHERE variant_id = \$1 AND point_id = \$2 FOR UPDATE`).
		WithArgs("v1", "p1").WillReturnRows(sqlmock.NewRows([]string{"quantity"}).AddRow(1))
	mock.ExpectRollback()

	exp := 3
	_, err = (&adminStockStore{db: db}).Set(context.Background(), "v1", "p1", 5, &exp)
	var conflict *stockConflictError
	if !errors.As(err, &conflict) || conflict.Current != 1 {
		t.Fatalf("err = %v, want conflict with current 1", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Matching expectation: UPDATE + journal line old → new in the same tx.
func TestAdminStockStoreUpdateJournals(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE`).WithArgs("v1", "p1").WillReturnRows(sqlmock.NewRows([]string{"quantity"}).AddRow(3))
	mock.ExpectQuery(`UPDATE stock SET quantity = \$3`).WithArgs("v1", "p1", 5).
		WillReturnRows(sqlmock.NewRows(stockCols).AddRow("v1", "p1", 5, now))
	mock.ExpectExec(`SAVEPOINT audit_log_write`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`JOIN points_of_sale pos ON pos.id = \$2`).WithArgs("v1", "p1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "size", "color", "point"}).AddRow("pr1", "Кеды", "42", "Белый", "Дордой"))
	mock.ExpectExec(`INSERT INTO audit_log`).
		WithArgs("s1", audit.ActionStockUpdate, audit.EntityStock, "v1", "Остаток «Кеды» 42 / Белый, Дордой: 3 → 5 (API)",
			sqlmock.AnyArg(), nil).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`RELEASE SAVEPOINT audit_log_write`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	exp := 3
	ctx := staff.NewContextWithStaff(context.Background(), ownerStaff)
	entry, err := (&adminStockStore{db: db, audit: audit.New(db)}).Set(ctx, "v1", "p1", 5, &exp)
	if err != nil || entry.Quantity != 5 {
		t.Fatalf("Set = %+v, %v", entry, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// No row yet + expected 0: insert-if-absent; someone else inserting first
// is a conflict, not an overwrite.
func TestAdminStockStoreInsertRaceIsConflict(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE`).WillReturnRows(sqlmock.NewRows([]string{"quantity"}))
	mock.ExpectQuery(`ON CONFLICT \(variant_id, point_id\) DO NOTHING`).WithArgs("v1", "p1", 4).
		WillReturnRows(sqlmock.NewRows(stockCols))
	mock.ExpectQuery(`SELECT quantity FROM stock WHERE variant_id = \$1 AND point_id = \$2$`).
		WillReturnRows(sqlmock.NewRows([]string{"quantity"}).AddRow(7))
	mock.ExpectRollback()

	exp := 0
	_, err = (&adminStockStore{db: db}).Set(context.Background(), "v1", "p1", 4, &exp)
	var conflict *stockConflictError
	if !errors.As(err, &conflict) || conflict.Current != 7 {
		t.Fatalf("err = %v, want conflict with current 7", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestAdminStockStoreUnconditionalUpsertAndBadIDs(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE`).WillReturnRows(sqlmock.NewRows([]string{"quantity"}))
	mock.ExpectQuery(`ON CONFLICT \(variant_id, point_id\) DO UPDATE`).WithArgs("v1", "p1", 4).
		WillReturnError(&pgconn.PgError{Code: "23503"})
	mock.ExpectRollback()

	_, err = (&adminStockStore{db: db}).Set(context.Background(), "v1", "p1", 4, nil)
	var ae *apperr.AppError
	if !errors.As(err, &ae) || ae.Status != http.StatusBadRequest {
		t.Fatalf("err = %v, want 400", err)
	}
	if _, err := (&adminStockStore{db: db}).Set(context.Background(), "v1", "p1", -1, nil); err == nil {
		t.Error("negative quantity accepted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
