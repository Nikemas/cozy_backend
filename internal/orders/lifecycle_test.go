package orders

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

type cancelRecorder struct {
	recordingNotifier
	cancelled []Order
}

func (c *cancelRecorder) OrderCancelledByCustomer(o Order) { c.cancelled = append(c.cancelled, o) }

func wantAppErr(t *testing.T, err error, status int, code string) {
	t.Helper()
	appErr, ok := err.(*apperr.AppError)
	if !ok || appErr.Status != status || appErr.Code != code {
		t.Fatalf("got %v (%T), want %d %s", err, err, status, code)
	}
}

func expectCancelUpdate(mock sqlmock.Sqlmock, payment any, refund bool, actor ActorType, staffID any) {
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE orders SET status = 'cancelled'")).
		WithArgs("order-1", payment, refund).
		WillReturnRows(sqlmock.NewRows([]string{"updated_at"}).AddRow(time.Now()))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO order_status_history")).
		WithArgs("order-1", sqlmock.AnyArg(), StatusCancelled, actor, staffID, nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// --- admin: owner-only cancel lives in the service ---

func TestAdminUpdateStatusManagerCannotCancel(t *testing.T) {
	svc, mock := newMockService(t)
	for _, role := range []staff.Role{staff.RoleManager, staff.RolePointStaff} {
		_, err := svc.AdminUpdateStatus(staffCtx(role), "order-1", StatusCancelled)
		wantAppErr(t, err, 403, "cancel_forbidden")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("must be rejected before touching the DB: %v", err)
	}
}

func TestAdminUpdateStatusRequiresStaffIdentity(t *testing.T) {
	svc, _ := newMockService(t)
	_, err := svc.AdminUpdateStatus(context.Background(), "order-1", StatusConfirmed)
	wantAppErr(t, err, 403, "forbidden")
}

// TestAdminCancelRestocksConfirmedOrder: an owner cancelling a confirmed
// cash order returns its stock in the same transaction.
func TestAdminCancelRestocksConfirmedOrder(t *testing.T) {
	rec := &recordingNotifier{}
	svc, mock := newNotifyingService(t, rec)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("FOR UPDATE")).WillReturnRows(codOrder(StatusConfirmed).rows())
	expectRestock(mock, testVar1, 2)
	expectCancelUpdate(mock, nil, false, ActorStaff, "staff-1")
	mock.ExpectCommit()
	expectNoItems(mock)

	o, err := svc.AdminUpdateStatus(staffCtx(staff.RoleOwner), "order-1", StatusCancelled)
	if err != nil {
		t.Fatalf("AdminUpdateStatus: %v", err)
	}
	if o.Status != StatusCancelled || o.RefundRequired {
		t.Errorf("got status %s refund %v", o.Status, o.RefundRequired)
	}
	if len(rec.changed) != 1 || rec.from[0] != StatusConfirmed {
		t.Errorf("want one status push from confirmed, got %+v", rec.from)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestAdminCancelPaidOnlineOrderFlagsRefund: cancelling an order that was
// paid online restocks and sets refund_required.
func TestAdminCancelPaidOnlineOrderFlagsRefund(t *testing.T) {
	svc, mock := newNotifyingService(t, &recordingNotifier{})

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("FOR UPDATE")).WillReturnRows(onlineOrder(StatusConfirmed, PaymentPaid).rows())
	expectRestock(mock, testVar1, 1)
	paid := PaymentPaid
	expectCancelUpdate(mock, &paid, true, ActorStaff, "staff-1")
	mock.ExpectCommit()
	expectNoItems(mock)

	o, err := svc.AdminUpdateStatus(staffCtx(staff.RoleOwner), "order-1", StatusCancelled)
	if err != nil {
		t.Fatalf("AdminUpdateStatus: %v", err)
	}
	if !o.RefundRequired {
		t.Error("cancelled paid order must be refund_required")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestAdminCancelPendingOnlineOrderCancelsPayment: the still-pending
// payment is cancelled too, so a late "paid" is caught as a refund case.
func TestAdminCancelPendingOnlineOrderCancelsPayment(t *testing.T) {
	svc, mock := newNotifyingService(t, &recordingNotifier{})

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("FOR UPDATE")).WillReturnRows(onlineOrder(StatusPlaced, PaymentPending).rows())
	expectRestock(mock, testVar1, 1)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE payments SET status = 'cancelled'")).
		WithArgs("order-1").WillReturnResult(sqlmock.NewResult(0, 1))
	cancelled := PaymentCancelled
	expectCancelUpdate(mock, &cancelled, false, ActorStaff, "staff-1")
	mock.ExpectCommit()
	expectNoItems(mock)

	o, err := svc.AdminUpdateStatus(staffCtx(staff.RoleOwner), "order-1", StatusCancelled)
	if err != nil {
		t.Fatalf("AdminUpdateStatus: %v", err)
	}
	if o.PaymentStatus == nil || *o.PaymentStatus != PaymentCancelled {
		t.Errorf("payment_status = %v, want cancelled", o.PaymentStatus)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// --- payment gate on confirm ---

func TestAdminConfirmUnpaidOnlineOrderIsRejected(t *testing.T) {
	for _, ps := range []PaymentStatus{PaymentPending, PaymentFailed, PaymentCancelled} {
		svc, mock := newNotifyingService(t, &recordingNotifier{})
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("FOR UPDATE")).WillReturnRows(onlineOrder(StatusPlaced, ps).rows())
		mock.ExpectRollback()

		_, err := svc.AdminUpdateStatus(staffCtx(staff.RoleManager), "order-1", StatusConfirmed)
		wantAppErr(t, err, 409, "payment_not_completed")
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("%s: unmet expectations: %v", ps, err)
		}
	}
}

func TestAdminConfirmPaidOnlineOrder(t *testing.T) {
	svc, mock := newNotifyingService(t, &recordingNotifier{})
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("FOR UPDATE")).WillReturnRows(onlineOrder(StatusPlaced, PaymentPaid).rows())
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE orders SET status")).
		WithArgs(StatusConfirmed, "order-1").
		WillReturnRows(sqlmock.NewRows([]string{"updated_at"}).AddRow(time.Now()))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO order_status_history")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	expectNoItems(mock)

	if _, err := svc.AdminUpdateStatus(staffCtx(staff.RoleManager), "order-1", StatusConfirmed); err != nil {
		t.Fatalf("confirming a paid online order: %v", err)
	}
}

// --- customer cancel ---

func TestCancelByCustomerRestocksAndNotifiesStaff(t *testing.T) {
	rec := &cancelRecorder{}
	svc, mock := newNotifyingService(t, rec)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("WHERE customer_id = $1 AND order_number = $2 FOR UPDATE")).
		WithArgs("cust-1", "COZY-20260923-001").WillReturnRows(codOrder(StatusPlaced).rows())
	expectRestock(mock, testVar1, 2)
	expectCancelUpdate(mock, nil, false, ActorCustomer, nil)
	mock.ExpectCommit()
	expectNoItems(mock)

	o, err := svc.CancelByCustomer(context.Background(), "cust-1", "COZY-20260923-001")
	if err != nil {
		t.Fatalf("CancelByCustomer: %v", err)
	}
	if o.Status != StatusCancelled {
		t.Errorf("status = %s", o.Status)
	}
	if len(rec.cancelled) != 1 {
		t.Errorf("staff must hear about a customer cancel, got %d", len(rec.cancelled))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestCancelByCustomerRejectsConfirmedOrPaid(t *testing.T) {
	for name, row := range map[string]orderRow{
		"confirmed cash": codOrder(StatusConfirmed),
		"placed paid":    onlineOrder(StatusPlaced, PaymentPaid),
		"cancelled":      codOrder(StatusCancelled),
	} {
		svc, mock := newNotifyingService(t, &recordingNotifier{})
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("FOR UPDATE")).WillReturnRows(row.rows())
		mock.ExpectRollback()

		_, err := svc.CancelByCustomer(context.Background(), "cust-1", "COZY-20260923-001")
		if appErr, ok := err.(*apperr.AppError); !ok || appErr.Code != "order_not_cancellable" || appErr.Status != 409 {
			t.Errorf("%s: got %v, want 409 order_not_cancellable", name, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("%s: unmet expectations: %v", name, err)
		}
	}
}

func TestCustomerCanCancel(t *testing.T) {
	pending, paid := PaymentPending, PaymentPaid
	cases := []struct {
		o    Order
		want bool
	}{
		{Order{Status: StatusPlaced, PaymentMethod: PaymentCashOnDelivery}, true},
		{Order{Status: StatusPlaced, PaymentMethod: PaymentOnlineCard, PaymentStatus: &pending}, true},
		{Order{Status: StatusPlaced, PaymentMethod: PaymentOnlineCard, PaymentStatus: &paid}, false},
		{Order{Status: StatusConfirmed, PaymentMethod: PaymentCashOnDelivery}, false},
	}
	for _, c := range cases {
		if got := CustomerCanCancel(c.o); got != c.want {
			t.Errorf("CustomerCanCancel(%+v) = %v, want %v", c.o, got, c.want)
		}
	}
}

// --- idempotency / open-orders limit / cart clearing ---

func TestPlaceOrderIdempotentReplayReturnsExistingOrder(t *testing.T) {
	rec := &recordingNotifier{}
	svc, mock := newNotifyingService(t, rec)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("pg_advisory_xact_lock")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("AND idempotency_key = $2")).
		WithArgs("cust-1", "key-1", IdempotencyWindow.Seconds()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "fresh"}).AddRow("order-1", true))
	mock.ExpectCommit()
	// Replay loads the existing order (customer-scoped) with its items.
	mock.ExpectQuery(regexp.QuoteMeta("FROM orders WHERE customer_id = $1 AND order_number = $2")).
		WithArgs("cust-1", "order-1").WillReturnRows(codOrder(StatusPlaced).rows())
	expectNoItems(mock)

	pickupID := "point-1"
	o, created, err := svc.PlaceOrder(context.Background(), PlaceOrderInput{
		CustomerID: "cust-1", Items: []OrderItemInput{{VariantID: testVar1, Quantity: 1}},
		PickupPointID: &pickupID, IdempotencyKey: "key-1",
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if created || o.ID != "order-1" {
		t.Errorf("created=%v id=%s, want replay of order-1", created, o.ID)
	}
	if len(rec.created) != 0 {
		t.Error("a replay must not notify staff again")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("no stock/insert must run on a replay: %v", err)
	}
}

func TestFindIdempotentOrderClearsStaleKey(t *testing.T) {
	withMockTx(t, func(tx *sql.Tx, mock sqlmock.Sqlmock) {
		mock.ExpectQuery(regexp.QuoteMeta("AND idempotency_key = $2")).
			WillReturnRows(sqlmock.NewRows([]string{"id", "fresh"}).AddRow("old-order", false))
		mock.ExpectExec(regexp.QuoteMeta("UPDATE orders SET idempotency_key = NULL WHERE id = $1")).
			WithArgs("old-order").WillReturnResult(sqlmock.NewResult(0, 1))

		id, err := findIdempotentOrder(context.Background(), tx, "cust-1", "key-1")
		if err != nil || id != "" {
			t.Fatalf("stale key: got %q, %v; want \"\", nil", id, err)
		}
	})
}

func TestPlaceOrderOpenOrdersLimit(t *testing.T) {
	svc, mock := newNotifyingService(t, &recordingNotifier{})
	expectOrderPrologue(mock, 5)
	mock.ExpectRollback()

	pickupID := "point-1"
	_, _, err := svc.PlaceOrder(context.Background(), PlaceOrderInput{
		CustomerID: "cust-1", Items: []OrderItemInput{{VariantID: testVar1, Quantity: 1}}, PickupPointID: &pickupID,
	})
	wantAppErr(t, err, 409, "too_many_open_orders")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestPlaceOrderClearCartInSameTransaction(t *testing.T) {
	svc, mock := newNotifyingService(t, &recordingNotifier{})
	expectOrderPrologue(mock, 0)
	expectPickupLine(mock, testVar1, 5000, 10, 1)
	expectOrderInsert(mock, nil)
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM cart_items WHERE customer_id = $1 AND variant_id IN ($2)")).
		WithArgs("cust-1", testVar1).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	pickupID := "point-1"
	if _, _, err := svc.PlaceOrder(context.Background(), PlaceOrderInput{
		CustomerID: "cust-1", Items: []OrderItemInput{{VariantID: testVar1, Quantity: 1}}, PickupPointID: &pickupID,
		ClearCart: true,
	}); err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("cart must be cleared before COMMIT: %v", err)
	}
}

// --- notify on paid ---

func TestNotifyOrderPaid(t *testing.T) {
	rec := &recordingNotifier{}
	svc, mock := newNotifyingService(t, rec)
	mock.ExpectQuery(regexp.QuoteMeta("FROM orders WHERE id = $1::uuid")).
		WillReturnRows(onlineOrder(StatusPlaced, PaymentPaid).rows())
	expectNoItems(mock)
	if err := svc.NotifyOrderPaid(context.Background(), "3f1c9a2e-8b7d-4c1a-9e2f-0a1b2c3d4e5f"); err != nil {
		t.Fatal(err)
	}
	if len(rec.created) != 1 {
		t.Fatalf("paid order must be announced to staff once, got %d", len(rec.created))
	}

	mock.ExpectQuery(regexp.QuoteMeta("FROM orders WHERE id = $1::uuid")).
		WillReturnRows(onlineOrder(StatusCancelled, PaymentPaid).rows())
	expectNoItems(mock)
	if err := svc.NotifyOrderPaid(context.Background(), "3f1c9a2e-8b7d-4c1a-9e2f-0a1b2c3d4e5f"); err != nil {
		t.Fatal(err)
	}
	if len(rec.created) != 1 {
		t.Error("a cancelled order must not be announced as new")
	}
}

// --- history ---

func TestAdminGetOrderIncludesHistory(t *testing.T) {
	svc, mock := newMockService(t)
	mock.ExpectQuery(regexp.QuoteMeta("FROM orders WHERE order_number = $1")).
		WillReturnRows(codOrder(StatusConfirmed).rows())
	expectNoItems(mock)
	now := time.Now()
	mock.ExpectQuery(regexp.QuoteMeta("FROM order_status_history h")).WithArgs("order-1").
		WillReturnRows(sqlmock.NewRows([]string{"from_status", "to_status", "actor_type", "actor_staff_id", "name", "note", "created_at"}).
			AddRow(nil, "placed", "customer", nil, nil, nil, now).
			AddRow("placed", "confirmed", "staff", "staff-1", "Айгуль", nil, now))

	o, err := svc.AdminGetOrder(context.Background(), "COZY-20260923-001")
	if err != nil {
		t.Fatalf("AdminGetOrder: %v", err)
	}
	if len(o.History) != 2 || o.History[1].ActorName == nil || *o.History[1].ActorName != "Айгуль" ||
		o.History[0].FromStatus != nil || *o.History[1].FromStatus != StatusPlaced {
		t.Fatalf("history = %+v", o.History)
	}
}

// --- settings ---

func TestSettingsFromLookup(t *testing.T) {
	env := func(m map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
	}
	s, err := settingsFromLookup(env(nil))
	if err != nil || s != DefaultSettings() {
		t.Fatalf("defaults: %+v, %v", s, err)
	}
	if s.DeliveryFee != 200 || s.MaxOpenOrders != 5 || s.PaymentPendingTTL != 30*time.Minute {
		t.Errorf("defaults = %+v, want 200 / 5 / 30m", s)
	}
	s, err = settingsFromLookup(env(map[string]string{
		"DELIVERY_FEE_SOM": "150.5", "MAX_OPEN_ORDERS_PER_CUSTOMER": "0", "PAYMENT_PENDING_TTL": "45m",
	}))
	if err != nil || s.DeliveryFee != 150.5 || s.MaxOpenOrders != 0 || s.PaymentPendingTTL != 45*time.Minute {
		t.Errorf("custom = %+v, %v", s, err)
	}
	for _, bad := range []map[string]string{
		{"DELIVERY_FEE_SOM": "-1"}, {"DELIVERY_FEE_SOM": "abc"},
		{"MAX_OPEN_ORDERS_PER_CUSTOMER": "-2"}, {"PAYMENT_PENDING_TTL": "10s"}, {"PAYMENT_PENDING_TTL": "soon"},
	} {
		if _, err := settingsFromLookup(env(bad)); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	if (Settings{DeliveryFee: 200}).DeliveryFeeFor(false) != 0 {
		t.Error("pickup must be free")
	}
}
