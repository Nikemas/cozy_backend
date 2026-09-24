package payments

import (
	"context"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

func TestExpirePendingCancelsOrderAndReturnsStock(t *testing.T) {
	svc, mock := newMockService(t, NewMockProvider("", "tok"), &fakeCreator{})

	mock.ExpectQuery(regexp.QuoteMeta("WHERE status = 'pending' AND created_at < now() - make_interval(secs => $1)")).
		WithArgs((30 * time.Minute).Seconds(), expiryBatch).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("pay-1").AddRow("pay-2"))

	// pay-1: still pending → expired.
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT order_id, status FROM payments WHERE id = $1 FOR UPDATE")).
		WithArgs("pay-1").WillReturnRows(sqlmock.NewRows([]string{"order_id", "status"}).AddRow("order-1", "pending"))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE payments SET status = 'cancelled'")).
		WithArgs("pay-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE orders SET payment_status = 'cancelled'")).
		WithArgs("order-1").WillReturnResult(sqlmock.NewResult(0, 1))
	expectCancelUnpaid(mock, StatusCancelled, "не оплачен вовремя")
	mock.ExpectCommit()

	// pay-2: a callback settled it between the scan and the lock → untouched.
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("FOR UPDATE")).
		WithArgs("pay-2").WillReturnRows(sqlmock.NewRows([]string{"order_id", "status"}).AddRow("order-2", "paid"))
	mock.ExpectCommit()

	n, err := svc.ExpirePending(context.Background(), 30*time.Minute)
	if err != nil {
		t.Fatalf("ExpirePending: %v", err)
	}
	if n != 1 {
		t.Errorf("expired = %d, want 1", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestRunPendingExpiryStopsOnCancel(t *testing.T) {
	svc, mock := newMockService(t, NewMockProvider("", "tok"), &fakeCreator{})
	mock.MatchExpectationsInOrder(false)
	for i := 0; i < 5; i++ {
		mock.ExpectQuery(regexp.QuoteMeta("FROM payments")).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := RunPendingExpiry(ctx, svc, time.Minute, time.Hour)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expiry loop did not stop after context cancel")
	}
}
