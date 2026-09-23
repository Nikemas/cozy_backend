package orders

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// TestCreateOnlineOrderInsertsPendingPayment drives CreateOnlineOrder
// through the same sequence as TestCreateOrderPickupHappyPath, checking
// the order is written as online_card/pending and a pending payments row
// is inserted in the same transaction (before COMMIT).
func TestCreateOnlineOrderInsertsPendingPayment(t *testing.T) {
	svc, mock := newMockService(t)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT is_active FROM points_of_sale")).
		WithArgs("point-1").WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta("FROM product_variants pv")).
		WithArgs("var-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "size", "color", "price_override", "name_ru", "base_price"}).
			AddRow("var-1", "42", "Черный", nil, "Air Max", 5000.0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT quantity FROM stock")).
		WithArgs("var-1", "point-1").WillReturnRows(sqlmock.NewRows([]string{"quantity"}).AddRow(10))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE stock SET quantity")).
		WithArgs(1, "var-1", "point-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("pg_advisory_xact_lock")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM orders")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	pending := PaymentPending
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO orders")).
		WithArgs(sqlmock.AnyArg(), "cust-1", nil, sqlmock.AnyArg(), StatusPlaced, PaymentOnlineCard, &pending, 5000.0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow("order-1", time.Now(), time.Now()))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO order_items")).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("item-1"))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO payments")).
		WithArgs("order-1", "mock", 5000.0).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("pay-1"))
	mock.ExpectCommit()

	pickupID := "point-1"
	order, paymentID, err := svc.CreateOnlineOrder(context.Background(), "cust-1",
		[]OrderItemInput{{VariantID: "var-1", Quantity: 1}}, nil, &pickupID, "mock")
	if err != nil {
		t.Fatalf("CreateOnlineOrder: %v", err)
	}
	if paymentID != "pay-1" {
		t.Errorf("paymentID = %q", paymentID)
	}
	if order.PaymentMethod != PaymentOnlineCard || order.PaymentStatus == nil || *order.PaymentStatus != PaymentPending {
		t.Errorf("order payment = %s/%v, want online_card/pending", order.PaymentMethod, order.PaymentStatus)
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
		mock.ExpectQuery(regexp.QuoteMeta("SELECT status, point_id FROM orders WHERE id = $1 FOR UPDATE")).
			WithArgs("order-1").WillReturnRows(sqlmock.NewRows([]string{"status", "point_id"}).AddRow("placed", "point-1"))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT variant_id, quantity FROM order_items")).
			WithArgs("order-1").
			WillReturnRows(sqlmock.NewRows([]string{"variant_id", "quantity"}).AddRow("var-b", 1).AddRow("var-a", 3))
		// Sorted: var-a before var-b — the lock order CreateOrder uses.
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO stock")).WithArgs("var-a", "point-1", 3).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO stock")).WithArgs("var-b", "point-1", 1).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("UPDATE orders SET status = 'cancelled'")).WithArgs("order-1").WillReturnResult(sqlmock.NewResult(0, 1))

		cancelled, err := CancelUnpaidOrderTx(context.Background(), tx, "order-1")
		if err != nil || !cancelled {
			t.Fatalf("CancelUnpaidOrderTx = %v, %v; want true, nil", cancelled, err)
		}
	})
}

func TestCancelUnpaidOrderTxLeavesNonPlacedOrderAlone(t *testing.T) {
	for _, st := range []OrderStatus{StatusConfirmed, StatusCancelled, StatusDelivered} {
		withMockTx(t, func(tx *sql.Tx, mock sqlmock.Sqlmock) {
			mock.ExpectQuery(regexp.QuoteMeta("SELECT status, point_id FROM orders")).
				WithArgs("order-1").WillReturnRows(sqlmock.NewRows([]string{"status", "point_id"}).AddRow(string(st), "point-1"))
			// no stock / order writes expected

			cancelled, err := CancelUnpaidOrderTx(context.Background(), tx, "order-1")
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
