package reports

import (
	"testing"
	"time"

	"github.com/Nikemas/cozy_backend/internal/orders"
)

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse(dateLayout, s)
	if err != nil {
		t.Fatalf("bad test date %q: %v", s, err)
	}
	return d
}

// --- ParseGroupBy ---

func TestParseGroupBy(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    GroupBy
		wantErr bool
	}{
		{"day", "day", GroupByDay, false},
		{"product", "product", GroupByProduct, false},
		{"point", "point", GroupByPoint, false},
		{"empty", "", "", true},
		{"unknown value", "week", "", true},
		{"case sensitive", "Day", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseGroupBy(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("ParseGroupBy(%q) = %v, nil; want an error", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseGroupBy(%q) unexpected error: %v", c.in, err)
			}
			if got != c.want {
				t.Fatalf("ParseGroupBy(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// --- ParseReportDate ---

func TestParseReportDate(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"valid date", "2026-01-15", false},
		{"empty", "", true},
		{"wrong format slashes", "2026/01/15", true},
		{"wrong format order", "15-01-2026", true},
		{"with time component", "2026-01-15T00:00:00Z", true},
		{"garbage", "not-a-date", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseReportDate(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("ParseReportDate(%q) = %v, nil; want an error", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseReportDate(%q) unexpected error: %v", c.in, err)
			}
		})
	}
}

// --- ValidateRange ---

func TestValidateRange(t *testing.T) {
	cases := []struct {
		name    string
		from    string
		to      string
		wantErr bool
	}{
		{"from before to", "2026-01-01", "2026-01-31", false},
		{"from equals to", "2026-01-15", "2026-01-15", false},
		{"from after to", "2026-01-31", "2026-01-01", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			from := mustDate(t, c.from)
			to := mustDate(t, c.to)
			err := ValidateRange(from, to)
			if c.wantErr && err == nil {
				t.Fatalf("ValidateRange(%s, %s) = nil, want an error", c.from, c.to)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("ValidateRange(%s, %s) unexpected error: %v", c.from, c.to, err)
			}
		})
	}
}

// --- AggregateSales ---

func strPtr(s string) *string { return &s }

func orderAt(id, dateStr string, status orders.OrderStatus, pointID *string, total float64, items ...orders.OrderItem) orders.Order {
	return orders.Order{
		ID:            id,
		Status:        status,
		PointID:       pointID,
		TotalAmount:   total,
		CreatedAt:     mustDateNoT(dateStr),
		PaymentMethod: orders.PaymentCashOnDelivery,
		Items:         items,
	}
}

// mustDateNoT parses without *testing.T, for use in table literals below.
func mustDateNoT(s string) time.Time {
	d, err := time.Parse(dateLayout, s)
	if err != nil {
		panic(err)
	}
	return d
}

func item(name string, qty int, price float64) orders.OrderItem {
	return orders.OrderItem{ProductNameSnapshot: name, Quantity: qty, Price: price}
}

func TestAggregateSalesEmptyInput(t *testing.T) {
	for _, gb := range []GroupBy{GroupByDay, GroupByProduct, GroupByPoint} {
		t.Run(string(gb), func(t *testing.T) {
			rows, err := AggregateSales(nil, gb, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(rows) != 0 {
				t.Fatalf("expected zero rows for empty input, got %+v", rows)
			}
		})
	}
}

func TestAggregateSalesInvalidGroupBy(t *testing.T) {
	_, err := AggregateSales(nil, GroupBy("bogus"), nil)
	if err == nil {
		t.Fatal("expected an error for an invalid GroupBy, got nil")
	}
}

func TestAggregateSalesByDay(t *testing.T) {
	ordersList := []orders.Order{
		orderAt("o1", "2026-01-01", orders.StatusDelivered, nil, 100, item("Кроссовки", 2, 50)),
		orderAt("o2", "2026-01-01", orders.StatusPlaced, nil, 30, item("Ботинки", 1, 30)),
		orderAt("o3", "2026-01-02", orders.StatusDelivered, nil, 70, item("Кроссовки", 1, 70)),
		// cancelled order on 2026-01-01 must be fully excluded
		orderAt("o4", "2026-01-01", orders.StatusCancelled, nil, 999, item("Кроссовки", 5, 999)),
	}

	rows, err := AggregateSales(ordersList, GroupByDay, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 day rows, got %d: %+v", len(rows), rows)
	}

	day1 := rows[0]
	if day1.Key != "2026-01-01" || day1.OrderCount != 2 || day1.ItemCount != 3 || day1.Revenue != 130 {
		t.Fatalf("unexpected day1 row: %+v", day1)
	}
	day2 := rows[1]
	if day2.Key != "2026-01-02" || day2.OrderCount != 1 || day2.ItemCount != 1 || day2.Revenue != 70 {
		t.Fatalf("unexpected day2 row: %+v", day2)
	}
}

func TestAggregateSalesByDaySortedChronologically(t *testing.T) {
	ordersList := []orders.Order{
		orderAt("o1", "2026-03-01", orders.StatusDelivered, nil, 10, item("A", 1, 10)),
		orderAt("o2", "2026-01-01", orders.StatusDelivered, nil, 10, item("A", 1, 10)),
		orderAt("o3", "2026-02-01", orders.StatusDelivered, nil, 10, item("A", 1, 10)),
	}
	rows, err := AggregateSales(ordersList, GroupByDay, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"2026-01-01", "2026-02-01", "2026-03-01"}
	if len(rows) != len(want) {
		t.Fatalf("expected %d rows, got %d", len(want), len(rows))
	}
	for i, k := range want {
		if rows[i].Key != k {
			t.Fatalf("row %d: key = %q, want %q", i, rows[i].Key, k)
		}
	}
}

func TestAggregateSalesByProduct(t *testing.T) {
	ordersList := []orders.Order{
		orderAt("o1", "2026-01-01", orders.StatusDelivered, nil, 130,
			item("Кроссовки", 2, 50), item("Ботинки", 1, 30)),
		orderAt("o2", "2026-01-02", orders.StatusConfirmed, nil, 70,
			item("Кроссовки", 1, 70)),
		// same product, cancelled order -> excluded entirely
		orderAt("o3", "2026-01-03", orders.StatusCancelled, nil, 500,
			item("Кроссовки", 10, 500)),
	}

	rows, err := AggregateSales(ordersList, GroupByProduct, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 product rows, got %d: %+v", len(rows), rows)
	}

	byKey := map[string]Row{}
	for _, r := range rows {
		byKey[r.Key] = r
	}

	sneakers, ok := byKey["Кроссовки"]
	if !ok {
		t.Fatalf("missing Кроссовки row: %+v", rows)
	}
	// 2 distinct orders (o1, o2) contained this product; item count 2+1=3;
	// revenue 2*50 + 1*70 = 170.
	if sneakers.OrderCount != 2 || sneakers.ItemCount != 3 || sneakers.Revenue != 170 {
		t.Fatalf("unexpected Кроссовки row: %+v", sneakers)
	}

	boots, ok := byKey["Ботинки"]
	if !ok {
		t.Fatalf("missing Ботинки row: %+v", rows)
	}
	if boots.OrderCount != 1 || boots.ItemCount != 1 || boots.Revenue != 30 {
		t.Fatalf("unexpected Ботинки row: %+v", boots)
	}
}

func TestAggregateSalesByProductSameProductTwiceInOneOrderCountsOrderOnce(t *testing.T) {
	// A defensive case: if a product's snapshot ever appears as two separate
	// line items within the *same* order (e.g. two different variants of the
	// same product name), that order should still only count once toward
	// order_count for that product key, since order_count means "orders
	// containing this product", not "line items".
	o := orderAt("o1", "2026-01-01", orders.StatusDelivered, nil, 100,
		item("Кроссовки", 1, 50), item("Кроссовки", 1, 50))

	rows, err := AggregateSales([]orders.Order{o}, GroupByProduct, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d: %+v", len(rows), rows)
	}
	if rows[0].OrderCount != 1 || rows[0].ItemCount != 2 || rows[0].Revenue != 100 {
		t.Fatalf("unexpected row: %+v", rows[0])
	}
}

func TestAggregateSalesByPoint(t *testing.T) {
	ordersList := []orders.Order{
		orderAt("o1", "2026-01-01", orders.StatusDelivered, strPtr("p1"), 100, item("A", 1, 100)),
		orderAt("o2", "2026-01-01", orders.StatusDelivered, strPtr("p2"), 50, item("A", 1, 50)),
		orderAt("o3", "2026-01-02", orders.StatusDelivered, strPtr("p1"), 20, item("A", 1, 20)),
		orderAt("o4", "2026-01-02", orders.StatusCancelled, strPtr("p1"), 999, item("A", 9, 999)),
	}
	pointNames := map[string]string{"p1": "Центр", "p2": "Восток"}

	rows, err := AggregateSales(ordersList, GroupByPoint, pointNames)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	byKey := map[string]Row{}
	for _, r := range rows {
		byKey[r.Key] = r
	}

	center, ok := byKey["Центр"]
	if !ok {
		t.Fatalf("missing Центр row: %+v", rows)
	}
	if center.OrderCount != 2 || center.ItemCount != 2 || center.Revenue != 120 {
		t.Fatalf("unexpected Центр row: %+v", center)
	}

	east, ok := byKey["Восток"]
	if !ok {
		t.Fatalf("missing Восток row: %+v", rows)
	}
	if east.OrderCount != 1 || east.ItemCount != 1 || east.Revenue != 50 {
		t.Fatalf("unexpected Восток row: %+v", east)
	}
}

func TestAggregateSalesByPointUnknownNameFallsBackToID(t *testing.T) {
	ordersList := []orders.Order{
		orderAt("o1", "2026-01-01", orders.StatusDelivered, strPtr("p-missing"), 10, item("A", 1, 10)),
	}
	rows, err := AggregateSales(ordersList, GroupByPoint, map[string]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 || rows[0].Key != "p-missing" {
		t.Fatalf("expected fallback to raw point id, got %+v", rows)
	}
}

func TestAggregateSalesByPointNilPointIDGroupedAsUnknown(t *testing.T) {
	ordersList := []orders.Order{
		orderAt("o1", "2026-01-01", orders.StatusDelivered, nil, 10, item("A", 1, 10)),
	}
	rows, err := AggregateSales(ordersList, GroupByPoint, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 || rows[0].Key != "unknown" {
		t.Fatalf("expected \"unknown\" key for a nil point id, got %+v", rows)
	}
}

func TestAggregateSalesAllCancelledYieldsEmptyReport(t *testing.T) {
	ordersList := []orders.Order{
		orderAt("o1", "2026-01-01", orders.StatusCancelled, strPtr("p1"), 100, item("A", 1, 100)),
	}
	for _, gb := range []GroupBy{GroupByDay, GroupByProduct, GroupByPoint} {
		t.Run(string(gb), func(t *testing.T) {
			rows, err := AggregateSales(ordersList, gb, map[string]string{"p1": "Центр"})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(rows) != 0 {
				t.Fatalf("expected zero rows when every order is cancelled, got %+v", rows)
			}
		})
	}
}

func TestAggregateSalesRevenueRoundingUnaffected(t *testing.T) {
	// AggregateSales sums whatever TotalAmount/Price values it's given; it
	// doesn't itself round. This just documents that expectation so a future
	// change doesn't silently introduce double-rounding.
	ordersList := []orders.Order{
		orderAt("o1", "2026-01-01", orders.StatusDelivered, nil, 10.10, item("A", 1, 10.10)),
		orderAt("o2", "2026-01-01", orders.StatusDelivered, nil, 10.20, item("A", 1, 10.20)),
	}
	rows, err := AggregateSales(ordersList, GroupByDay, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %+v", rows)
	}
	const want = 20.30
	if diff := rows[0].Revenue - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("Revenue = %v, want %v", rows[0].Revenue, want)
	}
}
