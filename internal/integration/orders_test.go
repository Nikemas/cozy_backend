//go:build integration

package integration

import (
	"database/sql"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/dbtx"
	"github.com/Nikemas/cozy_backend/internal/orders"
)

// recorder is an orders.Notifier that just counts events.
type recorder struct {
	mu      sync.Mutex
	created []orders.Order
	changed []orders.OrderStatus
}

func (r *recorder) OrderCreated(o orders.Order) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.created = append(r.created, o)
}

func (r *recorder) OrderStatusChanged(o orders.Order, _ orders.OrderStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changed = append(r.changed, o.Status)
}

func newService() (*orders.Service, *recorder) {
	rec := &recorder{}
	return orders.NewService(testDB).WithNotifier(rec), rec
}

// wantAppErr fails unless err is an *apperr.AppError with the given HTTP
// status (the code string is logged, not asserted, so wording changes in
// the orders package don't break these SQL-level tests).
func wantAppErr(t *testing.T, err error, status int) {
	t.Helper()
	var ae *apperr.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("want *apperr.AppError with status %d, got %T: %v", status, err, err)
	}
	if ae.Status != status {
		t.Fatalf("want status %d, got %d (%s: %s)", status, ae.Status, ae.Code, ae.Message)
	}
}

func TestCreatePickupOrder(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	f := newFixture(t, 5, 3)
	svc, rec := newService()

	o, err := svc.CreateOrder(ctx, f.CustomerID, []orders.OrderItemInput{
		{VariantID: f.VariantA, Quantity: 1},
		{VariantID: f.VariantB, Quantity: 1},
		{VariantID: f.VariantA, Quantity: 1}, // duplicate line is merged
	}, nil, strptr(f.PointA))
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	if o.ID == "" || o.OrderNumber == "" {
		t.Fatalf("order not persisted: %+v", o)
	}
	if o.Status != orders.StatusPlaced || o.PaymentMethod != orders.PaymentCashOnDelivery {
		t.Errorf("status/payment = %s/%s, want placed/cash_on_delivery", o.Status, o.PaymentMethod)
	}
	if o.PointID == nil || *o.PointID != f.PointA {
		t.Errorf("point_id = %v, want pickup point %s", o.PointID, f.PointA)
	}
	if o.AddressID != nil {
		t.Errorf("pickup order has address_id %v", *o.AddressID)
	}
	// 2 × 2500 (base price) + 1 × 3000 (price_override). Pickup: no delivery fee.
	if o.TotalAmount != 8000 {
		t.Errorf("total_amount = %v, want 8000", o.TotalAmount)
	}
	if len(o.Items) != 2 {
		t.Fatalf("items = %d, want 2 (duplicate variant lines merged)", len(o.Items))
	}
	if len(rec.created) != 1 {
		t.Errorf("OrderCreated fired %d times, want 1", len(rec.created))
	}

	if got := stockQty(t, f.VariantA, f.PointA); got != 3 {
		t.Errorf("stock A = %d, want 3", got)
	}
	if got := stockQty(t, f.VariantB, f.PointA); got != 2 {
		t.Errorf("stock B = %d, want 2", got)
	}

	// Read paths: by id, by order number, list, and not visible to others.
	for _, key := range []string{o.ID, o.OrderNumber} {
		got, err := svc.GetOrder(ctx, f.CustomerID, key)
		if err != nil {
			t.Fatalf("GetOrder(%s): %v", key, err)
		}
		if got.ID != o.ID || len(got.Items) != 2 || got.TotalAmount != o.TotalAmount {
			t.Errorf("GetOrder(%s) = %+v, want the created order", key, got)
		}
	}
	list, err := svc.ListOrders(ctx, f.CustomerID)
	if err != nil {
		t.Fatalf("ListOrders: %v", err)
	}
	if len(list) != 1 || list[0].ID != o.ID || len(list[0].Items) != 2 {
		t.Errorf("ListOrders = %+v, want exactly the created order with items", list)
	}
	_, err = svc.GetOrder(ctx, newCustomer(t), o.ID)
	wantAppErr(t, err, http.StatusNotFound)

	adm, err := svc.AdminGetOrder(ctx, o.OrderNumber)
	if err != nil {
		t.Fatalf("AdminGetOrder: %v", err)
	}
	if adm.ID != o.ID {
		t.Errorf("AdminGetOrder returned %s, want %s", adm.ID, o.ID)
	}
}

func TestCreateDeliveryOrderPicksStockedPoint(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	f := newFixture(t, 2, 0)
	svc, _ := newService()

	o, err := svc.CreateOrder(ctx, f.CustomerID,
		[]orders.OrderItemInput{{VariantID: f.VariantA, Quantity: 2}}, strptr(f.AddressID), nil)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if o.AddressID == nil || *o.AddressID != f.AddressID {
		t.Errorf("address_id = %v, want %s", o.AddressID, f.AddressID)
	}
	// PointB has no stock rows at all, so PointA must be chosen.
	if o.PointID == nil || *o.PointID != f.PointA {
		t.Errorf("fulfillment point = %v, want %s", o.PointID, f.PointA)
	}
	if got := stockQty(t, f.VariantA, f.PointA); got != 0 {
		t.Errorf("stock A = %d, want 0", got)
	}
}

func TestCreateOrderRejectionsLeaveStockUntouched(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	f := newFixture(t, 1, 1)
	svc, rec := newService()

	// More than in stock.
	_, err := svc.CreateOrder(ctx, f.CustomerID,
		[]orders.OrderItemInput{{VariantID: f.VariantA, Quantity: 2}}, nil, strptr(f.PointA))
	wantAppErr(t, err, http.StatusConflict)

	// Second line fails after the first one was already decremented inside
	// the transaction: the whole order must roll back.
	_, err = svc.CreateOrder(ctx, f.CustomerID, []orders.OrderItemInput{
		{VariantID: f.VariantA, Quantity: 1},
		{VariantID: f.VariantB, Quantity: 5},
	}, nil, strptr(f.PointA))
	wantAppErr(t, err, http.StatusConflict)

	// Someone else's address.
	other := newFixture(t, 0, 0)
	_, err = svc.CreateOrder(ctx, f.CustomerID,
		[]orders.OrderItemInput{{VariantID: f.VariantA, Quantity: 1}}, strptr(other.AddressID), nil)
	if err == nil {
		t.Fatal("order with another customer's address was accepted")
	}

	if got := stockQty(t, f.VariantA, f.PointA); got != 1 {
		t.Errorf("stock A = %d after rejected orders, want 1", got)
	}
	if got := stockQty(t, f.VariantB, f.PointA); got != 1 {
		t.Errorf("stock B = %d after rejected orders, want 1", got)
	}
	list, err := svc.ListOrders(ctx, f.CustomerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 || len(rec.created) != 0 {
		t.Errorf("rejected orders left %d rows / %d notifications", len(list), len(rec.created))
	}
}

// TestConcurrentOrdersDoNotOversell races several customers for the last
// unit: exactly one order may win, stock must end at 0, never negative
// (the CHECK would turn an oversell into a 500 instead of a 409).
func TestConcurrentOrdersDoNotOversell(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	f := newFixture(t, 1, 0)
	svc, _ := newService()

	const n = 6
	customers := make([]string, n)
	for i := range customers {
		customers[i] = newCustomer(t)
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = svc.CreateOrder(ctx, customers[i],
				[]orders.OrderItemInput{{VariantID: f.VariantA, Quantity: 1}}, nil, strptr(f.PointA))
		}(i)
	}
	wg.Wait()

	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
			continue
		}
		wantAppErr(t, err, http.StatusConflict)
	}
	if wins != 1 {
		t.Errorf("%d orders succeeded for 1 unit of stock, want exactly 1", wins)
	}
	if got := stockQty(t, f.VariantA, f.PointA); got != 0 {
		t.Errorf("stock = %d, want 0", got)
	}
}

// TestOnlineOrderCancelReturnsStock covers the online_card path: order +
// pending payments row in one transaction, then the compensating cancel
// (used when the bank reports failure) restores stock exactly once.
func TestOnlineOrderCancelReturnsStock(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	f := newFixture(t, 4, 0)
	svc, _ := newService()

	o, paymentID, err := svc.CreateOnlineOrder(ctx, f.CustomerID,
		[]orders.OrderItemInput{{VariantID: f.VariantA, Quantity: 3}}, nil, strptr(f.PointA), "mock")
	if err != nil {
		t.Fatalf("CreateOnlineOrder: %v", err)
	}
	if o.PaymentMethod != orders.PaymentOnlineCard || o.PaymentStatus == nil || *o.PaymentStatus != orders.PaymentPending {
		t.Errorf("payment method/status = %s/%v, want online_card/pending", o.PaymentMethod, o.PaymentStatus)
	}
	var (
		payStatus, currency string
		amount              float64
	)
	err = testDB.QueryRowContext(ctx,
		`SELECT status, amount, currency FROM payments WHERE id = $1 AND order_id = $2`, paymentID, o.ID,
	).Scan(&payStatus, &amount, &currency)
	if err != nil {
		t.Fatalf("payments row: %v", err)
	}
	if payStatus != "pending" || amount != o.TotalAmount || currency != "KGS" {
		t.Errorf("payment = %s/%v/%s, want pending/%v/KGS", payStatus, amount, currency, o.TotalAmount)
	}
	if got := stockQty(t, f.VariantA, f.PointA); got != 1 {
		t.Fatalf("stock after online order = %d, want 1 (reserved)", got)
	}

	cancel := func() bool {
		var done bool
		err := dbtx.WithTx(ctx, testDB, func(tx *sql.Tx) error {
			var err error
			done, err = orders.CancelUnpaidOrderTx(ctx, tx, o.ID)
			return err
		})
		if err != nil {
			t.Fatalf("CancelUnpaidOrderTx: %v", err)
		}
		return done
	}
	if !cancel() {
		t.Fatal("first cancel reported nothing done")
	}
	if got := stockQty(t, f.VariantA, f.PointA); got != 4 {
		t.Errorf("stock after cancel = %d, want 4", got)
	}
	got, err := svc.GetOrder(ctx, f.CustomerID, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != orders.StatusCancelled {
		t.Errorf("status after cancel = %s, want cancelled", got.Status)
	}

	if cancel() {
		t.Error("second cancel of the same order reported work done")
	}
	if got := stockQty(t, f.VariantA, f.PointA); got != 4 {
		t.Errorf("stock after repeated cancel = %d, want 4 (restocked once)", got)
	}
}

// TestAdminStatusFlow checks the admin status UPDATE path. Whether an
// admin cancel restocks is a business rule being changed separately, so
// only the status column and transition validation are asserted here.
func TestAdminStatusFlow(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	f := newFixture(t, 2, 0)
	svc, rec := newService()

	o, err := svc.CreateOrder(ctx, f.CustomerID,
		[]orders.OrderItemInput{{VariantID: f.VariantA, Quantity: 1}}, nil, strptr(f.PointA))
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	confirmed, err := svc.AdminUpdateStatus(ctx, o.ID, orders.StatusConfirmed)
	if err != nil {
		t.Fatalf("placed -> confirmed: %v", err)
	}
	if confirmed.Status != orders.StatusConfirmed || len(confirmed.Items) != 1 {
		t.Errorf("after confirm: status %s, %d items", confirmed.Status, len(confirmed.Items))
	}

	cancelled, err := svc.AdminUpdateStatus(ctx, o.OrderNumber, orders.StatusCancelled)
	if err != nil {
		t.Fatalf("confirmed -> cancelled: %v", err)
	}
	if cancelled.Status != orders.StatusCancelled {
		t.Errorf("after cancel: status %s", cancelled.Status)
	}

	_, err = svc.AdminUpdateStatus(ctx, o.ID, orders.StatusConfirmed)
	if err == nil {
		t.Error("cancelled -> confirmed was allowed")
	}
	if len(rec.changed) != 2 {
		t.Errorf("OrderStatusChanged fired %d times, want 2", len(rec.changed))
	}

	var dbStatus string
	if err := testDB.QueryRowContext(ctx, `SELECT status FROM orders WHERE id = $1`, o.ID).Scan(&dbStatus); err != nil {
		t.Fatal(err)
	}
	if dbStatus != "cancelled" {
		t.Errorf("orders.status = %s, want cancelled", dbStatus)
	}
}
