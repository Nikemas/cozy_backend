package orders

import (
	"context"
	"database/sql/driver"
	"errors"
	"math"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

func fptr(f float64) *float64 { return &f }

func TestDeliveryZoneFeeFor(t *testing.T) {
	z := DeliveryZone{Fee: 300, FreeFrom: fptr(10000)}
	cases := []struct {
		items, want float64
	}{
		{0, 300},
		{9999.99, 300},
		{10000, 0}, // threshold is inclusive
		{25000, 0},
	}
	for _, c := range cases {
		if got := z.FeeFor(c.items); got != c.want {
			t.Errorf("FeeFor(%v) = %v, want %v", c.items, got, c.want)
		}
	}
	if got := (DeliveryZone{Fee: 150}).FeeFor(1e7); got != 150 {
		t.Errorf("zone without free_from: FeeFor = %v, want 150", got)
	}
}

func TestDeliveryFee(t *testing.T) {
	st := Settings{DeliveryFee: 200}
	zone := &DeliveryZone{Fee: 350, FreeFrom: fptr(5000)}
	cases := []struct {
		name       string
		isDelivery bool
		zone       *DeliveryZone
		items      float64
		want       float64
	}{
		{"pickup is free", false, zone, 100, 0},
		{"no zones: flat fee", true, nil, 100, 200},
		{"zone fee", true, zone, 4999, 350},
		{"zone free from", true, zone, 5000, 0},
	}
	for _, c := range cases {
		if got := deliveryFee(st, c.isDelivery, c.zone, c.items); got != c.want {
			t.Errorf("%s: deliveryFee = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDeliveryZoneInputNormalize(t *testing.T) {
	ok := DeliveryZoneInput{NameRu: "  Бишкек ", NameKy: "Бишкек", Fee: 200.004, FreeFrom: fptr(5000)}
	if err := ok.Normalize(); err != nil {
		t.Fatalf("valid input: %v", err)
	}
	if ok.NameRu != "Бишкек" || ok.Fee != 200 {
		t.Errorf("normalized = %+v", ok)
	}
	bad := []DeliveryZoneInput{
		{NameRu: "", NameKy: "x", Fee: 1},
		{NameRu: "x", NameKy: " ", Fee: 1},
		{NameRu: "x", NameKy: "x", Fee: -1},
		{NameRu: "x", NameKy: "x", Fee: math.NaN()},
		{NameRu: "x", NameKy: "x", Fee: math.Inf(1)},
		{NameRu: "x", NameKy: "x", Fee: 100001},
		{NameRu: "x", NameKy: "x", Fee: 1, FreeFrom: fptr(0)},
		{NameRu: "x", NameKy: "x", Fee: 1, FreeFrom: fptr(math.NaN())},
	}
	for i, in := range bad {
		var appErr *apperr.AppError
		if err := in.Normalize(); !errors.As(err, &appErr) || appErr.Code != "invalid_delivery_zone_input" {
			t.Errorf("case %d (%+v): err = %v, want invalid_delivery_zone_input", i, in, err)
		}
	}
}

var zoneColumnNames = []string{"id", "name_ru", "name_ky", "fee", "free_from", "is_active", "sort_order"}

const testZone = "33333333-3333-4333-8333-333333333333"

// expectZonedDeliveryUntilInsert scripts a delivery order's address
// check, the lookup of testZone (fee zoneFee, free_from freeFrom), then one
// variant fulfilled from point-1.
func expectZonedDeliveryUntilInsert(mock sqlmock.Sqlmock, zoneFee float64, freeFrom any) {
	mock.ExpectQuery(regexp.QuoteMeta("FROM customer_addresses")).
		WithArgs("addr-1", "cust-1").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta("FROM delivery_zones WHERE id = $1 AND is_active")).
		WithArgs(testZone).
		WillReturnRows(sqlmock.NewRows(zoneColumnNames).AddRow(testZone, "Бишкек", "Бишкек", zoneFee, freeFrom, true, 0))
	mock.ExpectQuery(regexp.QuoteMeta("FROM product_variants pv")).
		WillReturnRows(sqlmock.NewRows(variantColumns).AddRow(testVar1, "42", "Черный", nil, "Air Max", 5000.0, true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM points_of_sale WHERE is_active = true")).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("point-1"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT quantity FROM stock WHERE variant_id = $1 AND point_id = $2")).
		WithArgs(testVar1, "point-1").WillReturnRows(sqlmock.NewRows([]string{"quantity"}).AddRow(3))
	mock.ExpectQuery(regexp.QuoteMeta("FOR UPDATE")).
		WithArgs(testVar1, "point-1").WillReturnRows(sqlmock.NewRows([]string{"quantity"}).AddRow(3))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE stock SET quantity")).WillReturnResult(sqlmock.NewResult(0, 1))
}

// TestCreateOrderChargesZoneFee: the fee is the chosen zone's (not the
// flat DELIVERY_FEE_SOM), 0 once the items total reaches free_from, and
// the zone is stored on the order and carried in its JSON.
func TestCreateOrderChargesZoneFee(t *testing.T) {
	cases := []struct {
		name     string
		qty      int
		freeFrom any
		wantFee  float64
	}{
		{"below free_from", 1, 10000.0, 350},
		{"at free_from", 2, 10000.0, 0},
		{"no free_from", 2, nil, 350},
	}
	for _, c := range cases {
		svc, mock := newMockService(t)
		svc.WithSettings(Settings{DeliveryFee: 200})
		expectOrderPrologue(mock, -1)
		expectZonedDeliveryUntilInsert(mock, 350, c.freeFrom)
		items := 5000.0 * float64(c.qty)
		expectOrderInsert(mock, []driver.Value{sqlmock.AnyArg(), "cust-1", "addr-1", "point-1", StatusPlaced, PaymentCashOnDelivery, nil,
			items + c.wantFee, c.wantFee, nil, nil, testZone})
		mock.ExpectCommit()

		addr, zone := "addr-1", testZone
		order, _, err := svc.PlaceOrder(context.Background(), PlaceOrderInput{
			CustomerID: "cust-1", Items: []OrderItemInput{{VariantID: testVar1, Quantity: c.qty}},
			AddressID: &addr, DeliveryZoneID: &zone,
		})
		if err != nil {
			t.Fatalf("%s: PlaceOrder: %v", c.name, err)
		}
		if order.DeliveryFee != c.wantFee || order.TotalAmount != items+c.wantFee {
			t.Errorf("%s: fee/total = %v/%v, want %v/%v", c.name, order.DeliveryFee, order.TotalAmount, c.wantFee, items+c.wantFee)
		}
		if order.DeliveryZone == nil || order.DeliveryZone.ID != testZone || order.DeliveryZone.NameRu != "Бишкек" {
			t.Errorf("%s: DeliveryZone = %+v", c.name, order.DeliveryZone)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestCreateOrderZoneErrors(t *testing.T) {
	t.Run("required while zones exist", func(t *testing.T) {
		svc, mock := newMockService(t)
		svc.WithSettings(Settings{DeliveryFee: 200})
		expectOrderPrologue(mock, -1)
		mock.ExpectQuery(regexp.QuoteMeta("FROM customer_addresses")).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS (SELECT 1 FROM delivery_zones WHERE is_active)")).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
		mock.ExpectRollback()

		addr := "addr-1"
		_, _, err := svc.PlaceOrder(context.Background(), PlaceOrderInput{
			CustomerID: "cust-1", Items: []OrderItemInput{{VariantID: testVar1, Quantity: 1}}, AddressID: &addr,
		})
		if !errors.Is(err, ErrDeliveryZoneRequired) {
			t.Fatalf("err = %v, want delivery_zone_required", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
	t.Run("inactive or unknown zone", func(t *testing.T) {
		svc, mock := newMockService(t)
		svc.WithSettings(Settings{DeliveryFee: 200})
		expectOrderPrologue(mock, -1)
		mock.ExpectQuery(regexp.QuoteMeta("FROM customer_addresses")).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
		mock.ExpectQuery(regexp.QuoteMeta("FROM delivery_zones WHERE id = $1 AND is_active")).
			WillReturnRows(sqlmock.NewRows(zoneColumnNames))
		mock.ExpectRollback()

		addr, zone := "addr-1", testZone
		_, _, err := svc.PlaceOrder(context.Background(), PlaceOrderInput{
			CustomerID: "cust-1", Items: []OrderItemInput{{VariantID: testVar1, Quantity: 1}},
			AddressID: &addr, DeliveryZoneID: &zone,
		})
		if !errors.Is(err, ErrInvalidDeliveryZone) {
			t.Fatalf("err = %v, want invalid_delivery_zone", err)
		}
	})
	t.Run("malformed zone id never reaches the DB", func(t *testing.T) {
		svc, mock := newMockService(t)
		svc.WithSettings(Settings{DeliveryFee: 200})
		expectOrderPrologue(mock, -1)
		mock.ExpectQuery(regexp.QuoteMeta("FROM customer_addresses")).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
		mock.ExpectRollback()

		addr, zone := "addr-1", "not-a-uuid"
		_, _, err := svc.PlaceOrder(context.Background(), PlaceOrderInput{
			CustomerID: "cust-1", Items: []OrderItemInput{{VariantID: testVar1, Quantity: 1}},
			AddressID: &addr, DeliveryZoneID: &zone,
		})
		if !errors.Is(err, ErrInvalidDeliveryZone) {
			t.Fatalf("err = %v, want invalid_delivery_zone", err)
		}
	})
}

// TestCreatePickupOrderIgnoresZone: a zone sent with a pickup order is
// ignored — no fee, no zone stored.
func TestCreatePickupOrderIgnoresZone(t *testing.T) {
	svc, mock := newMockService(t)
	svc.WithSettings(testSettings())
	expectOrderPrologue(mock, 0)
	expectPickupLine(mock, testVar1, 5000, 10, 1)
	expectOrderInsert(mock, []driver.Value{sqlmock.AnyArg(), "cust-1", nil, "point-1", StatusPlaced, PaymentCashOnDelivery, nil,
		5000.0, 0.0, nil, nil, nil})
	mock.ExpectCommit()

	point, zone := "point-1", testZone
	order, _, err := svc.PlaceOrder(context.Background(), PlaceOrderInput{
		CustomerID: "cust-1", Items: []OrderItemInput{{VariantID: testVar1, Quantity: 1}},
		PickupPointID: &point, DeliveryZoneID: &zone,
	})
	if err != nil {
		t.Fatal(err)
	}
	if order.DeliveryZone != nil || order.DeliveryFee != 0 {
		t.Errorf("pickup order zone/fee = %+v/%v", order.DeliveryZone, order.DeliveryFee)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestScanOrderRowFillsDeliveryZone(t *testing.T) {
	svc, mock := newMockService(t)
	r := codOrder(StatusPlaced)
	rows := sqlmock.NewRows(orderColumnNames).AddRow(r.id, r.number, r.customer, "addr-1", r.point,
		string(r.status), string(r.method), nil, 5350.0, 350.0, false, nil, time.Now(), time.Now(),
		testZone, "Бишкек", "Бишкек ш.")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, order_number")).WillReturnRows(rows)
	expectNoItems(mock)

	o, err := svc.GetOrder(context.Background(), "cust-1", "order-1")
	if err != nil {
		t.Fatal(err)
	}
	if o.DeliveryZone == nil || *o.DeliveryZone != (OrderDeliveryZone{ID: testZone, NameRu: "Бишкек", NameKy: "Бишкек ш."}) {
		t.Errorf("DeliveryZone = %+v", o.DeliveryZone)
	}
}
