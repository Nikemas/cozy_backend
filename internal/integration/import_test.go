//go:build integration

package integration

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"

	"github.com/Nikemas/cozy_backend/internal/catalog"
)

// importSheet builds an .xlsx price list: header + rows.
func importSheet(t *testing.T, rows [][]any) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	sheet := f.GetSheetName(0)
	for i, row := range rows {
		cell, _ := excelize.CoordinatesToCellName(1, i+1)
		if err := f.SetSheetRow(sheet, cell, &row); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestCatalogImportIsIdempotent(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	tag := uuid.NewString()[:8]

	var catID, pointID string
	if err := testDB.QueryRowContext(ctx,
		`INSERT INTO categories (name_ru, name_ky, slug) VALUES ($1, $1, $2) RETURNING id`,
		"Импорт "+tag, "it-import-"+tag).Scan(&catID); err != nil {
		t.Fatal(err)
	}
	if err := testDB.QueryRowContext(ctx,
		`INSERT INTO points_of_sale (name, address) VALUES ($1, 'ул. Импорт, 1') RETURNING id`,
		"Точка импорта "+tag).Scan(&pointID); err != nil {
		t.Fatal(err)
	}

	art := "IT-" + tag
	sku := func(size string) string { return art + "-" + size }
	header := []any{"Артикул", "Название*", "Категория*", "Бренд", "Цена*", "Размер", "Цвет", "SKU", "Остаток"}
	file := importSheet(t, [][]any{
		header,
		{art, "Кроссовки " + tag, "Импорт " + tag, "Cozy", 4990, "39", "Белый", sku("39"), 5},
		{art, "", "", "", "", "40", "Белый", sku("40"), 3},
		{art, "", "", "", 5290, "41", "Белый", sku("41"), 0},
	})
	store := catalog.NewSQLImportStore(testDB)
	opts := catalog.ImportOptions{PointID: pointID}

	count := func() (products, variants int) {
		t.Helper()
		if err := testDB.QueryRowContext(ctx,
			`SELECT count(*) FROM products WHERE category_id = $1`, catID).Scan(&products); err != nil {
			t.Fatal(err)
		}
		if err := testDB.QueryRowContext(ctx, `
			SELECT count(*) FROM product_variants v JOIN products p ON p.id = v.product_id
			WHERE p.category_id = $1`, catID).Scan(&variants); err != nil {
			t.Fatal(err)
		}
		return
	}
	stockOf := func(s string) int {
		t.Helper()
		var q int
		if err := testDB.QueryRowContext(ctx, `
			SELECT COALESCE((SELECT st.quantity FROM stock st JOIN product_variants v ON v.id = st.variant_id
			                 WHERE v.sku = $1 AND st.point_id = $2), -1)`, s, pointID).Scan(&q); err != nil {
			t.Fatal(err)
		}
		return q
	}

	// Dry run: full report, nothing written.
	dry, err := catalog.ImportProducts(ctx, bytes.NewReader(file), catalog.ImportFormatXLSX, store,
		catalog.ImportOptions{PointID: pointID, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if dry.Summary.ProductsCreated != 1 || dry.Summary.VariantsCreated != 3 || dry.Summary.Errors != 0 {
		t.Fatalf("dry run summary = %+v rows=%+v", dry.Summary, dry.Rows)
	}
	if p, v := count(); p != 0 || v != 0 {
		t.Fatalf("dry run wrote %d products, %d variants", p, v)
	}

	// First import: one product, three variants, stock at the point.
	first, err := catalog.ImportProducts(ctx, bytes.NewReader(file), catalog.ImportFormatXLSX, store, opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.Summary != dry.Summary {
		t.Errorf("real summary %+v != dry %+v", first.Summary, dry.Summary)
	}
	if p, v := count(); p != 1 || v != 3 {
		t.Fatalf("after first import: %d products, %d variants, want 1 and 3", p, v)
	}
	if q := stockOf(sku("39")); q != 5 {
		t.Errorf("stock 39 = %d, want 5", q)
	}
	var override *float64
	var modelCode string
	if err := testDB.QueryRowContext(ctx, `
		SELECT v.price_override, p.model_code FROM product_variants v JOIN products p ON p.id = v.product_id
		WHERE v.sku = $1`, sku("41")).Scan(&override, &modelCode); err != nil {
		t.Fatal(err)
	}
	if override == nil || *override != 5290 || modelCode != art {
		t.Errorf("41: override=%v model_code=%q", override, modelCode)
	}

	// Same file again: everything updated, no duplicates.
	second, err := catalog.ImportProducts(ctx, bytes.NewReader(file), catalog.ImportFormatXLSX, store, opts)
	if err != nil {
		t.Fatal(err)
	}
	if second.Summary.ProductsUpdated != 1 || second.Summary.VariantsUpdated != 3 ||
		second.Summary.ProductsCreated != 0 || second.Summary.VariantsCreated != 0 || second.Summary.Errors != 0 {
		t.Fatalf("second import summary = %+v rows=%+v", second.Summary, second.Rows)
	}
	if p, v := count(); p != 1 || v != 3 {
		t.Fatalf("after re-import: %d products, %d variants, want 1 and 3", p, v)
	}

	// Changed price list: new price/stock, a new size; the article is gone
	// from the file, so the product is found by SKU.
	changed := importSheet(t, [][]any{
		{"Название", "Категория", "Цена", "Размер", "Цвет", "SKU", "Остаток"},
		{"Кроссовки " + tag, "it-import-" + tag, "4500", "39", "белый", sku("39"), "7"},
		{"Кроссовки " + tag, "it-import-" + tag, "4500", "42", "Белый", sku("42"), "1"},
	})
	third, err := catalog.ImportProducts(ctx, bytes.NewReader(changed), catalog.ImportFormatXLSX, store, opts)
	if err != nil {
		t.Fatal(err)
	}
	if third.Summary.ProductsUpdated != 1 || third.Summary.VariantsUpdated != 1 || third.Summary.VariantsCreated != 1 {
		t.Fatalf("third import summary = %+v rows=%+v", third.Summary, third.Rows)
	}
	if p, v := count(); p != 1 || v != 4 {
		t.Fatalf("after changed import: %d products, %d variants, want 1 and 4", p, v)
	}
	if q := stockOf(sku("39")); q != 7 {
		t.Errorf("stock 39 = %d, want 7", q)
	}
	var base float64
	if err := testDB.QueryRowContext(ctx,
		`SELECT base_price, model_code FROM products WHERE category_id = $1`, catID).Scan(&base, &modelCode); err != nil {
		t.Fatal(err)
	}
	if base != 4500 || modelCode != art {
		t.Errorf("product base=%v model_code=%q, want 4500 and kept article", base, modelCode)
	}
}

func TestCatalogImportModelIsAtomic(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	f := newFixture(t, 1, 1) // its variants own SKUs IT-<tag>-40/41
	tag := uuid.NewString()[:8]

	var existingSKU string
	if err := testDB.QueryRowContext(ctx, `SELECT sku FROM product_variants WHERE id = $1`, f.VariantA).Scan(&existingSKU); err != nil {
		t.Fatal(err)
	}
	var catSlug string
	if err := testDB.QueryRowContext(ctx, `SELECT slug FROM categories WHERE id = $1`, f.CategoryID).Scan(&catSlug); err != nil {
		t.Fatal(err)
	}

	store := catalog.NewSQLImportStore(testDB)
	opts := catalog.ImportOptions{PointID: f.PointA}
	art := "M-" + tag
	first := importSheet(t, [][]any{
		{"Артикул", "Название", "Категория", "Цена", "Размер", "Цвет", "SKU"},
		{art, "Модель " + tag, catSlug, 1000, "38", "red", art + "-38"},
	})
	if res, err := catalog.ImportProducts(ctx, bytes.NewReader(first), catalog.ImportFormatXLSX, store, opts); err != nil || res.Summary.ProductsCreated != 1 {
		t.Fatalf("first import: %v %+v", err, res)
	}

	// Second file: the product (found by article) gets a new price and a
	// new size — then the third row claims the fixture product's SKU. The
	// model fails after writes were made; all of them must roll back.
	second := importSheet(t, [][]any{
		{"Артикул", "Название", "Категория", "Цена", "Размер", "Цвет", "SKU"},
		{art, "Модель " + tag, catSlug, 2000, "38", "red", art + "-38"},
		{art, "", "", "", "39", "red", art + "-39"},
		{art, "", "", "", "40", "red", existingSKU},
	})
	res, err := catalog.ImportProducts(ctx, bytes.NewReader(second), catalog.ImportFormatXLSX, store, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Errors != 1 || res.Summary.Skipped != 2 || res.Summary.ProductsUpdated != 0 {
		t.Fatalf("summary = %+v rows=%+v", res.Summary, res.Rows)
	}
	if !strings.Contains(res.Rows[2].Message, "другого товара") {
		t.Errorf("row 4 message = %q", res.Rows[2].Message)
	}
	var base float64
	var variants int
	if err := testDB.QueryRowContext(ctx, `
		SELECT p.base_price, (SELECT count(*) FROM product_variants v WHERE v.product_id = p.id)
		FROM products p WHERE p.model_code = $1`, art).Scan(&base, &variants); err != nil {
		t.Fatal(err)
	}
	if base != 1000 || variants != 1 {
		t.Errorf("after failed model: base=%v variants=%d, want 1000 and 1 (rolled back)", base, variants)
	}
}
