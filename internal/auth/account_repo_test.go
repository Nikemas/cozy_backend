package auth

import (
	"context"
	"net/http"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

func newMockAccountRepo(t *testing.T) (*accountRepo, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &accountRepo{db: db}, mock
}

func expectLockCustomer(mock sqlmock.Sqlmock, deletedAt any) {
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT deleted_at FROM customers WHERE id = $1 FOR UPDATE`)).
		WithArgs("cust-1").WillReturnRows(sqlmock.NewRows([]string{"deleted_at"}).AddRow(deletedAt))
}

func TestDeleteCustomerAnonymizesAndCleansUp(t *testing.T) {
	repo, mock := newMockAccountRepo(t)
	expectLockCustomer(mock, nil)
	mock.ExpectQuery(regexp.QuoteMeta(`status NOT IN ('delivered', 'cancelled')`)).
		WithArgs("cust-1").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	for _, q := range []string{
		`DELETE FROM cart_items WHERE customer_id = $1`,
		`DELETE FROM favorites WHERE customer_id = $1`,
		`DELETE FROM device_tokens WHERE customer_id = $1`,
		`DELETE FROM customer_addresses a WHERE a.customer_id = $1`,
		`UPDATE customer_addresses SET label = NULL, is_default = false WHERE customer_id = $1`,
		`UPDATE refresh_tokens SET revoked_at = now() WHERE customer_id = $1 AND revoked_at IS NULL`,
		`UPDATE customers SET phone = 'deleted:' || id::text, name = NULL, deleted_at = now()`,
	} {
		mock.ExpectExec(regexp.QuoteMeta(q)).WithArgs("cust-1").WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()

	if err := repo.deleteCustomer(context.Background(), "cust-1"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestDeleteCustomerRefusesWithActiveOrders(t *testing.T) {
	repo, mock := newMockAccountRepo(t)
	expectLockCustomer(mock, nil)
	mock.ExpectQuery(`SELECT EXISTS`).WithArgs("cust-1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectRollback()

	err := repo.deleteCustomer(context.Background(), "cust-1")
	wantAppErr(t, err, http.StatusConflict, "has_active_orders")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestDeleteCustomerIsIdempotent(t *testing.T) {
	repo, mock := newMockAccountRepo(t)
	expectLockCustomer(mock, time.Now())
	mock.ExpectCommit()
	if err := repo.deleteCustomer(context.Background(), "cust-1"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
