package orders

import (
	"context"
	"database/sql/driver"
	"regexp"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/staff"
)

// Variant ids must be real UUIDs now that CreateOrder/CartRepo reject
// malformed ones before touching the database.
const (
	testVar1       = "11111111-1111-4111-8111-111111111111"
	testVar2       = "22222222-2222-4222-8222-222222222222"
	testVarMissing = "99999999-9999-4999-8999-999999999999"
)

// orderColumnNames matches orderColumns (the scanOrderRow column list).
var orderColumnNames = []string{"id", "order_number", "customer_id", "address_id", "point_id", "status", "payment_method",
	"payment_status", "total_amount", "delivery_fee", "refund_required", "comment", "created_at", "updated_at"}

var variantColumns = []string{"id", "size", "color", "price_override", "name_ru", "base_price", "is_active"}

// orderRow is one scripted orders row for sqlmock.
type orderRow struct {
	id, number, customer string
	address, point       any // nil or string
	status               OrderStatus
	method               PaymentMethod
	payment              any // nil or string
	total, fee           float64
	refund               bool
}

func (r orderRow) rows() *sqlmock.Rows {
	now := time.Now()
	return sqlmock.NewRows(orderColumnNames).AddRow(r.id, r.number, r.customer, r.address, r.point,
		string(r.status), string(r.method), r.payment, r.total, r.fee, r.refund, nil, now, now)
}

func codOrder(status OrderStatus) orderRow {
	return orderRow{id: "order-1", number: "COZY-20260923-001", customer: "cust-1", address: nil, point: "point-1",
		status: status, method: PaymentCashOnDelivery, total: 5000}
}

func onlineOrder(status OrderStatus, payment PaymentStatus) orderRow {
	return orderRow{id: "order-1", number: "COZY-20260923-001", customer: "cust-1", address: nil, point: "point-1",
		status: status, method: PaymentOnlineCard, payment: string(payment), total: 5000}
}

// testSettings: delivery 200, open-orders cap 5.
func testSettings() Settings {
	return Settings{DeliveryFee: 200, MaxOpenOrders: 5, PaymentPendingTTL: 30 * time.Minute}
}

// expectOrderPrologue scripts the start of createOrder: BEGIN, the
// per-customer advisory lock, and (openCount >= 0) the open-orders count.
func expectOrderPrologue(mock sqlmock.Sqlmock, openCount int) {
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("pg_advisory_xact_lock")).
		WithArgs("order_customer:cust-1").WillReturnResult(sqlmock.NewResult(0, 0))
	if openCount >= 0 {
		mock.ExpectQuery(regexp.QuoteMeta("status IN ('placed', 'confirmed', 'courier_assigned')")).
			WithArgs("cust-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(openCount))
	}
}

// expectPickupLine scripts point check, one variant snapshot, its stock
// lock and decrement at point-1.
func expectPickupLine(mock sqlmock.Sqlmock, variantID string, price float64, stock, qty int) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT is_active FROM points_of_sale")).
		WithArgs("point-1").WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta("FROM product_variants pv")).
		WithArgs(variantID).
		WillReturnRows(sqlmock.NewRows(variantColumns).AddRow(variantID, "42", "Черный", nil, "Air Max", price, true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT quantity FROM stock WHERE variant_id = $1 AND point_id = $2 FOR UPDATE")).
		WithArgs(variantID, "point-1").WillReturnRows(sqlmock.NewRows([]string{"quantity"}).AddRow(stock))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE stock SET quantity")).
		WithArgs(qty, variantID, "point-1").WillReturnResult(sqlmock.NewResult(0, 1))
}

// expectOrderInsert scripts order-number generation, the orders insert
// (args checked when insertArgs is non-nil), one order_items insert and
// the initial history row.
func expectOrderInsert(mock sqlmock.Sqlmock, insertArgs []driver.Value) {
	mock.ExpectExec(regexp.QuoteMeta("pg_advisory_xact_lock")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("WHERE order_number LIKE")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	q := mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO orders"))
	if insertArgs != nil {
		q.WithArgs(insertArgs...)
	}
	q.WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow("order-1", time.Now(), time.Now()))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO order_items")).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("item-1"))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO order_status_history")).
		WithArgs("order-1", nil, StatusPlaced, ActorCustomer, nil, nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// expectRestock scripts restockOrderTx for one line.
func expectRestock(mock sqlmock.Sqlmock, variantID string, qty int) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT variant_id, quantity FROM order_items")).
		WithArgs("order-1").WillReturnRows(sqlmock.NewRows([]string{"variant_id", "quantity"}).AddRow(variantID, qty))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO stock")).
		WithArgs(variantID, "point-1", qty).WillReturnResult(sqlmock.NewResult(0, 1))
}

func expectNoItems(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("FROM order_items")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "order_id", "variant_id", "product_name_snapshot", "size_snapshot", "color_snapshot", "quantity", "price"}))
}

func staffCtx(role staff.Role) context.Context {
	return staff.NewContextWithStaff(context.Background(), &staff.Staff{ID: "staff-1", Role: role})
}
