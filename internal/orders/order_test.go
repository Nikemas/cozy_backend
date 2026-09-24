package orders

import (
	"context"
	"database/sql/driver"
	"regexp"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

func strPtr(s string) *string { return &s }

// --- pure-logic helpers: no database needed ---

func TestValidateFulfillmentExactlyOneRequired(t *testing.T) {
	cases := []struct {
		name          string
		addressID     *string
		pickupPointID *string
		wantErr       bool
	}{
		{"neither set", nil, nil, true},
		{"both set", strPtr("addr-1"), strPtr("point-1"), true},
		{"address only", strPtr("addr-1"), nil, false},
		{"pickup only", nil, strPtr("point-1"), false},
		{"both empty strings", strPtr(""), strPtr(""), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateFulfillment(c.addressID, c.pickupPointID)
			if c.wantErr && err == nil {
				t.Fatal("got nil error, want one")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("got error %v, want nil", err)
			}
		})
	}
}

func TestMergeItemQuantitiesRejectsEmpty(t *testing.T) {
	if _, _, err := mergeItemQuantities(nil); err == nil {
		t.Fatal("mergeItemQuantities(nil) succeeded, want an error")
	}
}

func TestMergeItemQuantitiesRejectsMissingVariant(t *testing.T) {
	_, _, err := mergeItemQuantities([]OrderItemInput{{VariantID: "", Quantity: 1}})
	if err == nil {
		t.Fatal("want an error for an empty VariantID")
	}
}

func TestMergeItemQuantitiesRejectsNonPositiveQty(t *testing.T) {
	_, _, err := mergeItemQuantities([]OrderItemInput{{VariantID: testVar1, Quantity: 0}})
	if err == nil {
		t.Fatal("want an error for a zero quantity")
	}
}

func TestMergeItemQuantitiesFoldsDuplicateVariants(t *testing.T) {
	merged, ids, err := mergeItemQuantities([]OrderItemInput{
		{VariantID: testVar2, Quantity: 1},
		{VariantID: testVar1, Quantity: 2},
		{VariantID: testVar2, Quantity: 3},
	})
	if err != nil {
		t.Fatalf("mergeItemQuantities: %v", err)
	}
	if merged[testVar2] != 4 {
		t.Errorf("merged[v2] = %d, want 4 (1+3)", merged[testVar2])
	}
	if merged[testVar1] != 2 {
		t.Errorf("merged[v1] = %d, want 2", merged[testVar1])
	}
	// ids must be sorted — that stable order is what keeps concurrent
	// orders' stock locks from deadlocking each other.
	if len(ids) != 2 || ids[0] != testVar1 || ids[1] != testVar2 {
		t.Errorf("ids = %v, want sorted [v1 v2]", ids)
	}
}

func TestFormatOrderNumber(t *testing.T) {
	cases := []struct {
		dateStr string
		seq     int
		want    string
	}{
		{"20260915", 1, "COZY-20260915-001"},
		{"20260915", 42, "COZY-20260915-042"},
		{"20260915", 999, "COZY-20260915-999"},
		{"20260915", 1000, "COZY-20260915-1000"},
	}
	for _, c := range cases {
		if got := formatOrderNumber(c.dateStr, c.seq); got != c.want {
			t.Errorf("formatOrderNumber(%q, %d) = %q, want %q", c.dateStr, c.seq, got, c.want)
		}
	}
}

// --- Service.CreateOrder against a scripted fake DB (no live Postgres
// available in this environment — see the task report) ---

func newMockService(t *testing.T) (*Service, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewService(db), mock
}

func TestCreateOrderRejectsInvalidFulfillmentBeforeTouchingDB(t *testing.T) {
	svc, mock := newMockService(t)

	_, err := svc.CreateOrder(context.Background(), "cust-1", []OrderItemInput{{VariantID: "v1", Quantity: 1}}, nil, nil)
	if err == nil {
		t.Fatal("want an error when neither addressID nor pickupPointID is set")
	}
	appErr, ok := err.(*apperr.AppError)
	if !ok || appErr.Code != "invalid_fulfillment" {
		t.Fatalf("got %v, want apperr invalid_fulfillment", err)
	}
	// Validation must fail before opening a transaction at all.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected DB interaction: %v", err)
	}
}

// TestCreateOrderPickupHappyPath drives a full self-pickup order through
// Service.CreateOrder: customer lock, open-orders count, point-active
// check, variant snapshot, stock lock+decrement, order-number generation,
// the order/order_items inserts and the initial history row, in the exact
// sequence CreateOrder issues them. Pickup → no delivery fee.
func TestCreateOrderPickupHappyPath(t *testing.T) {
	svc, mock := newMockService(t)
	svc.WithSettings(testSettings())

	expectOrderPrologue(mock, 0)
	expectPickupLine(mock, testVar1, 5000, 10, 2)
	expectOrderInsert(mock, []driver.Value{sqlmock.AnyArg(), "cust-1", nil, "point-1", StatusPlaced, PaymentCashOnDelivery, nil,
		10000.0, 0.0, nil, nil, nil})
	mock.ExpectCommit()

	pickupID := "point-1"
	order, err := svc.CreateOrder(context.Background(), "cust-1",
		[]OrderItemInput{{VariantID: testVar1, Quantity: 2}}, nil, &pickupID)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if order.TotalAmount != 10000 || order.DeliveryFee != 0 {
		t.Errorf("TotalAmount/DeliveryFee = %v/%v, want 10000/0 (2 * 5000, pickup is free)", order.TotalAmount, order.DeliveryFee)
	}
	if order.PointID == nil || *order.PointID != "point-1" {
		t.Errorf("PointID = %v, want point-1", order.PointID)
	}
	if order.AddressID != nil {
		t.Errorf("AddressID = %v, want nil for a pickup order", *order.AddressID)
	}
	if len(order.Items) != 1 || order.Items[0].Price != 5000 || order.Items[0].Quantity != 2 {
		t.Errorf("Items = %+v, want one line: qty 2 @ 5000", order.Items)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestCreateOrderDeliveryAddsFeeToTotal: a delivery order's total_amount
// includes the configured delivery fee and records it in delivery_fee.
func TestCreateOrderDeliveryAddsFeeToTotal(t *testing.T) {
	svc, mock := newMockService(t)
	svc.WithSettings(Settings{DeliveryFee: 250, MaxOpenOrders: 0})

	expectOrderPrologue(mock, -1) // cap disabled → no count query
	mock.ExpectQuery(regexp.QuoteMeta("FROM customer_addresses")).
		WithArgs("addr-1", "cust-1").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	// No active delivery zone → the flat fee applies.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS (SELECT 1 FROM delivery_zones WHERE is_active)")).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(regexp.QuoteMeta("FROM product_variants pv")).
		WillReturnRows(sqlmock.NewRows(variantColumns).AddRow(testVar1, "42", "Черный", nil, "Air Max", 5000.0, true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM points_of_sale WHERE is_active = true")).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("point-1"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT quantity FROM stock WHERE variant_id = $1 AND point_id = $2")).
		WithArgs(testVar1, "point-1").WillReturnRows(sqlmock.NewRows([]string{"quantity"}).AddRow(3))
	mock.ExpectQuery(regexp.QuoteMeta("FOR UPDATE")).
		WithArgs(testVar1, "point-1").WillReturnRows(sqlmock.NewRows([]string{"quantity"}).AddRow(3))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE stock SET quantity")).WillReturnResult(sqlmock.NewResult(0, 1))
	expectOrderInsert(mock, []driver.Value{sqlmock.AnyArg(), "cust-1", "addr-1", "point-1", StatusPlaced, PaymentCashOnDelivery, nil,
		5250.0, 250.0, "позвоните за час", nil, nil})
	mock.ExpectCommit()

	addr := "addr-1"
	order, created, err := svc.PlaceOrder(context.Background(), PlaceOrderInput{
		CustomerID: "cust-1", Items: []OrderItemInput{{VariantID: testVar1, Quantity: 1}},
		AddressID: &addr, Comment: "  позвоните за час ",
	})
	if err != nil || !created {
		t.Fatalf("PlaceOrder = %v, %v", created, err)
	}
	if order.TotalAmount != 5250 || order.DeliveryFee != 250 {
		t.Errorf("total/fee = %v/%v, want 5250/250", order.TotalAmount, order.DeliveryFee)
	}
	if order.Comment == nil || *order.Comment != "позвоните за час" {
		t.Errorf("comment = %v, want trimmed text", order.Comment)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestCreateOrderInsufficientStockRollsBack checks that a stock row
// locked under FOR UPDATE with less quantity than requested fails the
// whole order (and rolls back) rather than partially decrementing, and
// that the error names the product.
func TestCreateOrderInsufficientStockRollsBack(t *testing.T) {
	svc, mock := newMockService(t)
	svc.WithSettings(testSettings())

	expectOrderPrologue(mock, 0)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT is_active FROM points_of_sale")).
		WithArgs("point-1").
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta("FROM product_variants pv")).
		WithArgs(testVar1).
		WillReturnRows(sqlmock.NewRows(variantColumns).AddRow(testVar1, "42", "Черный", nil, "Air Max", 5000.0, true))
	// Only 1 in stock, but the order asks for 2.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT quantity FROM stock")).
		WithArgs(testVar1, "point-1").
		WillReturnRows(sqlmock.NewRows([]string{"quantity"}).AddRow(1))
	mock.ExpectRollback()

	pickupID := "point-1"
	_, err := svc.CreateOrder(context.Background(), "cust-1",
		[]OrderItemInput{{VariantID: testVar1, Quantity: 2}}, nil, &pickupID)
	if err == nil {
		t.Fatal("CreateOrder succeeded, want insufficient_stock error")
	}
	appErr, ok := err.(*apperr.AppError)
	if !ok || appErr.Code != "insufficient_stock" {
		t.Fatalf("got %v, want apperr insufficient_stock", err)
	}
	if !strings.Contains(appErr.Message, "Air Max, 42") || !strings.Contains(appErr.Message, "1 шт") {
		t.Errorf("message %q should name the line and what is left", appErr.Message)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations (e.g. no UPDATE stock should have run): %v", err)
	}
}

// TestCreateOrderUnknownVariantRollsBack checks that a variant ID with no
// matching product_variants row fails the order instead of silently
// skipping it.
func TestCreateOrderUnknownVariantRollsBack(t *testing.T) {
	svc, mock := newMockService(t)
	svc.WithSettings(testSettings())

	expectOrderPrologue(mock, 0)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT is_active FROM points_of_sale")).
		WithArgs("point-1").
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta("FROM product_variants pv")).
		WithArgs(testVarMissing).
		WillReturnRows(sqlmock.NewRows(variantColumns))
	mock.ExpectRollback()

	pickupID := "point-1"
	_, err := svc.CreateOrder(context.Background(), "cust-1",
		[]OrderItemInput{{VariantID: testVarMissing, Quantity: 1}}, nil, &pickupID)
	appErr, ok := err.(*apperr.AppError)
	if !ok || appErr.Code != "variant_not_found" {
		t.Fatalf("got %v, want apperr variant_not_found", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestCreateOrderInactiveProductNamesIt: a deactivated product fails with
// 409 product_unavailable naming it.
func TestCreateOrderInactiveProductNamesIt(t *testing.T) {
	svc, mock := newMockService(t)
	svc.WithSettings(testSettings())

	expectOrderPrologue(mock, 0)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT is_active FROM points_of_sale")).
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta("FROM product_variants pv")).
		WillReturnRows(sqlmock.NewRows(variantColumns).AddRow(testVar1, "40", "Белый", nil, "Old Boot", 3000.0, false))
	mock.ExpectRollback()

	pickupID := "point-1"
	_, err := svc.CreateOrder(context.Background(), "cust-1",
		[]OrderItemInput{{VariantID: testVar1, Quantity: 1}}, nil, &pickupID)
	appErr, ok := err.(*apperr.AppError)
	if !ok || appErr.Code != "product_unavailable" || !strings.Contains(appErr.Message, "Old Boot, 40") {
		t.Fatalf("got %v, want product_unavailable naming the product", err)
	}
}

// TestCreateOrderRejectsMalformedVariantID: a non-UUID variant id is a 400
// before any SQL, not a Postgres invalid-uuid 500.
func TestCreateOrderRejectsMalformedVariantID(t *testing.T) {
	svc, mock := newMockService(t)
	pickupID := "point-1"
	_, err := svc.CreateOrder(context.Background(), "cust-1",
		[]OrderItemInput{{VariantID: "not-a-uuid", Quantity: 1}}, nil, &pickupID)
	appErr, ok := err.(*apperr.AppError)
	if !ok || appErr.Code != "invalid_variant_id" || appErr.Status != 400 {
		t.Fatalf("got %v, want 400 invalid_variant_id", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected DB interaction: %v", err)
	}
}

func TestMergeItemQuantitiesCapsQuantity(t *testing.T) {
	_, _, err := mergeItemQuantities([]OrderItemInput{{VariantID: testVar1, Quantity: MaxCartQty}, {VariantID: testVar1, Quantity: 1}})
	if appErr, ok := err.(*apperr.AppError); !ok || appErr.Code != "qty_too_large" {
		t.Fatalf("got %v, want qty_too_large for merged lines over the cap", err)
	}
}

func TestNormalizeComment(t *testing.T) {
	if c, err := normalizeComment("   "); err != nil || c != nil {
		t.Errorf("blank comment → %v, %v; want nil, nil", c, err)
	}
	if _, err := normalizeComment(strings.Repeat("я", MaxCommentLen)); err != nil {
		t.Errorf("%d runes must be accepted: %v", MaxCommentLen, err)
	}
	if _, err := normalizeComment(strings.Repeat("я", MaxCommentLen+1)); err == nil {
		t.Error("comment over the limit accepted")
	}
}

func TestNormalizeIdempotencyKey(t *testing.T) {
	if k, err := normalizeIdempotencyKey(""); err != nil || k != nil {
		t.Errorf("empty key → %v, %v", k, err)
	}
	if k, err := normalizeIdempotencyKey("3f1c9a2e-8b7d-4c1a-9e2f-0a1b2c3d4e5f"); err != nil || k == nil {
		t.Errorf("uuid key rejected: %v", err)
	}
	for _, bad := range []string{strings.Repeat("a", MaxIdempotencyKeyLen+1), "has space", "ключ"} {
		if _, err := normalizeIdempotencyKey(bad); err == nil {
			t.Errorf("key %q accepted", bad)
		}
	}
}

func TestOrderKeyPredicate(t *testing.T) {
	if got := orderKeyPredicate("3f1c9a2e-8b7d-4c1a-9e2f-0a1b2c3d4e5f", 2); got != "id = $2::uuid" {
		t.Errorf("uuid: got %q", got)
	}
	if got := orderKeyPredicate("COZY-20260923-004", 1); got != "order_number = $1" {
		t.Errorf("order number: got %q", got)
	}
	if got := orderKeyPredicate("", 1); got != "order_number = $1" {
		t.Errorf("empty: got %q", got)
	}
}
