package orders

import (
	"context"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// There's no live Postgres available to test against in this environment
// (see the task report), so these tests drive CartRepo's real SQL bodies
// against github.com/DATA-DOG/go-sqlmock — a fake driver that lets us
// assert the exact statements/args CartRepo issues and script back rows/
// results, without weakening CartRepo's frozen *sql.DB-based signatures.

func newMockCartRepo(t *testing.T) (*CartRepo, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewCartRepo(db), mock
}

func TestCartRepoListReturnsRows(t *testing.T) {
	repo, mock := newMockCartRepo(t)
	now := time.Now()

	rows := sqlmock.NewRows([]string{"customer_id", "variant_id", "qty", "created_at"}).
		AddRow("cust-1", "var-1", 2, now).
		AddRow("cust-1", "var-2", 1, now)

	mock.ExpectQuery(regexp.QuoteMeta("FROM cart_items")).
		WithArgs("cust-1").
		WillReturnRows(rows)

	items, err := repo.List(context.Background(), "cust-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].VariantID != "var-1" || items[0].Qty != 2 {
		t.Errorf("items[0] = %+v, want variant var-1 qty 2", items[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestCartRepoListEmptyReturnsEmptySliceNotNil(t *testing.T) {
	repo, mock := newMockCartRepo(t)
	rows := sqlmock.NewRows([]string{"customer_id", "variant_id", "qty", "created_at"})
	mock.ExpectQuery(regexp.QuoteMeta("FROM cart_items")).WithArgs("cust-1").WillReturnRows(rows)

	items, err := repo.List(context.Background(), "cust-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if items == nil {
		t.Fatal("List returned nil, want a non-nil empty slice")
	}
	if len(items) != 0 {
		t.Fatalf("got %d items, want 0", len(items))
	}
}

func TestCartRepoAddUpsertsWithIncrementingQty(t *testing.T) {
	repo, mock := newMockCartRepo(t)

	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO cart_items")).
		WithArgs("cust-1", "var-1", 3).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.Add(context.Background(), "cust-1", "var-1", 3); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestCartRepoAddRejectsNonPositiveQty(t *testing.T) {
	repo, _ := newMockCartRepo(t)

	err := repo.Add(context.Background(), "cust-1", "var-1", 0)
	if err == nil {
		t.Fatal("Add(qty=0) succeeded, want an error")
	}
	appErr, ok := err.(*apperr.AppError)
	if !ok {
		t.Fatalf("got error type %T, want *apperr.AppError", err)
	}
	if appErr.Code != "invalid_qty" {
		t.Errorf("Code = %q, want invalid_qty", appErr.Code)
	}
}

func TestCartRepoAddRejectsEmptyVariantID(t *testing.T) {
	repo, _ := newMockCartRepo(t)

	err := repo.Add(context.Background(), "cust-1", "", 1)
	if err == nil {
		t.Fatal("Add(variantID=\"\") succeeded, want an error")
	}
}

func TestCartRepoUpdateQtySetsQty(t *testing.T) {
	repo, mock := newMockCartRepo(t)

	mock.ExpectExec(regexp.QuoteMeta("UPDATE cart_items SET qty")).
		WithArgs("cust-1", "var-1", 5).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.UpdateQty(context.Background(), "cust-1", "var-1", 5); err != nil {
		t.Fatalf("UpdateQty: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestCartRepoUpdateQtyZeroDeletesInstead(t *testing.T) {
	repo, mock := newMockCartRepo(t)

	// qty<=0 should route to a DELETE, not an UPDATE ... SET qty=0 (per
	// the doc comment on UpdateQty: it removes the line instead).
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM cart_items")).
		WithArgs("cust-1", "var-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.UpdateQty(context.Background(), "cust-1", "var-1", 0); err != nil {
		t.Fatalf("UpdateQty(0): %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestCartRepoUpdateQtyNotFoundWhenNoRowsAffected(t *testing.T) {
	repo, mock := newMockCartRepo(t)

	mock.ExpectExec(regexp.QuoteMeta("UPDATE cart_items SET qty")).
		WithArgs("cust-1", "missing-variant", 5).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err := repo.UpdateQty(context.Background(), "cust-1", "missing-variant", 5)
	if err == nil {
		t.Fatal("UpdateQty on a missing line succeeded, want apperr.NotFound")
	}
	appErr, ok := err.(*apperr.AppError)
	if !ok || appErr.Code != "cart_item_not_found" {
		t.Fatalf("got %v, want apperr with code cart_item_not_found", err)
	}
}

func TestCartRepoRemoveDeletesRow(t *testing.T) {
	repo, mock := newMockCartRepo(t)

	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM cart_items")).
		WithArgs("cust-1", "var-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.Remove(context.Background(), "cust-1", "var-1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
