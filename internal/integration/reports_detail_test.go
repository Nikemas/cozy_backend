//go:build integration

package integration

import (
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Nikemas/cozy_backend/internal/reports"
)

// Detail reports follow the summary's sale rules: sold = sale orders only
// (cancelled and unpaid online excluded), cancelled units reported
// apart, a subcategory's products in its parent's report, every variant
// of the product listed, sales split by the order's point, stock current.
func TestDetailReportsSQL(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	f := newFixture(t, 4, 6)
	repo := reports.NewRepo(testDB)
	tag := uuid.NewString()[:8]
	brand := "Brand-" + tag

	if _, err := testDB.ExecContext(ctx, `UPDATE products SET brand = $1 WHERE id = $2`, brand, f.ProductID); err != nil {
		t.Fatal(err)
	}
	// A subcategory with a second product (same brand, never sold) and an
	// inactive unsold product, which must not be listed.
	var sub, p2, code1 string
	mustScan := func(dst *string, q string, args ...any) {
		t.Helper()
		if err := testDB.QueryRowContext(ctx, q, args...).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	mustScan(&sub, `INSERT INTO categories (parent_id, name_ru, name_ky, slug) VALUES ($1, 'Под', 'Под', $2) RETURNING id`, f.CategoryID, "it-sub-"+tag)
	mustScan(&p2, `INSERT INTO products (category_id, name_ru, name_ky, brand, base_price) VALUES ($1, 'Второй', 'Второй', $2, 100) RETURNING id`, sub, brand)
	var inactive string
	mustScan(&inactive, `INSERT INTO products (category_id, name_ru, name_ky, brand, base_price, is_active) VALUES ($1, 'Скрытый', 'Скрытый', $2, 100, false) RETURNING id`, sub, brand)
	mustScan(&code1, `SELECT model_code FROM products WHERE id = $1`, f.ProductID)

	base := time.Date(2002, 5, 10, 8, 0, 0, 0, time.UTC)
	order := func(status, method string, payment, point *string, variant string, qty int, price float64) {
		t.Helper()
		var id string
		mustScan(&id, `INSERT INTO orders (order_number, customer_id, address_id, point_id, status, payment_method, payment_status, total_amount, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
			"IT-RD-"+uuid.NewString()[:12], f.CustomerID, f.AddressID, point, status, method, payment, float64(qty)*price, base)
		if _, err := testDB.ExecContext(ctx, `INSERT INTO order_items (order_id, variant_id, product_name_snapshot, size_snapshot, color_snapshot, quantity, price)
			VALUES ($1, $2, 'x', '40', 'black', $3, $4)`, id, variant, qty, price); err != nil {
			t.Fatal(err)
		}
	}
	paid, pending := "paid", "pending"
	a, b := f.PointA, f.PointB
	order("delivered", "cash_on_delivery", nil, &a, f.VariantA, 2, 2500)
	order("placed", "online_card", &paid, &b, f.VariantB, 1, 3000)
	order("placed", "online_card", &pending, &a, f.VariantA, 5, 2500) // not a sale yet
	order("cancelled", "cash_on_delivery", nil, &a, f.VariantB, 3, 3000)
	order("confirmed", "cash_on_delivery", nil, nil, f.VariantA, 1, 2400)

	from, to := base.Add(-time.Hour), base.Add(time.Hour)

	check := func(label string, rows []reports.ProductSalesRow, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if len(rows) != 2 {
			t.Fatalf("%s: rows = %+v, want sold product + active unsold one", label, rows)
		}
		r := rows[0]
		if r.ProductID != f.ProductID || r.Code != code1 || r.Brand != brand || r.ItemCount != 4 || r.OrderCount != 3 ||
			math.Abs(r.Revenue-10400) > 1e-6 || r.CancelledCount != 3 {
			t.Errorf("%s: first row = %+v, want 4 sold / 3 orders / 10400 / 3 cancelled", label, r)
		}
		if rows[1].ProductID != p2 || rows[1].ItemCount != 0 || rows[1].Code == "" {
			t.Errorf("%s: second row = %+v", label, rows[1])
		}
	}
	cat, err := repo.CategoryProductSales(ctx, f.CategoryID, from, to)
	check("category", cat, err)
	br, err := repo.BrandProductSales(ctx, " "+brand+" ", from, to)
	check("brand", br, err)

	// Outside the period: nothing sold, the active products still listed.
	empty, err := repo.CategoryProductSales(ctx, sub, base.Add(time.Hour), base.Add(2*time.Hour))
	if err != nil || len(empty) != 1 || empty[0].ProductID != p2 {
		t.Errorf("subcategory, empty period = %+v, %v", empty, err)
	}

	// Summary CategorySales carries the category id.
	cats, err := repo.CategorySales(ctx, from, to)
	if err != nil || len(cats) != 1 || cats[0].ID != f.CategoryID {
		t.Errorf("CategorySales = %+v, %v", cats, err)
	}

	ref, err := repo.ProductByCode(ctx, "  "+code1+" ")
	if err != nil || ref == nil || ref.ID != f.ProductID {
		t.Fatalf("ProductByCode = %+v, %v", ref, err)
	}
	if none, err := repo.ProductByCode(ctx, "NO-SUCH-"+tag); err != nil || none != nil {
		t.Fatalf("ProductByCode(missing) = %+v, %v", none, err)
	}

	names := map[string]string{a: "Точка A", b: "Точка B"}
	sales, err := repo.ProductCodeSales(ctx, f.ProductID, from, to, names)
	if err != nil {
		t.Fatal(err)
	}
	if len(sales.Variants) != 2 {
		t.Fatalf("variants = %+v", sales.Variants)
	}
	va, vb := sales.Variants[0], sales.Variants[1] // sizes 40, 41
	if va.ItemCount != 3 || math.Abs(va.Revenue-7400) > 1e-6 || va.CancelledCount != 0 ||
		vb.ItemCount != 1 || math.Abs(vb.Revenue-3000) > 1e-6 || vb.CancelledCount != 3 {
		t.Errorf("variants = %+v", sales.Variants)
	}
	byKey := map[string]reports.Row{}
	for _, r := range sales.Points {
		byKey[r.Key] = r
	}
	if byKey["Точка A"].ItemCount != 2 || byKey["Точка B"].ItemCount != 1 || byKey[reports.UnknownPointKey].ItemCount != 1 || len(byKey) != 3 {
		t.Errorf("points = %+v", sales.Points)
	}

	stock, err := repo.ProductStock(ctx, f.ProductID)
	if err != nil || len(stock) != 2 {
		t.Fatalf("stock = %+v, %v", stock, err)
	}
	for _, c := range stock {
		if (c.VariantID == f.VariantA && c.Qty != 4) || (c.VariantID == f.VariantB && c.Qty != 6) || c.PointID != a {
			t.Errorf("stock cell = %+v", c)
		}
	}
}
