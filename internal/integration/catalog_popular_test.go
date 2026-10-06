//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Nikemas/cozy_backend/internal/catalog"
)

// sort=popular orders products by units sold across non-cancelled orders,
// newest first among equals (including the never-sold ones).
func TestListSortPopular(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	f := newFixture(t, 10, 10) // f.ProductID: created_at = now()

	mustScan := func(q string, args ...any) string {
		t.Helper()
		var id string
		if err := testDB.QueryRowContext(ctx, q, args...).Scan(&id); err != nil {
			t.Fatalf("%v\nquery: %s", err, q)
		}
		return id
	}
	newProduct := func(name string, created time.Time) (productID, variantID string) {
		t.Helper()
		productID = mustScan(`INSERT INTO products (category_id, name_ru, name_ky, base_price, created_at)
			VALUES ($1, $2, $2, 1000, $3) RETURNING id`, f.CategoryID, name, created)
		variantID = mustScan(`INSERT INTO product_variants (product_id, size, color, sku)
			VALUES ($1, '40', 'white', $2) RETURNING id`, productID, "IT-POP-"+uuid.NewString()[:8])
		return productID, variantID
	}
	order := func(status string, items map[string]int) {
		t.Helper()
		orderID := mustScan(`INSERT INTO orders (order_number, customer_id, address_id, status, payment_method, total_amount)
			VALUES ($1, $2, $3, $4, 'cash_on_delivery', 1000) RETURNING id`,
			"IT-POP-"+uuid.NewString()[:12], f.CustomerID, f.AddressID, status)
		for variantID, qty := range items {
			if _, err := testDB.ExecContext(ctx, `INSERT INTO order_items
				(order_id, variant_id, product_name_snapshot, size_snapshot, color_snapshot, quantity, price)
				VALUES ($1, $2, 'x', '40', 'white', $3, 1000)`, orderID, variantID, qty); err != nil {
				t.Fatal(err)
			}
		}
	}

	now := time.Now()
	hit, hitVar := newProduct("Хит", now.Add(-3*time.Hour))
	mid, midVar := newProduct("Средний", now.Add(-2*time.Hour))
	cancelledOnly, cancelledVar := newProduct("Только отменённые", now.Add(-1*time.Hour))
	fresh, _ := newProduct("Новинка без продаж", now.Add(time.Hour))

	// Fixture product: 1 unit of each variant = 2, summed per product.
	order("delivered", map[string]int{f.VariantA: 1, f.VariantB: 1})
	order("placed", map[string]int{hitVar: 3, midVar: 1})
	order("confirmed", map[string]int{hitVar: 2})
	order("courier_assigned", map[string]int{midVar: 1})
	order("cancelled", map[string]int{cancelledVar: 50, midVar: 50})

	products, total, err := catalog.NewProductRepo(testDB).List(ctx, catalog.ListFilter{
		CategoryID: f.CategoryID, Sort: catalog.SortPopular,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// hit 5; fixture 2 and mid 2 (tie: the fixture product is newer);
	// then the unsold ones newest first — cancelled orders don't count.
	want := []string{hit, f.ProductID, mid, fresh, cancelledOnly}
	if total != len(want) || len(products) != len(want) {
		t.Fatalf("total = %d, len = %d, want %d", total, len(products), len(want))
	}
	for i, p := range products {
		if p.ID != want[i] {
			t.Errorf("position %d = %s (%s), want %s", i, p.ID, p.NameRu, want[i])
		}
	}
}
