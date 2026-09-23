package orders

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

type recordingNotifier struct {
	created []Order
	changed []Order
	from    []OrderStatus
	panics  bool
}

func (r *recordingNotifier) OrderCreated(o Order) {
	if r.panics {
		panic("boom")
	}
	r.created = append(r.created, o)
}

func (r *recordingNotifier) OrderStatusChanged(o Order, from OrderStatus) {
	if r.panics {
		panic("boom")
	}
	r.changed = append(r.changed, o)
	r.from = append(r.from, from)
}

func expectPickupOrderCreation(mock sqlmock.Sqlmock, commitErr error) {
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT is_active FROM points_of_sale")).
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta("FROM product_variants pv")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "size", "color", "price_override", "name_ru", "base_price"}).
			AddRow("var-1", "42", "Черный", nil, "Air Max", 5000.0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT quantity FROM stock")).
		WillReturnRows(sqlmock.NewRows([]string{"quantity"}).AddRow(10))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE stock SET quantity")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("pg_advisory_xact_lock")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM orders")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO orders")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow("order-1", time.Now(), time.Now()))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO order_items")).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("item-1"))
	if commitErr != nil {
		mock.ExpectCommit().WillReturnError(commitErr)
	} else {
		mock.ExpectCommit()
	}
}

func TestCreateOrderNotifiesAfterCommit(t *testing.T) {
	svc, mock := newMockService(t)
	rec := &recordingNotifier{}
	svc.WithNotifier(rec)
	expectPickupOrderCreation(mock, nil)

	pickupID := "point-1"
	if _, err := svc.CreateOrder(context.Background(), "cust-1",
		[]OrderItemInput{{VariantID: "var-1", Quantity: 2}}, nil, &pickupID); err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if len(rec.created) != 1 || rec.created[0].ID != "order-1" || len(rec.created[0].Items) != 1 {
		t.Fatalf("notifier got %+v, want one OrderCreated for order-1 with its item", rec.created)
	}
}

func TestCreateOrderDoesNotNotifyWhenCommitFails(t *testing.T) {
	svc, mock := newMockService(t)
	rec := &recordingNotifier{}
	svc.WithNotifier(rec)
	expectPickupOrderCreation(mock, errors.New("commit failed"))

	pickupID := "point-1"
	if _, err := svc.CreateOrder(context.Background(), "cust-1",
		[]OrderItemInput{{VariantID: "var-1", Quantity: 2}}, nil, &pickupID); err == nil {
		t.Fatal("want commit error")
	}
	if len(rec.created) != 0 {
		t.Fatalf("notified about an order that never committed: %+v", rec.created)
	}
}

func TestCreateOrderSurvivesPanickingNotifier(t *testing.T) {
	svc, mock := newMockService(t)
	svc.WithNotifier(&recordingNotifier{panics: true})
	expectPickupOrderCreation(mock, nil)

	pickupID := "point-1"
	if _, err := svc.CreateOrder(context.Background(), "cust-1",
		[]OrderItemInput{{VariantID: "var-1", Quantity: 2}}, nil, &pickupID); err != nil {
		t.Fatalf("CreateOrder must succeed even if the notifier panics: %v", err)
	}
}

var orderColumns = []string{"id", "order_number", "customer_id", "address_id", "point_id", "status", "payment_method", "total_amount", "comment", "created_at", "updated_at"}

func TestAdminUpdateStatusNotifiesWithPreviousStatus(t *testing.T) {
	svc, mock := newMockService(t)
	rec := &recordingNotifier{}
	svc.WithNotifier(rec)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("FOR UPDATE")).WithArgs("COZY-20260923-001").
		WillReturnRows(sqlmock.NewRows(orderColumns).AddRow("order-1", "COZY-20260923-001", "cust-1", "addr-1", "point-1",
			"placed", "cash_on_delivery", 5000.0, nil, time.Now(), time.Now()))
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE orders SET status")).
		WillReturnRows(sqlmock.NewRows([]string{"updated_at"}).AddRow(time.Now()))
	mock.ExpectCommit()
	mock.ExpectQuery(regexp.QuoteMeta("FROM order_items")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "order_id", "variant_id", "product_name_snapshot", "size_snapshot", "color_snapshot", "quantity", "price"}))

	o, err := svc.AdminUpdateStatus(context.Background(), "COZY-20260923-001", StatusConfirmed)
	if err != nil {
		t.Fatalf("AdminUpdateStatus: %v", err)
	}
	if o.Status != StatusConfirmed {
		t.Errorf("status = %q", o.Status)
	}
	if len(rec.changed) != 1 || rec.changed[0].Status != StatusConfirmed || rec.from[0] != StatusPlaced {
		t.Fatalf("notifier got changed=%+v from=%v, want one placed→confirmed", rec.changed, rec.from)
	}
}

func TestAdminUpdateStatusInvalidTransitionDoesNotNotify(t *testing.T) {
	svc, mock := newMockService(t)
	rec := &recordingNotifier{}
	svc.WithNotifier(rec)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows(orderColumns).AddRow("order-1", "COZY-20260923-001", "cust-1", "addr-1", "point-1",
			"delivered", "cash_on_delivery", 5000.0, nil, time.Now(), time.Now()))
	mock.ExpectRollback()

	if _, err := svc.AdminUpdateStatus(context.Background(), "order-1", StatusCancelled); err == nil {
		t.Fatal("want invalid_status_transition")
	}
	if len(rec.changed) != 0 {
		t.Fatalf("notified about a rejected transition: %+v", rec.changed)
	}
}

func TestDefaultNotifierIsUsedWhenServiceHasNone(t *testing.T) {
	rec := &recordingNotifier{}
	SetDefaultNotifier(rec)
	t.Cleanup(func() { SetDefaultNotifier(nil) })

	svc := NewService(nil)
	svc.notifyCreated(Order{OrderNumber: "X"})
	if len(rec.created) != 1 {
		t.Fatal("default notifier not used")
	}

	own := &recordingNotifier{}
	svc.WithNotifier(own)
	svc.notifyCreated(Order{OrderNumber: "Y"})
	if len(own.created) != 1 || len(rec.created) != 1 {
		t.Fatal("service-specific notifier must override the default")
	}
}
