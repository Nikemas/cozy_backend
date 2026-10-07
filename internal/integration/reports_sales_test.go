//go:build integration

package integration

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Nikemas/cozy_backend/internal/reports"
)

// reports.Repo.Sales (aggregated in SQL) must return exactly what the
// reference AggregateSales computes over LoadOrders for every grouping:
// non-sales (cancelled, unpaid online) excluded, Bishkek day boundaries,
// points sharing a name merged, an order without a point under "unknown".
func TestSalesSQLMatchesAggregateSales(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	f := newFixture(t, 10, 10)
	repo := reports.NewRepo(testDB)

	// A private window far in the past, so parallel tests' orders don't
	// land in it. 18:30 UTC is already the next day in Bishkek (UTC+6).
	base := time.Date(2001, 3, 10, 18, 30, 0, 0, time.UTC)
	var pointC string
	if err := testDB.QueryRowContext(ctx, `INSERT INTO points_of_sale (name, address) VALUES ($1, 'x') RETURNING id`,
		"Точка A dup "+uuid.NewString()[:6]).Scan(&pointC); err != nil {
		t.Fatal(err)
	}

	type item struct {
		variant string
		qty     int
		price   float64
		name    string
	}
	order := func(at time.Time, status, method string, payment *string, point *string, total float64, items ...item) {
		t.Helper()
		var id string
		err := testDB.QueryRowContext(ctx, `INSERT INTO orders (order_number, customer_id, address_id, point_id, status,
				payment_method, payment_status, total_amount, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
			"IT-RS-"+uuid.NewString()[:12], f.CustomerID, f.AddressID, point, status, method, payment, total, at).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range items {
			if _, err := testDB.ExecContext(ctx, `INSERT INTO order_items
				(order_id, variant_id, product_name_snapshot, size_snapshot, color_snapshot, quantity, price)
				VALUES ($1, $2, $3, '40', 'black', $4, $5)`, id, it.variant, it.name, it.qty, it.price); err != nil {
				t.Fatal(err)
			}
		}
	}
	paid, pending := "paid", "pending"
	a, c := f.PointA, pointC

	order(base, "delivered", "cash_on_delivery", nil, &a, 5200.10,
		item{f.VariantA, 2, 2500.05, "Кроссовки"}, item{f.VariantB, 1, 200, "Шнурки"})
	order(base.Add(2*time.Hour), "placed", "online_card", &paid, &c, 3000, item{f.VariantB, 1, 3000, "Кроссовки"})
	order(base.Add(3*time.Hour), "placed", "online_card", &pending, &a, 999, item{f.VariantA, 9, 111, "Кроссовки"})
	order(base.Add(26*time.Hour), "cancelled", "cash_on_delivery", nil, &a, 777, item{f.VariantA, 7, 111, "Кроссовки"})
	order(base.Add(27*time.Hour), "confirmed", "cash_on_delivery", nil, nil, 0.3, item{f.VariantA, 1, 0.1, "Ботинки"},
		item{f.VariantB, 2, 0.1, "Ботинки"})
	order(base.Add(28*time.Hour), "confirmed", "cash_on_delivery", nil, &a, 50) // no items

	from, to := base.Add(-48*time.Hour), base.Add(72*time.Hour)
	list, err := repo.LoadOrders(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	// Both points are named the same in the display map: their rows merge.
	names := map[string]string{a: "Склад", c: "Склад"}

	for _, g := range []reports.GroupBy{reports.GroupByDay, reports.GroupByProduct, reports.GroupByPoint} {
		want, err := reports.AggregateSales(list, g, names)
		if err != nil {
			t.Fatal(err)
		}
		got, err := repo.Sales(ctx, from, to, g, names)
		if err != nil {
			t.Fatalf("Sales(%s): %v", g, err)
		}
		if len(got) != len(want) || len(want) == 0 {
			t.Fatalf("%s: got %+v, want %+v", g, got, want)
		}
		for i := range want {
			w, r := want[i], got[i]
			if r.Key != w.Key || r.OrderCount != w.OrderCount || r.ItemCount != w.ItemCount ||
				math.Abs(r.Revenue-w.Revenue) > 1e-6 {
				t.Errorf("%s row %d: got %+v, want %+v", g, i, r, w)
			}
		}
	}

	// The category breakdown (grouped under COLLATE "C", ordered in the
	// database collation) over the same window: one fixture category,
	// sale orders only — 3 orders, 7 items, 2×2500.05+200+3000+0.1+2×0.1.
	cats, err := repo.CategorySales(ctx, from, to)
	if err != nil {
		t.Fatalf("CategorySales: %v", err)
	}
	if len(cats) != 1 || cats[0].OrderCount != 3 || cats[0].ItemCount != 7 || math.Abs(cats[0].Revenue-8200.4) > 1e-6 {
		t.Errorf("CategorySales = %+v, want one row 3 orders / 7 items / 8200.40", cats)
	}

	// Brands (moved from the admin package): same sale lines, so the rows
	// add up to the category total.
	brands, err := repo.BrandSales(ctx, from, to)
	if err != nil {
		t.Fatalf("BrandSales: %v", err)
	}
	var brandRevenue float64
	for _, b := range brands {
		brandRevenue += b.Revenue
	}
	if len(brands) == 0 || math.Abs(brandRevenue-8200.4) > 1e-6 {
		t.Errorf("BrandSales = %+v, want revenue summing to 8200.40", brands)
	}

	// The cached layer serves exactly the repo's rows, first load and hit.
	cached := reports.NewCachedRepo(repo, reports.CacheConfig{})
	for pass := range 2 {
		got, err := cached.Sales(ctx, from, to, reports.GroupByPoint, names)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := repo.Sales(ctx, from, to, reports.GroupByPoint, names)
		if !slices.Equal(got, want) {
			t.Errorf("pass %d: cached Sales = %+v, want %+v", pass, got, want)
		}
		gotBrands, err := cached.BrandSales(ctx, from, to)
		if err != nil || !slices.Equal(gotBrands, brands) {
			t.Errorf("pass %d: cached BrandSales = %+v (%v), want %+v", pass, gotBrands, err, brands)
		}
		gotCats, err := cached.CategorySales(ctx, from, to)
		if err != nil || !slices.Equal(gotCats, cats) {
			t.Errorf("pass %d: cached CategorySales = %+v (%v), want %+v", pass, gotCats, err, cats)
		}
	}
}
