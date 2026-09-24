//go:build integration

package integration

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/payments"
)

func floatPtr(f float64) *float64 { return &f }

// deactivateZonesOnCleanup switches every zone off when the test ends, so
// parallel tests (which run after this sequential one) see no active zone
// and keep the flat delivery fee.
func deactivateZonesOnCleanup(t *testing.T) {
	t.Cleanup(func() {
		if _, err := testDB.Exec(`UPDATE delivery_zones SET is_active = false`); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
}

// TestDeliveryZoneFee: with an active zone a delivery order must name
// one; its fee is the zone's, 0 from free_from; the zone travels with the
// order. Deliberately NOT parallel: an active zone changes every delivery
// order in the database.
func TestDeliveryZoneFee(t *testing.T) {
	ctx := ctxT(t)
	deactivateZonesOnCleanup(t)
	f := newFixture(t, 10, 10)
	svc, _ := newService()
	svc.WithSettings(orders.Settings{DeliveryFee: 200})
	zones := orders.NewDeliveryZoneRepo(testDB)

	city, err := zones.Create(ctx, orders.DeliveryZoneInput{NameRu: "Бишкек", NameKy: "Бишкек ш.", Fee: 300, FreeFrom: floatPtr(5000), IsActive: true})
	if err != nil {
		t.Fatalf("Create zone: %v", err)
	}
	far, err := zones.Create(ctx, orders.DeliveryZoneInput{NameRu: "Пригород", NameKy: "Чет", Fee: 600, IsActive: true, SortOrder: 1})
	if err != nil {
		t.Fatal(err)
	}
	active, err := zones.ListActive(ctx)
	if err != nil || len(active) != 2 || active[0].ID != city.ID || active[0].FreeFrom == nil || *active[0].FreeFrom != 5000 {
		t.Fatalf("ListActive = %+v, %v", active, err)
	}

	place := func(variant string, qty int, zoneID *string) (*orders.Order, error) {
		o, _, err := svc.PlaceOrder(ctx, orders.PlaceOrderInput{
			CustomerID: f.CustomerID, Items: []orders.OrderItemInput{{VariantID: variant, Quantity: qty}},
			AddressID: strptr(f.AddressID), DeliveryZoneID: zoneID,
		})
		return o, err
	}

	if _, err := place(f.VariantA, 1, nil); !errors.Is(err, orders.ErrDeliveryZoneRequired) {
		t.Fatalf("no zone while zones are active: err = %v, want delivery_zone_required", err)
	}
	if got := stockQty(t, f.VariantA, f.PointA); got != 10 {
		t.Fatalf("a rejected order reserved stock: %d", got)
	}

	// 2500 < 5000: the zone's fee.
	o, err := place(f.VariantA, 1, &city.ID)
	if err != nil {
		t.Fatalf("zoned order: %v", err)
	}
	if o.DeliveryFee != 300 || o.TotalAmount != 2800 {
		t.Errorf("fee/total = %v/%v, want 300/2800", o.DeliveryFee, o.TotalAmount)
	}
	got, err := svc.GetOrder(ctx, f.CustomerID, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeliveryZone == nil || got.DeliveryZone.ID != city.ID || got.DeliveryZone.NameKy != "Бишкек ш." || got.DeliveryFee != 300 {
		t.Errorf("stored order zone/fee = %+v/%v", got.DeliveryZone, got.DeliveryFee)
	}

	// 2 × 3000 = 6000 ≥ 5000: free.
	o, err = place(f.VariantB, 2, &city.ID)
	if err != nil {
		t.Fatal(err)
	}
	if o.DeliveryFee != 0 || o.TotalAmount != 6000 {
		t.Errorf("free_from: fee/total = %v/%v, want 0/6000", o.DeliveryFee, o.TotalAmount)
	}

	// A zone without free_from charges whatever the total.
	o, err = place(f.VariantB, 2, &far.ID)
	if err != nil {
		t.Fatal(err)
	}
	if o.DeliveryFee != 600 || o.TotalAmount != 6600 {
		t.Errorf("far zone: fee/total = %v/%v, want 600/6600", o.DeliveryFee, o.TotalAmount)
	}

	// Pickup ignores a zone.
	o, _, err = svc.PlaceOrder(ctx, orders.PlaceOrderInput{
		CustomerID: f.CustomerID, Items: []orders.OrderItemInput{{VariantID: f.VariantA, Quantity: 1}},
		PickupPointID: strptr(f.PointA), DeliveryZoneID: &city.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if o.DeliveryFee != 0 || o.DeliveryZone != nil {
		t.Errorf("pickup: fee/zone = %v/%+v", o.DeliveryFee, o.DeliveryZone)
	}

	// A used zone can't be deleted, only deactivated; a deactivated zone
	// can't be ordered to.
	var appErr *apperr.AppError
	if err := zones.Delete(ctx, city.ID); !errors.As(err, &appErr) || appErr.Code != "delivery_zone_in_use" {
		t.Fatalf("Delete used zone: err = %v, want delivery_zone_in_use", err)
	}
	if err := zones.SetActive(ctx, far.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := place(f.VariantA, 1, &far.ID); !errors.Is(err, orders.ErrInvalidDeliveryZone) {
		t.Errorf("inactive zone: err = %v, want invalid_delivery_zone", err)
	}

	// No active zone left → the flat fee again, no zone needed.
	if err := zones.SetActive(ctx, city.ID, false); err != nil {
		t.Fatal(err)
	}
	o, err = place(f.VariantA, 1, nil)
	if err != nil {
		t.Fatalf("flat fee order: %v", err)
	}
	if o.DeliveryFee != 200 || o.DeliveryZone != nil {
		t.Errorf("flat: fee/zone = %v/%+v, want 200/nil", o.DeliveryFee, o.DeliveryZone)
	}

	// An unused zone can be deleted.
	spare, err := zones.Create(ctx, orders.DeliveryZoneInput{NameRu: "Запас", NameKy: "Запас", Fee: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := zones.Delete(ctx, spare.ID); err != nil {
		t.Errorf("Delete unused zone: %v", err)
	}
}

// payTestService is a payments.Service on the mock provider.
func payTestService(svc *orders.Service) (*payments.Service, *payments.MockProvider) {
	mock := payments.NewMockProvider("https://cozy.test", "tok")
	return payments.NewService(testDB, mock, svc, "https://cozy.test"), mock
}

func externalID(t *testing.T, paymentURL string) string {
	t.Helper()
	i := strings.LastIndex(paymentURL, "/")
	if i < 0 || !strings.HasPrefix(paymentURL[i+1:], "mock_") {
		t.Fatalf("unexpected payment_url %q", paymentURL)
	}
	return paymentURL[i+1:]
}

func callback(t *testing.T, ctx context.Context, pay *payments.Service, mock *payments.MockProvider, ext string, st payments.Status, amount float64) *payments.CallbackResult {
	t.Helper()
	h, body, err := mock.SignedCallback(ext, st, amount)
	if err != nil {
		t.Fatal(err)
	}
	res, err := pay.HandleCallback(ctx, payments.MockProviderName, h, body)
	if err != nil {
		t.Fatalf("HandleCallback(%s): %v", st, err)
	}
	return res
}

func paymentStatuses(t *testing.T, orderID string) []string {
	t.Helper()
	rows, err := testDB.Query(`SELECT status FROM payments WHERE order_id = $1 ORDER BY created_at, id`, orderID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// TestPaymentRetry: a declined payment keeps the order open, POST
// /orders/{id}/pay opens a new attempt (closing the pending one), a late
// success of an earlier attempt is honoured, and a paid order can't be
// retried.
func TestPaymentRetry(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	f := newFixture(t, 5, 0)
	svc, _ := newService()
	pay, mock := payTestService(svc)

	o, url1, created, err := pay.PlaceOnlineOrder(ctx, orders.PlaceOrderInput{
		CustomerID: f.CustomerID, Items: []orders.OrderItemInput{{VariantID: f.VariantA, Quantity: 2}},
		PickupPointID: strptr(f.PointA),
	})
	if err != nil || !created {
		t.Fatalf("PlaceOnlineOrder: %v", err)
	}
	if got := stockQty(t, f.VariantA, f.PointA); got != 3 {
		t.Fatalf("stock after order = %d, want 3", got)
	}

	// Retry while pending: new session, the old attempt closed.
	url2, err := pay.RetryPayment(ctx, f.CustomerID, o.ID)
	if err != nil {
		t.Fatalf("RetryPayment (pending): %v", err)
	}
	if url2 == url1 {
		t.Fatal("retry must open a new session")
	}
	if st := paymentStatuses(t, o.ID); len(st) != 2 || st[0] != "cancelled" || st[1] != "pending" {
		t.Fatalf("attempts = %v, want [cancelled pending]", st)
	}

	// Declined: order stays placed with its stock, payment_status failed.
	res := callback(t, ctx, pay, mock, externalID(t, url2), payments.StatusFailed, 0)
	if !res.Applied || res.OrderCancelled {
		t.Fatalf("failed callback = %+v", res)
	}
	got, _ := svc.GetOrder(ctx, f.CustomerID, o.ID)
	if got.Status != orders.StatusPlaced || got.PaymentStatus == nil || *got.PaymentStatus != orders.PaymentFailed {
		t.Fatalf("after decline: %s/%v, want placed/failed", got.Status, got.PaymentStatus)
	}
	if n := stockQty(t, f.VariantA, f.PointA); n != 3 {
		t.Fatalf("a declined payment must keep the reservation: stock = %d", n)
	}

	// Someone else's order: not found.
	var appErr *apperr.AppError
	if _, err := pay.RetryPayment(ctx, newCustomer(t), o.ID); !errors.As(err, &appErr) || appErr.Status != http.StatusNotFound {
		t.Fatalf("foreign retry: err = %v, want 404", err)
	}

	// Retry after decline (by order number, like the site's links).
	url3, err := pay.RetryPayment(ctx, f.CustomerID, o.OrderNumber)
	if err != nil {
		t.Fatalf("RetryPayment (failed): %v", err)
	}
	got, _ = svc.GetOrder(ctx, f.CustomerID, o.ID)
	if *got.PaymentStatus != orders.PaymentPending {
		t.Fatalf("after retry payment_status = %s, want pending", *got.PaymentStatus)
	}

	// The customer actually completed the FIRST (closed) attempt: accepted,
	// the open third attempt closed, no refund.
	res = callback(t, ctx, pay, mock, externalID(t, url1), payments.StatusPaid, o.TotalAmount)
	if !res.Applied || res.RefundRequired || res.Status != payments.StatusPaid {
		t.Fatalf("late paid = %+v", res)
	}
	got, _ = svc.GetOrder(ctx, f.CustomerID, o.ID)
	if *got.PaymentStatus != orders.PaymentPaid || got.RefundRequired || got.Status != orders.StatusPlaced {
		t.Fatalf("after late paid: %s/%s refund=%v", got.Status, *got.PaymentStatus, got.RefundRequired)
	}
	if st := paymentStatuses(t, o.ID); len(st) != 3 || st[0] != "paid" || st[1] != "failed" || st[2] != "cancelled" {
		t.Fatalf("attempts = %v, want [paid failed cancelled]", st)
	}

	// Paid: no more retries.
	if _, err := pay.RetryPayment(ctx, f.CustomerID, o.ID); !errors.As(err, &appErr) || appErr.Code != "payment_not_retryable" || appErr.Status != http.StatusConflict {
		t.Fatalf("retry of a paid order: err = %v, want 409 payment_not_retryable", err)
	}

	// Money for the closed third attempt too: a double payment → refund.
	res = callback(t, ctx, pay, mock, externalID(t, url3), payments.StatusPaid, o.TotalAmount)
	if !res.RefundRequired {
		t.Fatalf("second payment of a paid order = %+v, want refund_required", res)
	}
	if n := stockQty(t, f.VariantA, f.PointA); n != 3 {
		t.Errorf("stock = %d, want 3 (still reserved for the paid order)", n)
	}
}

// TestPaymentRetryNotRetryable: cash orders and cancelled online orders.
func TestPaymentRetryNotRetryable(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	f := newFixture(t, 5, 0)
	svc, _ := newService()
	pay, mock := payTestService(svc)

	cash, _, err := svc.PlaceOrder(ctx, orders.PlaceOrderInput{
		CustomerID: f.CustomerID, Items: []orders.OrderItemInput{{VariantID: f.VariantA, Quantity: 1}}, PickupPointID: strptr(f.PointA),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pay.RetryPayment(ctx, f.CustomerID, cash.ID); !errors.Is(err, orders.ErrPaymentNotRetryable) {
		t.Errorf("cash order: err = %v", err)
	}

	online, url, _, err := pay.PlaceOnlineOrder(ctx, orders.PlaceOrderInput{
		CustomerID: f.CustomerID, Items: []orders.OrderItemInput{{VariantID: f.VariantA, Quantity: 1}}, PickupPointID: strptr(f.PointA),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Cancelled on the bank page → order cancelled, stock back, no retry.
	res := callback(t, ctx, pay, mock, externalID(t, url), payments.StatusCancelled, 0)
	if !res.OrderCancelled {
		t.Fatalf("cancelled callback = %+v", res)
	}
	if _, err := pay.RetryPayment(ctx, f.CustomerID, online.ID); !errors.Is(err, orders.ErrPaymentNotRetryable) {
		t.Errorf("cancelled order: err = %v", err)
	}
	if n := stockQty(t, f.VariantA, f.PointA); n != 4 {
		t.Errorf("stock = %d, want 4 (cash order only)", n)
	}
}

// TestPaymentExpiryUsesLatestAttempt: the expiry clock restarts with each
// attempt; an abandoned pending attempt, or a decline never retried,
// cancels the order and returns the stock. Not parallel: ExpirePending
// scans every order, and this test backdates rows.
func TestPaymentExpiryUsesLatestAttempt(t *testing.T) {
	ctx := ctxT(t)
	f := newFixture(t, 5, 0)
	svc, _ := newService()
	pay, mock := payTestService(svc)
	const ttl = 30 * time.Minute

	backdate := func(q, orderID string) {
		t.Helper()
		if _, err := testDB.ExecContext(ctx, q, orderID); err != nil {
			t.Fatal(err)
		}
	}
	placeOnline := func() (*orders.Order, string) {
		o, url, _, err := pay.PlaceOnlineOrder(ctx, orders.PlaceOrderInput{
			CustomerID: f.CustomerID, Items: []orders.OrderItemInput{{VariantID: f.VariantA, Quantity: 1}}, PickupPointID: strptr(f.PointA),
		})
		if err != nil {
			t.Fatal(err)
		}
		return o, url
	}
	status := func(id string) orders.OrderStatus {
		o, err := svc.GetOrder(ctx, f.CustomerID, id)
		if err != nil {
			t.Fatal(err)
		}
		return o.Status
	}

	// 1. Old first attempt, fresh retry → kept.
	retried, _ := placeOnline()
	backdate(`UPDATE payments SET created_at = now() - interval '1 hour', updated_at = now() - interval '1 hour' WHERE order_id = $1`, retried.ID)
	if _, err := pay.RetryPayment(ctx, f.CustomerID, retried.ID); err != nil {
		t.Fatal(err)
	}
	// 2. Abandoned pending attempt → expired.
	abandoned, _ := placeOnline()
	backdate(`UPDATE payments SET created_at = now() - interval '1 hour' WHERE order_id = $1`, abandoned.ID)
	// 3. Declined long ago, never retried → expired; declined just now → kept.
	oldDecline, oldURL := placeOnline()
	callback(t, ctx, pay, mock, externalID(t, oldURL), payments.StatusFailed, 0)
	backdate(`UPDATE payments SET created_at = now() - interval '2 hour', updated_at = now() - interval '1 hour' WHERE order_id = $1`, oldDecline.ID)
	freshDecline, freshURL := placeOnline()
	callback(t, ctx, pay, mock, externalID(t, freshURL), payments.StatusFailed, 0)
	backdate(`UPDATE payments SET created_at = now() - interval '2 hour' WHERE order_id = $1`, freshDecline.ID)

	if n := stockQty(t, f.VariantA, f.PointA); n != 1 {
		t.Fatalf("stock before expiry = %d, want 1", n)
	}
	n, err := pay.ExpirePending(ctx, ttl)
	if err != nil {
		t.Fatalf("ExpirePending: %v", err)
	}
	if n != 2 {
		t.Errorf("expired = %d, want 2", n)
	}
	if status(retried.ID) != orders.StatusPlaced || status(freshDecline.ID) != orders.StatusPlaced {
		t.Error("an order with a recent attempt must not expire")
	}
	if status(abandoned.ID) != orders.StatusCancelled || status(oldDecline.ID) != orders.StatusCancelled {
		t.Error("abandoned / never-retried orders must expire")
	}
	if st := paymentStatuses(t, abandoned.ID); len(st) != 1 || st[0] != "cancelled" {
		t.Errorf("abandoned attempt = %v, want [cancelled]", st)
	}
	if n := stockQty(t, f.VariantA, f.PointA); n != 3 {
		t.Errorf("stock after expiry = %d, want 3 (two orders returned)", n)
	}
	// Idempotent.
	if n, _ := pay.ExpirePending(ctx, ttl); n != 0 {
		t.Errorf("second pass expired %d", n)
	}
}

// TestConcurrentRetriesLeaveOnePending: parallel retries serialize on the
// order row — every one succeeds and exactly one attempt stays pending.
func TestConcurrentRetriesLeaveOnePending(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	f := newFixture(t, 5, 0)
	svc, _ := newService()
	pay, _ := payTestService(svc)
	o, _, _, err := pay.PlaceOnlineOrder(ctx, orders.PlaceOrderInput{
		CustomerID: f.CustomerID, Items: []orders.OrderItemInput{{VariantID: f.VariantA, Quantity: 1}}, PickupPointID: strptr(f.PointA),
	})
	if err != nil {
		t.Fatal(err)
	}
	const n = 6
	errs := make(chan error, n)
	for range n {
		go func() {
			_, err := pay.RetryPayment(ctx, f.CustomerID, o.ID)
			errs <- err
		}()
	}
	for range n {
		if err := <-errs; err != nil {
			t.Errorf("concurrent retry: %v", err)
		}
	}
	pending := 0
	for _, s := range paymentStatuses(t, o.ID) {
		if s == "pending" {
			pending++
		}
	}
	if pending != 1 {
		t.Errorf("pending attempts = %d, want 1 (%v)", pending, paymentStatuses(t, o.ID))
	}
}
