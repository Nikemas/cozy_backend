package payments

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/orders"
)

// --- transition (pure) ---

func TestTransition(t *testing.T) {
	cases := []struct {
		from, to        Status
		apply, conflict bool
	}{
		{StatusPending, StatusPaid, true, false},
		{StatusPending, StatusFailed, true, false},
		{StatusPending, StatusCancelled, true, false},
		{StatusPending, StatusPending, false, false},
		{StatusPaid, StatusPaid, false, false}, // duplicate
		{StatusPaid, StatusPending, false, false},
		{StatusPaid, StatusRefunded, true, false},
		{StatusPaid, StatusFailed, false, true}, // contradictory
		{StatusFailed, StatusPaid, false, true}, // paid after we cancelled
		{StatusCancelled, StatusPaid, false, true},
		{StatusFailed, StatusCancelled, false, false}, // both already cancelled the order
		{StatusCancelled, StatusFailed, false, false},
		{StatusPending, StatusRefunded, false, true},
	}
	for _, c := range cases {
		apply, conflict := transition(c.from, c.to)
		if apply != c.apply || conflict != c.conflict {
			t.Errorf("transition(%s -> %s) = (%v, %v), want (%v, %v)", c.from, c.to, apply, conflict, c.apply, c.conflict)
		}
	}
}

// --- providers ---

func TestMockProviderCreateAndParse(t *testing.T) {
	m := NewMockProvider("https://cozy.test", "tok")
	sess, err := m.CreatePayment(context.Background(), CreateRequest{Amount: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sess.ExternalID, "mock_") || sess.RedirectURL != "https://cozy.test"+MockCheckoutPath+sess.ExternalID {
		t.Fatalf("session = %+v", sess)
	}
	if st, _ := m.QueryStatus(context.Background(), sess.ExternalID); st != StatusPending {
		t.Errorf("QueryStatus after create = %q, want pending", st)
	}

	h, body, err := m.SignedCallback(sess.ExternalID, StatusPaid, 100)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := m.ParseCallback(h, body)
	if err != nil {
		t.Fatalf("ParseCallback: %v", err)
	}
	if ev.ExternalID != sess.ExternalID || ev.Status != StatusPaid || ev.Amount != 100 {
		t.Errorf("event = %+v", ev)
	}
	if st, _ := m.QueryStatus(context.Background(), sess.ExternalID); st != StatusPaid {
		t.Errorf("QueryStatus after callback = %q, want paid", st)
	}
}

func TestMockProviderRejectsBadTokenAndBody(t *testing.T) {
	m := NewMockProvider("", "tok")
	_, body, _ := m.SignedCallback("mock_x", StatusPaid, 1)

	for name, h := range map[string]http.Header{
		"missing": {},
		"wrong":   {WebhookTokenHeader: []string{"nope"}},
	} {
		_, err := m.ParseCallback(h, body)
		if code := appCode(err); code != "invalid_webhook_token" {
			t.Errorf("%s token: code = %q, want invalid_webhook_token", name, code)
		}
	}

	good := http.Header{WebhookTokenHeader: []string{"tok"}}
	for _, b := range []string{`not json`, `{"external_id":"x","status":"bogus"}`, `{"status":"paid"}`} {
		if _, err := m.ParseCallback(good, []byte(b)); appCode(err) != "invalid_callback" {
			t.Errorf("body %s: err = %v, want invalid_callback", b, err)
		}
	}
}

func TestMockProviderEmptyTokenIsRandomNotEmpty(t *testing.T) {
	m := NewMockProvider("", "")
	// An empty header must never match an empty configured token.
	if _, err := m.ParseCallback(http.Header{}, []byte(`{}`)); appCode(err) != "invalid_webhook_token" {
		t.Fatalf("err = %v, want invalid_webhook_token", err)
	}
}

func TestBakaiStubNotConfigured(t *testing.T) {
	b := NewBakaiProvider("tok")
	if !errors.Is(b.Ready(), ErrNotConfigured) {
		t.Error("Ready should be ErrNotConfigured")
	}
	if _, err := b.CreatePayment(context.Background(), CreateRequest{}); !errors.Is(err, ErrNotConfigured) {
		t.Error("CreatePayment should be ErrNotConfigured")
	}
	if _, err := b.ParseCallback(http.Header{}, nil); appCode(err) != "invalid_webhook_token" {
		t.Errorf("unauthenticated callback err = %v", err)
	}
	if _, err := b.ParseCallback(http.Header{WebhookTokenHeader: []string{"tok"}}, nil); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("authenticated callback err = %v", err)
	}
}

// --- Service.HandleCallback against sqlmock ---

func newMockService(t *testing.T, p Provider, creator OnlineOrderCreator) (*Service, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewService(db, p, creator, "https://cozy.test"), mock
}

// orderLockRows is the orders row orders.CancelUnpaidOrderTx locks.
func orderLockRows(status orders.OrderStatus, payment Status) *sqlmock.Rows {
	now := time.Now()
	return sqlmock.NewRows([]string{"id", "order_number", "customer_id", "address_id", "point_id", "status", "payment_method",
		"payment_status", "total_amount", "delivery_fee", "refund_required", "comment", "created_at", "updated_at"}).
		AddRow("order-1", "COZY-1", "cust-1", nil, "point-1", string(status), "online_card", string(payment), 10.0, 0.0, false, nil, now, now)
}

// expectCancelUnpaid scripts orders.CancelUnpaidOrderTx on a placed order
// with one line (payment already in its final unpaid status).
func expectCancelUnpaid(mock sqlmock.Sqlmock, payment Status, note string) {
	mock.ExpectQuery(regexp.QuoteMeta("FROM orders WHERE id = $1 FOR UPDATE")).
		WithArgs("order-1").WillReturnRows(orderLockRows(orders.StatusPlaced, payment))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT variant_id, quantity FROM order_items")).
		WithArgs("order-1").WillReturnRows(sqlmock.NewRows([]string{"variant_id", "quantity"}).AddRow("var-1", 2))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO stock")).
		WithArgs("var-1", "point-1", 2).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE orders SET status = 'cancelled'")).
		WithArgs("order-1", sqlmock.AnyArg(), false).WillReturnRows(sqlmock.NewRows([]string{"updated_at"}).AddRow(time.Now()))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO order_status_history")).
		WithArgs("order-1", "placed", orders.StatusCancelled, orders.ActorSystem, nil, note).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func expectLockPayment(mock sqlmock.Sqlmock, ext string, status Status, amount float64) {
	mock.ExpectQuery(regexp.QuoteMeta("FROM payments")+".*"+regexp.QuoteMeta("FOR UPDATE")).
		WithArgs(MockProviderName, ext).
		WillReturnRows(sqlmock.NewRows([]string{"id", "order_id", "status", "amount"}).AddRow("pay-1", "order-1", string(status), amount))
}

func TestHandleCallbackPaidUpdatesPaymentAndOrder(t *testing.T) {
	m := NewMockProvider("", "tok")
	creator := &fakeCreator{}
	svc, mock := newMockService(t, m, creator)
	h, body, _ := m.SignedCallback("mock_1", StatusPaid, 4990.5)

	mock.ExpectBegin()
	expectLockPayment(mock, "mock_1", StatusPending, 4990.5)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE payments SET status")).
		WithArgs(StatusPaid, sqlmock.AnyArg(), "pay-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE orders SET payment_status")).
		WithArgs(StatusPaid, "order-1").WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("placed"))
	mock.ExpectCommit()

	res, err := svc.HandleCallback(context.Background(), MockProviderName, h, body)
	if err != nil {
		t.Fatalf("HandleCallback: %v", err)
	}
	if !res.Applied || res.Status != StatusPaid || res.OrderCancelled || res.RefundRequired {
		t.Errorf("result = %+v", res)
	}
	if len(creator.notified) != 1 || creator.notified[0] != "order-1" {
		t.Errorf("staff must be notified once the online order is paid, got %v", creator.notified)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// TestHandleCallbackPaidForCancelledOrderFlagsRefund: the order was
// cancelled while the customer paid — refund_required, no "new order".
func TestHandleCallbackPaidForCancelledOrderFlagsRefund(t *testing.T) {
	m := NewMockProvider("", "tok")
	creator := &fakeCreator{}
	svc, mock := newMockService(t, m, creator)
	h, body, _ := m.SignedCallback("mock_1", StatusPaid, 10)

	mock.ExpectBegin()
	expectLockPayment(mock, "mock_1", StatusPending, 10)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE payments SET status")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE orders SET payment_status")).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("cancelled"))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE orders SET refund_required = true")).
		WithArgs("order-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res, err := svc.HandleCallback(context.Background(), MockProviderName, h, body)
	if err != nil {
		t.Fatalf("HandleCallback: %v", err)
	}
	if !res.RefundRequired {
		t.Errorf("result = %+v, want refund_required", res)
	}
	if len(creator.notified) != 0 {
		t.Error("a cancelled order must not be announced as a new paid order")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// TestHandleCallbackPaidAfterFailedFlagsRefundInDB: "paid" after we
// already failed/cancelled/expired the payment is recorded in the DB
// (refund_required), not just logged.
func TestHandleCallbackPaidAfterFailedFlagsRefundInDB(t *testing.T) {
	for _, current := range []Status{StatusFailed, StatusCancelled} {
		m := NewMockProvider("", "tok")
		creator := &fakeCreator{}
		svc, mock := newMockService(t, m, creator)
		h, body, _ := m.SignedCallback("mock_1", StatusPaid, 10)

		mock.ExpectBegin()
		expectLockPayment(mock, "mock_1", current, 10)
		mock.ExpectExec(regexp.QuoteMeta("UPDATE payments SET raw_webhook")).
			WithArgs(sqlmock.AnyArg(), "pay-1").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("UPDATE orders SET refund_required = true")).
			WithArgs("order-1").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()

		res, err := svc.HandleCallback(context.Background(), MockProviderName, h, body)
		if err != nil {
			t.Fatalf("%s: HandleCallback: %v", current, err)
		}
		if !res.RefundRequired || res.Applied {
			t.Errorf("%s: result = %+v", current, res)
		}
		if len(creator.notified) != 0 {
			t.Errorf("%s: must not notify staff", current)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("%s: %v", current, err)
		}
	}
}

func TestHandleCallbackDuplicateIsNoop(t *testing.T) {
	m := NewMockProvider("", "tok")
	svc, mock := newMockService(t, m, nil)
	h, body, _ := m.SignedCallback("mock_1", StatusPaid, 10)

	mock.ExpectBegin()
	expectLockPayment(mock, "mock_1", StatusPaid, 10)
	mock.ExpectCommit() // no UPDATEs

	res, err := svc.HandleCallback(context.Background(), MockProviderName, h, body)
	if err != nil {
		t.Fatalf("HandleCallback: %v", err)
	}
	if res.Applied {
		t.Errorf("duplicate should not be applied: %+v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestHandleCallbackFailedCancelsOrderAndReturnsStock(t *testing.T) {
	m := NewMockProvider("", "tok")
	svc, mock := newMockService(t, m, nil)
	h, body, _ := m.SignedCallback("mock_1", StatusFailed, 0)

	mock.ExpectBegin()
	expectLockPayment(mock, "mock_1", StatusPending, 10)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE payments SET status")).
		WithArgs(StatusFailed, sqlmock.AnyArg(), "pay-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE orders SET payment_status")).
		WithArgs(StatusFailed, "order-1").WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("placed"))
	expectCancelUnpaid(mock, StatusFailed, "оплата отклонена")
	mock.ExpectCommit()

	res, err := svc.HandleCallback(context.Background(), MockProviderName, h, body)
	if err != nil {
		t.Fatalf("HandleCallback: %v", err)
	}
	if !res.Applied || !res.OrderCancelled || res.Status != StatusFailed {
		t.Errorf("result = %+v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestHandleCallbackAmountMismatchRollsBack(t *testing.T) {
	m := NewMockProvider("", "tok")
	svc, mock := newMockService(t, m, nil)
	h, body, _ := m.SignedCallback("mock_1", StatusPaid, 1)

	mock.ExpectBegin()
	expectLockPayment(mock, "mock_1", StatusPending, 4990.5)
	mock.ExpectRollback()

	_, err := svc.HandleCallback(context.Background(), MockProviderName, h, body)
	if appCode(err) != "amount_mismatch" {
		t.Fatalf("err = %v, want amount_mismatch", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestHandleCallbackUnknownPayment(t *testing.T) {
	m := NewMockProvider("", "tok")
	svc, mock := newMockService(t, m, nil)
	h, body, _ := m.SignedCallback("mock_x", StatusPaid, 1)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("FROM payments")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "order_id", "status", "amount"}))
	mock.ExpectRollback()

	if _, err := svc.HandleCallback(context.Background(), MockProviderName, h, body); appCode(err) != "payment_not_found" {
		t.Fatalf("err = %v, want payment_not_found", err)
	}
}

func TestHandleCallbackWrongProviderNeverTouchesDB(t *testing.T) {
	svc, mock := newMockService(t, NewMockProvider("", "tok"), nil)
	if _, err := svc.HandleCallback(context.Background(), "bakai", http.Header{}, nil); appCode(err) != "payment_provider_not_found" {
		t.Fatalf("err = %v, want payment_provider_not_found", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// --- Service.PlaceOnlineOrder ---

type fakeCreator struct {
	called   bool
	provider string
	in       orders.PlaceOrderInput
	order    *orders.Order
	replay   bool
	notified []string
}

func (f *fakeCreator) CreateOnlineOrder(_ context.Context, in orders.PlaceOrderInput, provider string) (*orders.Order, string, bool, error) {
	f.called = true
	f.provider = provider
	f.in = in
	if f.replay {
		return f.order, "", false, nil
	}
	return f.order, "pay-1", true, nil
}

func (f *fakeCreator) NotifyOrderPaid(_ context.Context, orderID string) error {
	f.notified = append(f.notified, orderID)
	return nil
}

type failingProvider struct{ *MockProvider }

func (failingProvider) CreatePayment(context.Context, CreateRequest) (*Session, error) {
	return nil, errors.New("bank down")
}

func TestPlaceOnlineOrderHappyPath(t *testing.T) {
	creator := &fakeCreator{order: &orders.Order{ID: "order-1", OrderNumber: "COZY-1", TotalAmount: 10}}
	svc, mock := newMockService(t, NewMockProvider("https://cozy.test", "tok"), creator)

	mock.ExpectExec(regexp.QuoteMeta("UPDATE payments SET provider_tx_id = $1, redirect_url = $2")).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "pay-1").WillReturnResult(sqlmock.NewResult(0, 1))

	order, url, created, err := svc.PlaceOnlineOrder(context.Background(), orders.PlaceOrderInput{CustomerID: "cust-1", IdempotencyKey: "k"})
	if err != nil || !created {
		t.Fatalf("PlaceOnlineOrder: %v", err)
	}
	if order.ID != "order-1" || !strings.HasPrefix(url, "https://cozy.test"+MockCheckoutPath+"mock_") {
		t.Errorf("order = %+v, url = %q", order, url)
	}
	if creator.provider != MockProviderName || creator.in.IdempotencyKey != "k" {
		t.Errorf("provider/key passed to CreateOnlineOrder = %q/%q", creator.provider, creator.in.IdempotencyKey)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestPlaceOnlineOrderNotReadyDoesNotCreateOrder(t *testing.T) {
	creator := &fakeCreator{}
	svc, _ := newMockService(t, NewBakaiProvider("tok"), creator)

	_, _, _, err := svc.PlaceOnlineOrder(context.Background(), orders.PlaceOrderInput{CustomerID: "cust-1"})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
	if creator.called {
		t.Fatal("order must not be created (and stock reserved) when the provider isn't ready")
	}
}

func TestPlaceOnlineOrderProviderFailureCancelsOrder(t *testing.T) {
	creator := &fakeCreator{order: &orders.Order{ID: "order-1", OrderNumber: "COZY-1", TotalAmount: 10}}
	svc, mock := newMockService(t, failingProvider{NewMockProvider("", "tok")}, creator)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE payments SET status = 'failed'")).
		WithArgs("pay-1").WillReturnResult(sqlmock.NewResult(0, 1))
	// The idempotency key is released so the client's retry places a
	// fresh order rather than replaying this cancelled one.
	mock.ExpectExec(regexp.QuoteMeta("UPDATE orders SET payment_status = 'failed', idempotency_key = NULL")).
		WithArgs("order-1").WillReturnResult(sqlmock.NewResult(0, 1))
	expectCancelUnpaid(mock, StatusFailed, "не удалось открыть платёж")
	mock.ExpectCommit()

	_, _, _, err := svc.PlaceOnlineOrder(context.Background(), orders.PlaceOrderInput{CustomerID: "cust-1"})
	if appCode(err) != "payment_create_failed" {
		t.Fatalf("err = %v, want payment_create_failed", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("compensation not run: %v", err)
	}
}

// TestPlaceOnlineOrderReplayReturnsPendingURL: an idempotent replay opens
// no new session and hands back the first attempt's payment URL.
func TestPlaceOnlineOrderReplayReturnsPendingURL(t *testing.T) {
	creator := &fakeCreator{order: &orders.Order{ID: "order-1"}, replay: true}
	svc, mock := newMockService(t, failingProvider{NewMockProvider("", "tok")}, creator)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT status, COALESCE(redirect_url, '') FROM payments")).
		WithArgs("order-1").
		WillReturnRows(sqlmock.NewRows([]string{"status", "redirect_url"}).AddRow("pending", "https://pay/1"))

	order, url, created, err := svc.PlaceOnlineOrder(context.Background(), orders.PlaceOrderInput{CustomerID: "cust-1", IdempotencyKey: "k"})
	if err != nil || created || order.ID != "order-1" || url != "https://pay/1" {
		t.Fatalf("replay = %+v %q %v %v", order, url, created, err)
	}

	// Once the payment is no longer pending there is nothing to open.
	mock.ExpectQuery(regexp.QuoteMeta("FROM payments")).
		WillReturnRows(sqlmock.NewRows([]string{"status", "redirect_url"}).AddRow("paid", "https://pay/1"))
	if _, url, _, _ := svc.PlaceOnlineOrder(context.Background(), orders.PlaceOrderInput{CustomerID: "cust-1", IdempotencyKey: "k"}); url != "" {
		t.Errorf("paid replay url = %q, want empty", url)
	}
}

func appCode(err error) string {
	var appErr *apperr.AppError
	if errors.As(err, &appErr) {
		return appErr.Code
	}
	return ""
}
