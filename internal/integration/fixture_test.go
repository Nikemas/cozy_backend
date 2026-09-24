//go:build integration

package integration

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fixture is a small, self-contained catalog: one category, one product
// (base price 2500) with two variants (the second with price_override
// 3000), two active points of sale and one customer with one address.
// Every name is unique per fixture, so tests don't see each other's rows.
type fixture struct {
	CategoryID string
	ProductID  string
	VariantA   string // size 40, base price 2500
	VariantB   string // size 41, price_override 3000
	PointA     string
	PointB     string
	CustomerID string
	AddressID  string
}

var phoneSeq atomic.Int64

func init() {
	// Seed from the clock so reruns against a reused database (never the
	// case in CI, which uses a fresh one) don't collide on customers.phone.
	phoneSeq.Store(time.Now().UnixNano() % 1_000_000)
}

func uniquePhone() string {
	return fmt.Sprintf("+996700%06d", phoneSeq.Add(1)%1_000_000)
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// newFixture inserts the catalog/customer rows with plain SQL (not via
// internal/catalog, which other work streams are changing) and stocks
// qtyA/qtyB of VariantA/VariantB at PointA only. PointB has no stock rows.
func newFixture(t *testing.T, qtyA, qtyB int) fixture {
	t.Helper()
	ctx := ctxT(t)
	tag := uuid.NewString()[:8]
	var f fixture

	mustScan := func(dst *string, q string, args ...any) {
		t.Helper()
		if err := testDB.QueryRowContext(ctx, q, args...).Scan(dst); err != nil {
			t.Fatalf("fixture: %v\nquery: %s", err, q)
		}
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := testDB.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("fixture: %v\nquery: %s", err, q)
		}
	}

	mustScan(&f.CategoryID,
		`INSERT INTO categories (name_ru, name_ky, slug) VALUES ($1, $1, $2) RETURNING id`,
		"Кроссовки "+tag, "it-sneakers-"+tag)
	mustScan(&f.ProductID,
		`INSERT INTO products (category_id, name_ru, name_ky, brand, base_price)
		 VALUES ($1, $2, $2, 'Cozy', 2500) RETURNING id`,
		f.CategoryID, "Кроссовки IT "+tag)
	mustScan(&f.VariantA,
		`INSERT INTO product_variants (product_id, size, color, sku) VALUES ($1, '40', 'black', $2) RETURNING id`,
		f.ProductID, "IT-"+tag+"-40")
	mustScan(&f.VariantB,
		`INSERT INTO product_variants (product_id, size, color, sku, price_override)
		 VALUES ($1, '41', 'black', $2, 3000) RETURNING id`,
		f.ProductID, "IT-"+tag+"-41")
	mustScan(&f.PointA,
		`INSERT INTO points_of_sale (name, address) VALUES ($1, 'ул. Тестовая, 1') RETURNING id`,
		"Точка A "+tag)
	mustScan(&f.PointB,
		`INSERT INTO points_of_sale (name, address) VALUES ($1, 'ул. Тестовая, 2') RETURNING id`,
		"Точка B "+tag)
	mustExec(`INSERT INTO stock (variant_id, point_id, quantity) VALUES ($1, $3, $4), ($2, $3, $5)`,
		f.VariantA, f.VariantB, f.PointA, qtyA, qtyB)
	mustScan(&f.CustomerID,
		`INSERT INTO customers (phone, name) VALUES ($1, 'Тест') RETURNING id`, uniquePhone())
	mustScan(&f.AddressID,
		`INSERT INTO customer_addresses (customer_id, label, address_text, is_default)
		 VALUES ($1, 'Дом', 'г. Бишкек, ул. Тестовая, 10', true) RETURNING id`, f.CustomerID)
	return f
}

// newCustomer inserts another customer (for ownership checks).
func newCustomer(t *testing.T) string {
	t.Helper()
	var id string
	err := testDB.QueryRowContext(ctxT(t),
		`INSERT INTO customers (phone) VALUES ($1) RETURNING id`, uniquePhone()).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// stockQty is the current stock of variantID at pointID (0 if no row).
func stockQty(t *testing.T, variantID, pointID string) int {
	t.Helper()
	var q int
	err := testDB.QueryRowContext(ctxT(t),
		`SELECT COALESCE((SELECT quantity FROM stock WHERE variant_id = $1 AND point_id = $2), 0)`,
		variantID, pointID).Scan(&q)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func strptr(s string) *string { return &s }
