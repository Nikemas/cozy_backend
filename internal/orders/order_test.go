package orders

import (
	"context"
	"regexp"
	"testing"
	"time"

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
	_, _, err := mergeItemQuantities([]OrderItemInput{{VariantID: "v1", Quantity: 0}})
	if err == nil {
		t.Fatal("want an error for a zero quantity")
	}
}

func TestMergeItemQuantitiesFoldsDuplicateVariants(t *testing.T) {
	merged, ids, err := mergeItemQuantities([]OrderItemInput{
		{VariantID: "v2", Quantity: 1},
		{VariantID: "v1", Quantity: 2},
		{VariantID: "v2", Quantity: 3},
	})
	if err != nil {
		t.Fatalf("mergeItemQuantities: %v", err)
	}
	if merged["v2"] != 4 {
		t.Errorf("merged[v2] = %d, want 4 (1+3)", merged["v2"])
	}
	if merged["v1"] != 2 {
		t.Errorf("merged[v1] = %d, want 2", merged["v1"])
	}
	// ids must be sorted — that stable order is what keeps concurrent
	// orders' stock locks from deadlocking each other.
	if len(ids) != 2 || ids[0] != "v1" || ids[1] != "v2" {
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
// Service.CreateOrder: point-active check, variant snapshot, stock
// lock+decrement, order-number generation, and the order/order_items
// inserts, in the exact sequence CreateOrder issues them.
func TestCreateOrderPickupHappyPath(t *testing.T) {
	svc, mock := newMockService(t)

	mock.ExpectBegin()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT is_active FROM points_of_sale")).
		WithArgs("point-1").
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))

	mock.ExpectQuery(regexp.QuoteMeta("FROM product_variants pv")).
		WithArgs("var-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "size", "color", "price_override", "name_ru", "base_price"}).
			AddRow("var-1", "42", "Черный", nil, "Air Max", 5000.0))

	mock.ExpectQuery(regexp.QuoteMeta("SELECT quantity FROM stock")).
		WithArgs("var-1", "point-1").
		WillReturnRows(sqlmock.NewRows([]string{"quantity"}).AddRow(10))

	mock.ExpectExec(regexp.QuoteMeta("UPDATE stock SET quantity")).
		WithArgs(2, "var-1", "point-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	mock.ExpectExec(regexp.QuoteMeta("pg_advisory_xact_lock")).
		WillReturnResult(sqlmock.NewResult(0, 0))

	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM orders")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO orders")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow("order-1", time.Now(), time.Now()))

	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO order_items")).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("item-1"))

	mock.ExpectCommit()

	pickupID := "point-1"
	order, err := svc.CreateOrder(context.Background(), "cust-1",
		[]OrderItemInput{{VariantID: "var-1", Quantity: 2}}, nil, &pickupID)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if order.TotalAmount != 10000 {
		t.Errorf("TotalAmount = %v, want 10000 (2 * 5000)", order.TotalAmount)
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

// TestCreateOrderInsufficientStockRollsBack checks that a stock row
// locked under FOR UPDATE with less quantity than requested fails the
// whole order (and rolls back) rather than partially decrementing.
func TestCreateOrderInsufficientStockRollsBack(t *testing.T) {
	svc, mock := newMockService(t)

	mock.ExpectBegin()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT is_active FROM points_of_sale")).
		WithArgs("point-1").
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))

	mock.ExpectQuery(regexp.QuoteMeta("FROM product_variants pv")).
		WithArgs("var-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "size", "color", "price_override", "name_ru", "base_price"}).
			AddRow("var-1", "42", "Черный", nil, "Air Max", 5000.0))

	// Only 1 in stock, but the order asks for 2.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT quantity FROM stock")).
		WithArgs("var-1", "point-1").
		WillReturnRows(sqlmock.NewRows([]string{"quantity"}).AddRow(1))

	mock.ExpectRollback()

	pickupID := "point-1"
	_, err := svc.CreateOrder(context.Background(), "cust-1",
		[]OrderItemInput{{VariantID: "var-1", Quantity: 2}}, nil, &pickupID)
	if err == nil {
		t.Fatal("CreateOrder succeeded, want insufficient_stock error")
	}
	appErr, ok := err.(*apperr.AppError)
	if !ok || appErr.Code != "insufficient_stock" {
		t.Fatalf("got %v, want apperr insufficient_stock", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations (e.g. no UPDATE stock should have run): %v", err)
	}
}

// TestCreateOrderUnknownVariantRollsBack checks that a variant ID with no
// matching product_variants/products row (deleted/deactivated product)
// fails the order instead of silently skipping it.
func TestCreateOrderUnknownVariantRollsBack(t *testing.T) {
	svc, mock := newMockService(t)

	mock.ExpectBegin()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT is_active FROM points_of_sale")).
		WithArgs("point-1").
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))

	// No rows back for the requested variant.
	mock.ExpectQuery(regexp.QuoteMeta("FROM product_variants pv")).
		WithArgs("var-missing").
		WillReturnRows(sqlmock.NewRows([]string{"id", "size", "color", "price_override", "name_ru", "base_price"}))

	mock.ExpectRollback()

	pickupID := "point-1"
	_, err := svc.CreateOrder(context.Background(), "cust-1",
		[]OrderItemInput{{VariantID: "var-missing", Quantity: 1}}, nil, &pickupID)
	if err == nil {
		t.Fatal("CreateOrder succeeded, want variant_not_found error")
	}
	appErr, ok := err.(*apperr.AppError)
	if !ok || appErr.Code != "variant_not_found" {
		t.Fatalf("got %v, want apperr variant_not_found", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
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
