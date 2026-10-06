//go:build integration

package integration

import (
	"testing"

	"github.com/google/uuid"
)

// product_sales (migration 000042) must always equal the aggregate that
// sort=popular used to compute per request: SUM(order_items.quantity) over
// non-cancelled orders, per product — through item inserts/updates/deletes,
// status changes into and out of 'cancelled', order deletes and a variant
// moving to another product.
func TestProductSalesTracksOrders(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	f := newFixture(t, 10, 10)

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := testDB.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%v\nquery: %s", err, q)
		}
	}
	scan := func(dst any, q string, args ...any) {
		t.Helper()
		if err := testDB.QueryRowContext(ctx, q, args...).Scan(dst); err != nil {
			t.Fatalf("%v\nquery: %s", err, q)
		}
	}
	var otherProduct string
	scan(&otherProduct, `INSERT INTO products (category_id, name_ru, name_ky, base_price)
		VALUES ($1, 'Другой', 'Другой', 100) RETURNING id`, f.CategoryID)

	check := func(step string) {
		t.Helper()
		for _, p := range []string{f.ProductID, otherProduct} {
			var stored, want int64
			scan(&stored, `SELECT COALESCE((SELECT units_sold FROM product_sales WHERE product_id = $1), 0)`, p)
			scan(&want, `SELECT COALESCE(SUM(oi.quantity), 0)
				FROM order_items oi
				JOIN product_variants pv ON pv.id = oi.variant_id
				JOIN orders o ON o.id = oi.order_id
				WHERE pv.product_id = $1 AND o.status <> 'cancelled'`, p)
			if stored != want {
				t.Fatalf("%s: product %s units_sold = %d, aggregate = %d", step, p, stored, want)
			}
		}
	}
	newOrder := func(status string) string {
		t.Helper()
		var id string
		scan(&id, `INSERT INTO orders (order_number, customer_id, address_id, status, payment_method, total_amount)
			VALUES ($1, $2, $3, $4, 'cash_on_delivery', 1000) RETURNING id`,
			"IT-PS-"+uuid.NewString()[:12], f.CustomerID, f.AddressID, status)
		return id
	}
	addItem := func(orderID, variantID string, qty int) string {
		t.Helper()
		var id string
		scan(&id, `INSERT INTO order_items (order_id, variant_id, product_name_snapshot, size_snapshot, color_snapshot, quantity, price)
			VALUES ($1, $2, 'x', '40', 'black', $3, 1000) RETURNING id`, orderID, variantID, qty)
		return id
	}

	placed := newOrder("placed")
	itemA := addItem(placed, f.VariantA, 2)
	addItem(placed, f.VariantB, 3)
	cancelled := newOrder("cancelled")
	addItem(cancelled, f.VariantA, 40)
	check("inserts")

	exec(`UPDATE order_items SET quantity = 5 WHERE id = $1`, itemA)
	check("quantity update")

	exec(`UPDATE orders SET status = 'cancelled' WHERE id = $1`, placed)
	check("cancel")
	exec(`UPDATE orders SET status = 'confirmed' WHERE id = $1`, placed)
	check("un-cancel")
	exec(`UPDATE orders SET status = 'delivered' WHERE id = $1`, placed)
	check("non-cancel transition")

	exec(`UPDATE order_items SET order_id = $1 WHERE id = $2`, cancelled, itemA)
	check("item moved to a cancelled order")

	exec(`UPDATE product_variants SET product_id = $1 WHERE id = $2`, otherProduct, f.VariantB)
	check("variant moved to another product")

	exec(`DELETE FROM order_items WHERE order_id = $1 AND variant_id = $2`, placed, f.VariantB)
	check("item delete")

	again := newOrder("placed")
	addItem(again, f.VariantA, 7)
	exec(`DELETE FROM orders WHERE id = $1`, again)
	check("order delete (cascade)")
	exec(`DELETE FROM orders WHERE id = $1`, cancelled)
	check("cancelled order delete")
}
