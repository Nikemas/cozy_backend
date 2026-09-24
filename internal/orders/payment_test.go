package orders

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// TestCreateOnlineOrderInsertsPendingPayment drives CreateOnlineOrder
// through the same sequence as TestCreateOrderPickupHappyPath (plus the
// idempotency lookup), checking the order is written as online_card/
// pending with its key and a pending payments row is inserted in the same
// transaction (before COMMIT) — and that staff are NOT notified yet (only
// once paid, see NotifyOrderPaid).
func TestCreateOnlineOrderInsertsPendingPayment(t *testing.T) {
	rec := &recordingNotifier{}
	svc, mock := newNotifyingService(t, rec)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("pg_advisory_xact_lock")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("AND idempotency_key = $2")).
		WithArgs("cust-1", "key-1", IdempotencyWindow.Seconds()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "fresh"}))
	mock.ExpectQuery(regexp.QuoteMeta("status IN ('placed'")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	expectPickupLine(mock, testVar1, 5000, 10, 1)
	pending := PaymentPending
	expectOrderInsert(mock, []driver.Value{sqlmock.AnyArg(), "cust-1", nil, "point-1", StatusPlaced, PaymentOnlineCard, &pending,
		5000.0, 0.0, nil, "key-1", nil})
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO payments")).
		WithArgs("order-1", "mock", 5000.0).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("pay-1"))
	mock.ExpectCommit()

	pickupID := "point-1"
	order, paymentID, created, err := svc.CreateOnlineOrder(context.Background(), PlaceOrderInput{
		CustomerID: "cust-1", Items: []OrderItemInput{{VariantID: testVar1, Quantity: 1}}, PickupPointID: &pickupID,
		IdempotencyKey: "key-1",
	}, "mock")
	if err != nil || !created {
		t.Fatalf("CreateOnlineOrder = %v, %v", created, err)
	}
	if paymentID != "pay-1" {
		t.Errorf("paymentID = %q", paymentID)
	}
	if order.PaymentMethod != PaymentOnlineCard || order.PaymentStatus == nil || *order.PaymentStatus != PaymentPending {
		t.Errorf("order payment = %s/%v, want online_card/pending", order.PaymentMethod, order.PaymentStatus)
	}
	if len(rec.created) != 0 {
		t.Errorf("staff notified about an unpaid online order: %+v", rec.created)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func withMockTx(t *testing.T, fn func(tx *sql.Tx, mock sqlmock.Sqlmock)) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	fn(tx, mock)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestCancelUnpaidOrderTxRestocksInVariantOrder(t *testing.T) {
	withMockTx(t, func(tx *sql.Tx, mock sqlmock.Sqlmock) {
		mock.ExpectQuery(regexp.QuoteMeta("FROM orders WHERE id = $1 FOR UPDATE")).
			WithArgs("order-1").WillReturnRows(onlineOrder(StatusPlaced, PaymentFailed).rows())
		mock.ExpectQuery(regexp.QuoteMeta("SELECT variant_id, quantity FROM order_items")).
			WithArgs("order-1").
			WillReturnRows(sqlmock.NewRows([]string{"variant_id", "quantity"}).AddRow("var-b", 1).AddRow("var-a", 3))
		// Sorted: var-a before var-b — the lock order CreateOrder uses.
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO stock")).WithArgs("var-a", "point-1", 3).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO stock")).WithArgs("var-b", "point-1", 1).WillReturnResult(sqlmock.NewResult(0, 1))
		// Payment already failed: no payments update, no refund flag.
		failed := PaymentFailed
		mock.ExpectQuery(regexp.QuoteMeta("UPDATE orders SET status = 'cancelled'")).
			WithArgs("order-1", &failed, false).
			WillReturnRows(sqlmock.NewRows([]string{"updated_at"}).AddRow(time.Now()))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO order_status_history")).
			WithArgs("order-1", "placed", StatusCancelled, ActorSystem, nil, "оплата отклонена").
			WillReturnResult(sqlmock.NewResult(0, 1))

		cancelled, err := CancelUnpaidOrderTx(context.Background(), tx, "order-1", "оплата отклонена")
		if err != nil || !cancelled {
			t.Fatalf("CancelUnpaidOrderTx = %v, %v; want true, nil", cancelled, err)
		}
	})
}

func TestCancelUnpaidOrderTxLeavesNonPlacedOrderAlone(t *testing.T) {
	for _, st := range []OrderStatus{StatusConfirmed, StatusCancelled, StatusDelivered} {
		withMockTx(t, func(tx *sql.Tx, mock sqlmock.Sqlmock) {
			mock.ExpectQuery(regexp.QuoteMeta("FROM orders WHERE id = $1 FOR UPDATE")).
				WithArgs("order-1").WillReturnRows(onlineOrder(st, PaymentFailed).rows())
			// no stock / order writes expected

			cancelled, err := CancelUnpaidOrderTx(context.Background(), tx, "order-1", "")
			if err != nil || cancelled {
				t.Fatalf("status %s: CancelUnpaidOrderTx = %v, %v; want false, nil", st, cancelled, err)
			}
		})
	}
}

func TestPaymentStatusValid(t *testing.T) {
	for _, s := range []PaymentStatus{PaymentPending, PaymentPaid, PaymentFailed, PaymentCancelled, PaymentRefunded} {
		if !s.Valid() {
			t.Errorf("%s should be valid", s)
		}
	}
	if PaymentStatus("bogus").Valid() || PaymentStatus("").Valid() {
		t.Error("bogus/empty should be invalid")
	}
}

func TestPaymentRetryable(t *testing.T) {
	st := func(p PaymentStatus) *PaymentStatus { return &p }
	cases := []struct {
		o    Order
		want bool
	}{
		{Order{PaymentMethod: PaymentOnlineCard, Status: StatusPlaced, PaymentStatus: st(PaymentPending)}, true},
		{Order{PaymentMethod: PaymentOnlineCard, Status: StatusPlaced, PaymentStatus: st(PaymentFailed)}, true},
		{Order{PaymentMethod: PaymentOnlineCard, Status: StatusPlaced, PaymentStatus: st(PaymentPaid)}, false},
		{Order{PaymentMethod: PaymentOnlineCard, Status: StatusPlaced, PaymentStatus: st(PaymentCancelled)}, false},
		{Order{PaymentMethod: PaymentOnlineCard, Status: StatusCancelled, PaymentStatus: st(PaymentFailed)}, false},
		{Order{PaymentMethod: PaymentCashOnDelivery, Status: StatusPlaced}, false},
	}
	for i, c := range cases {
		if got := PaymentRetryable(c.o); got != c.want {
			t.Errorf("case %d: PaymentRetryable = %v, want %v", i, got, c.want)
		}
	}
}

// TestPrepareRetryPaymentReplacesPendingAttempt: under the order lock the
// old pending attempt is closed, a new one inserted for the order total,
// and the order is back to payment_status pending.
func TestPrepareRetryPaymentReplacesPendingAttempt(t *testing.T) {
	svc, mock := newMockService(t)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("FROM orders WHERE customer_id = $1 AND id = $2::uuid FOR UPDATE")).
		WithArgs("cust-1", testVar1).WillReturnRows(onlineOrder(StatusPlaced, PaymentFailed).rows())
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM payments WHERE order_id = $1")).
		WithArgs("order-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE payments SET status = 'cancelled', updated_at = now() WHERE order_id = $1 AND status = 'pending'")).
		WithArgs("order-1").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO payments")).
		WithArgs("order-1", "mock", 5000.0).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("pay-3"))
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE orders SET payment_status = 'pending'")).
		WithArgs("order-1").WillReturnRows(sqlmock.NewRows([]string{"updated_at"}).AddRow(time.Now()))
	mock.ExpectCommit()

	o, paymentID, err := svc.PrepareRetryPayment(context.Background(), "cust-1", testVar1, "mock")
	if err != nil {
		t.Fatalf("PrepareRetryPayment: %v", err)
	}
	if paymentID != "pay-3" || o.PaymentStatus == nil || *o.PaymentStatus != PaymentPending {
		t.Errorf("paymentID/status = %q/%v", paymentID, o.PaymentStatus)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestPrepareRetryPaymentRejects(t *testing.T) {
	cases := []struct {
		name     string
		row      orderRow
		attempts int // -1: not reached
	}{
		{"paid", onlineOrder(StatusPlaced, PaymentPaid), -1},
		{"cancelled order", onlineOrder(StatusCancelled, PaymentFailed), -1},
		{"cash", codOrder(StatusPlaced), -1},
		{"too many attempts", onlineOrder(StatusPlaced, PaymentFailed), MaxPaymentAttempts},
	}
	for _, c := range cases {
		svc, mock := newMockService(t)
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("FOR UPDATE")).WillReturnRows(c.row.rows())
		if c.attempts >= 0 {
			mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM payments")).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(c.attempts))
		}
		mock.ExpectRollback()

		_, _, err := svc.PrepareRetryPayment(context.Background(), "cust-1", "COZY-20260923-001", "mock")
		var appErr *apperr.AppError
		if !errors.As(err, &appErr) || appErr.Code != "payment_not_retryable" || appErr.Status != 409 {
			t.Errorf("%s: err = %v, want 409 payment_not_retryable", c.name, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}
