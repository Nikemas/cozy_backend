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
		AddRow("cust-1", testVar1, 2, now).
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
	if items[0].VariantID != testVar1 || items[0].Qty != 2 {
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

	mock.ExpectQuery(regexp.QuoteMeta("SELECT p.is_active FROM product_variants pv")).
		WithArgs(testVar1).WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectExec(regexp.QuoteMeta("DO UPDATE SET qty = LEAST(cart_items.qty + EXCLUDED.qty, $4)")).
		WithArgs("cust-1", testVar1, 3, MaxCartQty).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.Add(context.Background(), "cust-1", testVar1, 3); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestCartRepoAddRejectsNonPositiveQty(t *testing.T) {
	repo, _ := newMockCartRepo(t)

	err := repo.Add(context.Background(), "cust-1", testVar1, 0)
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
		WithArgs("cust-1", testVar1, 5).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.UpdateQty(context.Background(), "cust-1", testVar1, 5); err != nil {
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
		WithArgs("cust-1", testVar1).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.UpdateQty(context.Background(), "cust-1", testVar1, 0); err != nil {
		t.Fatalf("UpdateQty(0): %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestCartRepoUpdateQtyNotFoundWhenNoRowsAffected(t *testing.T) {
	repo, mock := newMockCartRepo(t)

	mock.ExpectExec(regexp.QuoteMeta("UPDATE cart_items SET qty")).
		WithArgs("cust-1", testVarMissing, 5).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err := repo.UpdateQty(context.Background(), "cust-1", testVarMissing, 5)
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
		WithArgs("cust-1", testVar1).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.Remove(context.Background(), "cust-1", testVar1); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestCartRepoAddUnknownVariantIs404(t *testing.T) {
	repo, mock := newMockCartRepo(t)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT p.is_active FROM product_variants pv")).
		WithArgs(testVarMissing).WillReturnRows(sqlmock.NewRows([]string{"is_active"}))

	err := repo.Add(context.Background(), "cust-1", testVarMissing, 1)
	appErr, ok := err.(*apperr.AppError)
	if !ok || appErr.Code != "variant_not_found" || appErr.Status != 404 {
		t.Fatalf("got %v, want 404 variant_not_found (not an FK-violation 500)", err)
	}
}

func TestCartRepoAddInactiveProductIs409(t *testing.T) {
	repo, mock := newMockCartRepo(t)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT p.is_active FROM product_variants pv")).
		WithArgs(testVar1).WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(false))

	err := repo.Add(context.Background(), "cust-1", testVar1, 1)
	appErr, ok := err.(*apperr.AppError)
	if !ok || appErr.Code != "product_unavailable" {
		t.Fatalf("got %v, want product_unavailable", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("no insert must follow: %v", err)
	}
}

func TestCartRepoRejectsMalformedVariantAndHugeQty(t *testing.T) {
	repo, mock := newMockCartRepo(t)
	ctx := context.Background()
	for name, err := range map[string]error{
		"add bad id":     repo.Add(ctx, "cust-1", "not-a-uuid", 1),
		"update bad id":  repo.UpdateQty(ctx, "cust-1", "not-a-uuid", 1),
		"remove bad id":  repo.Remove(ctx, "cust-1", "not-a-uuid"),
		"add huge qty":   repo.Add(ctx, "cust-1", testVar1, MaxCartQty+1),
		"update huge qt": repo.UpdateQty(ctx, "cust-1", testVar1, MaxCartQty+1),
	} {
		appErr, ok := err.(*apperr.AppError)
		if !ok || appErr.Status != 400 {
			t.Errorf("%s: got %v, want a 400", name, err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("validation must fail before SQL: %v", err)
	}
}

func TestCartRepoListDetailedOneQuery(t *testing.T) {
	repo, mock := newMockCartRepo(t)
	now := time.Now()
	mock.ExpectQuery(regexp.QuoteMeta("FROM cart_items ci")).WithArgs("cust-1").
		WillReturnRows(sqlmock.NewRows([]string{"variant_id", "qty", "id", "name_ru", "name_ky", "size", "color", "price", "is_active", "in_stock", "created_at"}).
			AddRow(testVar1, 2, "prod-1", "Air Max", "Air Max KY", "42", "Черный", 5000.0, true, 5, now).
			AddRow(testVar2, 1, "prod-2", "Old Boot", "Old Boot KY", "40", "Белый", 3000.0, false, 3, now).
			AddRow(testVarMissing, 3, "prod-3", "Rare", "Rare KY", "41", "Синий", 7000.0, true, 1, now))

	lines, err := repo.ListDetailed(context.Background(), "cust-1")
	if err != nil {
		t.Fatalf("ListDetailed: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("got %d lines", len(lines))
	}
	if !lines[0].Available() {
		t.Error("active product with enough stock must be available")
	}
	if lines[1].Available() {
		t.Error("inactive product must be unavailable")
	}
	if lines[2].Available() {
		t.Error("qty 3 with only 1 in stock must be unavailable")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
