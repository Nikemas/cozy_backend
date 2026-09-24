package payments

import (
	"context"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/orders"
)

func TestExpirePendingCancelsOrderAndReturnsStock(t *testing.T) {
	svc, mock := newMockService(t, NewMockProvider("", "tok"), &fakeCreator{})

	mock.ExpectQuery(regexp.QuoteMeta("SELECT o.id FROM orders o WHERE")+"(?s).*"+
		regexp.QuoteMeta("ORDER BY p.created_at DESC, p.id DESC LIMIT 1")).
		WithArgs((30 * time.Minute).Seconds(), expiryBatch).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("order-1").AddRow("order-2"))

	// order-1: still abandoned under the lock → cancelled, stock back.
	mock.ExpectBegin()
	expectLockOrder(mock, orders.StatusPlaced, StatusPending)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS (SELECT 1 FROM orders o WHERE")).
		WithArgs((30 * time.Minute).Seconds(), "order-1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	expectCancelUnpaid(mock, StatusPending, "не оплачен вовремя")
	mock.ExpectCommit()

	// order-2: paid/retried between the scan and the lock → untouched.
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("FROM orders WHERE id = $1 FOR UPDATE")).
		WithArgs("order-2").WillReturnRows(orderLockRows(orders.StatusPlaced, StatusPaid))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS")).
		WithArgs((30 * time.Minute).Seconds(), "order-2").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
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
		mock.ExpectQuery(regexp.QuoteMeta("FROM orders o")).WillReturnRows(sqlmock.NewRows([]string{"id"}))
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
