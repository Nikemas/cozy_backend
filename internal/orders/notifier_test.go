package orders

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/staff"
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
	expectOrderPrologue(mock, 0)
	expectPickupLine(mock, testVar1, 5000, 10, 2)
	expectOrderInsert(mock, nil)
	if commitErr != nil {
		mock.ExpectCommit().WillReturnError(commitErr)
	} else {
		mock.ExpectCommit()
	}
}

func newNotifyingService(t *testing.T, n Notifier) (*Service, sqlmock.Sqlmock) {
	svc, mock := newMockService(t)
	svc.WithSettings(testSettings()).WithNotifier(n)
	return svc, mock
}

func TestCreateOrderNotifiesAfterCommit(t *testing.T) {
	rec := &recordingNotifier{}
	svc, mock := newNotifyingService(t, rec)
	expectPickupOrderCreation(mock, nil)

	pickupID := "point-1"
	if _, err := svc.CreateOrder(context.Background(), "cust-1",
		[]OrderItemInput{{VariantID: testVar1, Quantity: 2}}, nil, &pickupID); err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if len(rec.created) != 1 || rec.created[0].ID != "order-1" || len(rec.created[0].Items) != 1 {
		t.Fatalf("notifier got %+v, want one OrderCreated for order-1 with its item", rec.created)
	}
}

func TestCreateOrderDoesNotNotifyWhenCommitFails(t *testing.T) {
	rec := &recordingNotifier{}
	svc, mock := newNotifyingService(t, rec)
	expectPickupOrderCreation(mock, errors.New("commit failed"))

	pickupID := "point-1"
	if _, err := svc.CreateOrder(context.Background(), "cust-1",
		[]OrderItemInput{{VariantID: testVar1, Quantity: 2}}, nil, &pickupID); err == nil {
		t.Fatal("want commit error")
	}
	if len(rec.created) != 0 {
		t.Fatalf("notified about an order that never committed: %+v", rec.created)
	}
}

func TestCreateOrderSurvivesPanickingNotifier(t *testing.T) {
	svc, mock := newNotifyingService(t, &recordingNotifier{panics: true})
	expectPickupOrderCreation(mock, nil)

	pickupID := "point-1"
	if _, err := svc.CreateOrder(context.Background(), "cust-1",
		[]OrderItemInput{{VariantID: testVar1, Quantity: 2}}, nil, &pickupID); err != nil {
		t.Fatalf("CreateOrder must succeed even if the notifier panics: %v", err)
	}
}

func TestAdminUpdateStatusNotifiesWithPreviousStatus(t *testing.T) {
	rec := &recordingNotifier{}
	svc, mock := newNotifyingService(t, rec)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("FOR UPDATE")).WithArgs("COZY-20260923-001").
		WillReturnRows(codOrder(StatusPlaced).rows())
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE orders SET status")).
		WillReturnRows(sqlmock.NewRows([]string{"updated_at"}).AddRow(time.Now()))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO order_status_history")).
		WithArgs("order-1", "placed", StatusConfirmed, ActorStaff, "staff-1", nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	expectNoItems(mock)

	o, err := svc.AdminUpdateStatus(staffCtx(staff.RoleManager), "COZY-20260923-001", StatusConfirmed)
	if err != nil {
		t.Fatalf("AdminUpdateStatus: %v", err)
	}
	if o.Status != StatusConfirmed {
		t.Errorf("status = %q", o.Status)
	}
	if len(rec.changed) != 1 || rec.changed[0].Status != StatusConfirmed || rec.from[0] != StatusPlaced {
		t.Fatalf("notifier got changed=%+v from=%v, want one placed→confirmed", rec.changed, rec.from)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestAdminUpdateStatusInvalidTransitionDoesNotNotify(t *testing.T) {
	rec := &recordingNotifier{}
	svc, mock := newNotifyingService(t, rec)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("FOR UPDATE")).
		WillReturnRows(codOrder(StatusDelivered).rows())
	mock.ExpectRollback()

	if _, err := svc.AdminUpdateStatus(staffCtx(staff.RoleOwner), "order-1", StatusCancelled); err == nil {
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
