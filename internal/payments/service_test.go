package payments

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"

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

func expectLockPayment(mock sqlmock.Sqlmock, ext string, status Status, amount float64) {
	mock.ExpectQuery(regexp.QuoteMeta("FROM payments")+".*"+regexp.QuoteMeta("FOR UPDATE")).
		WithArgs(MockProviderName, ext).
		WillReturnRows(sqlmock.NewRows([]string{"id", "order_id", "status", "amount"}).AddRow("pay-1", "order-1", string(status), amount))
}

func TestHandleCallbackPaidUpdatesPaymentAndOrder(t *testing.T) {
	m := NewMockProvider("", "tok")
	svc, mock := newMockService(t, m, nil)
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
	if !res.Applied || res.Status != StatusPaid || res.OrderCancelled {
		t.Errorf("result = %+v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
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
	// orders.CancelUnpaidOrderTx
	mock.ExpectQuery(regexp.QuoteMeta("SELECT status, point_id FROM orders WHERE id = $1 FOR UPDATE")).
		WithArgs("order-1").WillReturnRows(sqlmock.NewRows([]string{"status", "point_id"}).AddRow("placed", "point-1"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT variant_id, quantity FROM order_items")).
		WithArgs("order-1").WillReturnRows(sqlmock.NewRows([]string{"variant_id", "quantity"}).AddRow("var-1", 2))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO stock")).
		WithArgs("var-1", "point-1", 2).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE orders SET status = 'cancelled'")).
		WithArgs("order-1").WillReturnResult(sqlmock.NewResult(0, 1))
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
	order    *orders.Order
}

func (f *fakeCreator) CreateOnlineOrder(_ context.Context, _ string, _ []orders.OrderItemInput, _, _ *string, provider string) (*orders.Order, string, error) {
	f.called = true
	f.provider = provider
	return f.order, "pay-1", nil
}

type failingProvider struct{ *MockProvider }

func (failingProvider) CreatePayment(context.Context, CreateRequest) (*Session, error) {
	return nil, errors.New("bank down")
}

func TestPlaceOnlineOrderHappyPath(t *testing.T) {
	creator := &fakeCreator{order: &orders.Order{ID: "order-1", OrderNumber: "COZY-1", TotalAmount: 10}}
	svc, mock := newMockService(t, NewMockProvider("https://cozy.test", "tok"), creator)

	mock.ExpectExec(regexp.QuoteMeta("UPDATE payments SET provider_tx_id")).
		WithArgs(sqlmock.AnyArg(), "pay-1").WillReturnResult(sqlmock.NewResult(0, 1))

	order, url, err := svc.PlaceOnlineOrder(context.Background(), "cust-1", nil, nil, nil)
	if err != nil {
		t.Fatalf("PlaceOnlineOrder: %v", err)
	}
	if order.ID != "order-1" || !strings.HasPrefix(url, "https://cozy.test"+MockCheckoutPath+"mock_") {
		t.Errorf("order = %+v, url = %q", order, url)
	}
	if creator.provider != MockProviderName {
		t.Errorf("provider passed to CreateOnlineOrder = %q", creator.provider)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestPlaceOnlineOrderNotReadyDoesNotCreateOrder(t *testing.T) {
	creator := &fakeCreator{}
	svc, _ := newMockService(t, NewBakaiProvider("tok"), creator)

	_, _, err := svc.PlaceOnlineOrder(context.Background(), "cust-1", nil, nil, nil)
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
	mock.ExpectExec(regexp.QuoteMeta("UPDATE orders SET payment_status = 'failed'")).
		WithArgs("order-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT status, point_id FROM orders")).
		WithArgs("order-1").WillReturnRows(sqlmock.NewRows([]string{"status", "point_id"}).AddRow("placed", "point-1"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT variant_id, quantity FROM order_items")).
		WillReturnRows(sqlmock.NewRows([]string{"variant_id", "quantity"}).AddRow("var-1", 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO stock")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE orders SET status = 'cancelled'")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	_, _, err := svc.PlaceOnlineOrder(context.Background(), "cust-1", nil, nil, nil)
	if appCode(err) != "payment_create_failed" {
		t.Fatalf("err = %v, want payment_create_failed", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("compensation not run: %v", err)
	}
}

func appCode(err error) string {
	var appErr *apperr.AppError
	if errors.As(err, &appErr) {
		return appErr.Code
	}
	return ""
}
