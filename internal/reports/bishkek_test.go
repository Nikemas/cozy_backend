package reports

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/orders"
)

func TestParseReportDateIsBishkekMidnight(t *testing.T) {
	got, err := ParseReportDate("2026-09-15")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 14, 18, 0, 0, 0, time.UTC) // 00:00 UTC+6
	if !got.Equal(want) {
		t.Errorf("ParseReportDate = %v, want %v", got, want)
	}
}

// An order placed at 02:00 Bishkek time (20:00 UTC the previous day) must
// be counted on its local calendar day, not the UTC one.
func TestAggregateSalesByDayUsesBishkekDays(t *testing.T) {
	o := orders.Order{
		ID: "o1", Status: orders.StatusPlaced, PaymentMethod: orders.PaymentCashOnDelivery, TotalAmount: 100,
		CreatedAt: time.Date(2026, 9, 14, 20, 0, 0, 0, time.UTC),
	}
	rows, err := AggregateSales([]orders.Order{o}, GroupByDay, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Key != "2026-09-15" {
		t.Fatalf("rows = %+v, want one row keyed 2026-09-15", rows)
	}
}

func TestCountsAsSale(t *testing.T) {
	ps := func(s orders.PaymentStatus) *orders.PaymentStatus { return &s }
	cases := []struct {
		name string
		o    orders.Order
		want bool
	}{
		{"cod placed", orders.Order{Status: orders.StatusPlaced, PaymentMethod: orders.PaymentCashOnDelivery}, true},
		{"cod cancelled", orders.Order{Status: orders.StatusCancelled, PaymentMethod: orders.PaymentCashOnDelivery}, false},
		{"online paid", orders.Order{Status: orders.StatusConfirmed, PaymentMethod: orders.PaymentOnlineCard, PaymentStatus: ps(orders.PaymentPaid)}, true},
		{"online pending", orders.Order{Status: orders.StatusPlaced, PaymentMethod: orders.PaymentOnlineCard, PaymentStatus: ps(orders.PaymentPending)}, false},
		{"online failed", orders.Order{Status: orders.StatusPlaced, PaymentMethod: orders.PaymentOnlineCard, PaymentStatus: ps(orders.PaymentFailed)}, false},
		{"online refunded", orders.Order{Status: orders.StatusDelivered, PaymentMethod: orders.PaymentOnlineCard, PaymentStatus: ps(orders.PaymentRefunded)}, false},
		{"online no status", orders.Order{Status: orders.StatusPlaced, PaymentMethod: orders.PaymentOnlineCard}, false},
	}
	for _, c := range cases {
		if got := CountsAsSale(c.o); got != c.want {
			t.Errorf("%s: CountsAsSale = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAggregateSalesExcludesUnpaidOnlineOrders(t *testing.T) {
	pending := orders.PaymentPending
	paid := orders.PaymentPaid
	list := []orders.Order{
		{ID: "a", Status: orders.StatusPlaced, PaymentMethod: orders.PaymentOnlineCard, PaymentStatus: &pending, TotalAmount: 5000,
			Items: []orders.OrderItem{{ProductNameSnapshot: "X", Quantity: 1, Price: 5000}}},
		{ID: "b", Status: orders.StatusPlaced, PaymentMethod: orders.PaymentOnlineCard, PaymentStatus: &paid, TotalAmount: 700,
			Items: []orders.OrderItem{{ProductNameSnapshot: "X", Quantity: 1, Price: 700}}},
	}
	for _, g := range []GroupBy{GroupByDay, GroupByProduct, GroupByPoint} {
		rows, err := AggregateSales(list, g, nil)
		if err != nil {
			t.Fatal(err)
		}
		var revenue float64
		var count int
		for _, r := range rows {
			revenue += r.Revenue
			count += r.OrderCount
		}
		if revenue != 700 || count != 1 {
			t.Errorf("%s: revenue = %v, orders = %d; want 700 / 1 (pending online order excluded)", g, revenue, count)
		}
	}
}

type stringSliceConverter struct{}

func (stringSliceConverter) ConvertValue(v any) (driver.Value, error) {
	if s, ok := v.([]string); ok {
		return s, nil
	}
	return driver.DefaultParameterConverter.ConvertValue(v)
}

func TestLoadOrdersBatchesItemsWithAnyArray(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.ValueConverterOption(stringSliceConverter{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	now := time.Now()
	mock.ExpectQuery(`FROM orders\s+WHERE created_at >= \$1 AND created_at < \$2`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "order_number", "customer_id", "address_id", "point_id", "status", "payment_method", "payment_status", "total_amount", "comment", "created_at", "updated_at"}).
			AddRow("o1", "N1", "c1", nil, nil, "placed", "cash_on_delivery", nil, 100.0, nil, now, now).
			AddRow("o2", "N2", "c1", nil, nil, "placed", "cash_on_delivery", nil, 200.0, nil, now, now))
	mock.ExpectQuery(`FROM order_items\s+WHERE order_id = ANY\(\$1\)`).WithArgs([]string{"o1", "o2"}).
		WillReturnRows(sqlmock.NewRows([]string{"id", "order_id", "variant_id", "product_name_snapshot", "size_snapshot", "color_snapshot", "quantity", "price"}).
			AddRow("i1", "o2", "v1", "Nike", "42", "Белый", 1, 200.0))

	list, err := NewRepo(db).LoadOrders(context.Background(), now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || len(list[1].Items) != 1 {
		t.Fatalf("list = %+v", list)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestCategorySalesQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	from, _ := ParseReportDate("2026-09-01")
	to, _ := ParseReportDate("2026-10-01")
	mock.ExpectQuery(`LEFT JOIN categories parent ON parent.id = c.parent_id.*o.status <> 'cancelled' AND \(o.payment_method <> 'online_card' OR o.payment_status = 'paid'\)`).
		WithArgs(from, to).
		WillReturnRows(sqlmock.NewRows([]string{"category", "order_count", "item_count", "revenue"}).
			AddRow("Мужская обувь", 3, 4, 18000.0).AddRow("Детская", 1, 1, 2500.0))

	rows, err := NewRepo(db).CategorySales(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Key != "Мужская обувь" || rows[0].Revenue != 18000 {
		t.Errorf("rows = %+v", rows)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
