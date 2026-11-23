//go:build integration

package integration

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/catalog"
)

const productCodeMigration = 51

// migrateUpTo applies the up migrations with version < below.
func migrateUpTo(ctx context.Context, db *sql.DB, below int) error {
	files, err := migrationFiles("up")
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.version >= below {
			break
		}
		if err := execMigrationFile(ctx, db, f.path); err != nil {
			return err
		}
	}
	return nil
}

func execMigrationFile(ctx context.Context, db *sql.DB, path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, string(body)); err != nil {
		return errors.New(filepath.Base(path) + ": " + err.Error())
	}
	return nil
}

// Migration 000051 backfills a code for every product without one (in
// creation order, skipping CZ-numbers an imported article already took),
// keeps existing articles, and its down returns generated codes to NULL.
func TestProductCodeMigrationBackfill(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, drop, err := createTempDB(ctx, os.Getenv("TEST_DATABASE_URL"), "pcode")
	if err != nil {
		t.Fatal(err)
	}
	defer drop()
	defer func() { _ = db.Close() }()

	if err := migrateUpTo(ctx, db, productCodeMigration); err != nil {
		t.Fatal(err)
	}
	var cat string
	if err := db.QueryRowContext(ctx, `INSERT INTO categories (name_ru, name_ky, slug) VALUES ('К', 'К', 'k') RETURNING id`).Scan(&cat); err != nil {
		t.Fatal(err)
	}
	insert := func(name string, code any, age time.Duration) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `INSERT INTO products (category_id, name_ru, name_ky, base_price, model_code, created_at)
			VALUES ($1, $2, $2, 100, $3, now() - $4::interval)`, cat, name, code, age.String()); err != nil {
			t.Fatal(err)
		}
	}
	insert("A", nil, 4*time.Hour)
	insert("B", "cz-00002", 3*time.Hour) // an import article in the generated range
	insert("C", "  ", 2*time.Hour)       // blank counts as none
	insert("D", "ART-1", time.Hour)

	files, _ := migrationFiles("up")
	var up, down string
	for _, f := range files {
		if f.version == productCodeMigration {
			up = f.path
		}
	}
	downs, _ := migrationFiles("down")
	for _, f := range downs {
		if f.version == productCodeMigration {
			down = f.path
		}
	}
	if err := execMigrationFile(ctx, db, up); err != nil {
		t.Fatal(err)
	}

	codes := func() map[string]string {
		t.Helper()
		rows, err := db.QueryContext(ctx, `SELECT name_ru, COALESCE(model_code, '') FROM products`)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		out := map[string]string{}
		for rows.Next() {
			var n, c string
			if err := rows.Scan(&n, &c); err != nil {
				t.Fatal(err)
			}
			out[n] = c
		}
		return out
	}
	want := map[string]string{"A": "CZ-00001", "B": "cz-00002", "C": "CZ-00003", "D": "ART-1"}
	if got := codes(); !mapsEqual(got, want) {
		t.Fatalf("after up: %v, want %v", got, want)
	}

	// The trigger: a new product without a code gets the next free one;
	// clearing a code keeps it.
	insert("E", nil, 0)
	if _, err := db.ExecContext(ctx, `UPDATE products SET model_code = NULL WHERE name_ru = 'D'`); err != nil {
		t.Fatal(err)
	}
	if got := codes(); got["E"] != "CZ-00004" || got["D"] != "ART-1" {
		t.Fatalf("trigger: %v", got)
	}

	if err := execMigrationFile(ctx, db, down); err != nil {
		t.Fatal(err)
	}
	want = map[string]string{"A": "", "B": "cz-00002", "C": "", "D": "ART-1", "E": ""}
	if got := codes(); !mapsEqual(got, want) {
		t.Fatalf("after down: %v, want %v", got, want)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// ProductRepo: a product created without a code gets a generated one, an
// explicit code is kept, a duplicate (ignoring case) is product_code_taken,
// and Update with a nil code keeps the stored one. Search finds by code.
func TestProductRepoCode(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	f := newFixture(t, 1, 1)
	repo := catalog.NewProductRepo(testDB)

	auto, err := repo.Create(ctx, catalog.ProductInput{CategoryID: f.CategoryID, NameRu: "Авто", NameKy: "Авто", BasePrice: 10, IsActive: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(auto.Code) < len("CZ-00001") || auto.Code[:3] != "CZ-" {
		t.Fatalf("generated code = %q", auto.Code)
	}

	code := "IT-" + uuid.NewString()[:8]
	in := catalog.ProductInput{CategoryID: f.CategoryID, NameRu: "Ручной", NameKy: "Ручной", BasePrice: 10, IsActive: true, Code: &code}
	manual, err := repo.Create(ctx, in)
	if err != nil || manual.Code != code {
		t.Fatalf("manual code: %+v, %v", manual, err)
	}

	lower := "it-" + code[3:]
	dupIn := in
	dupIn.Code = &lower
	_, err = repo.Create(ctx, dupIn)
	var ae *apperr.AppError
	if !errors.As(err, &ae) || ae.Code != "product_code_taken" {
		t.Fatalf("duplicate code err = %v, want product_code_taken", err)
	}

	upd := in
	upd.Code = nil
	upd.NameRu = "Ручной 2"
	got, err := repo.Update(ctx, manual.ID, upd)
	if err != nil || got.Code != code {
		t.Fatalf("update without code: %+v, %v", got, err)
	}

	list, _, err := repo.ListForAdmin(ctx, catalog.AdminListFilter{Query: code})
	if err != nil || len(list) != 1 || list[0].ID != manual.ID {
		t.Fatalf("search by code: %+v, %v", list, err)
	}
}

// The admin color filter keeps only products with a variant of that color.
func TestProductRepoAdminColorFilter(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	f := newFixture(t, 1, 1)
	color := "Цвет-" + uuid.NewString()[:6]
	if _, err := testDB.ExecContext(ctx, `UPDATE product_variants SET color = $1 WHERE id = $2`, color, f.VariantA); err != nil {
		t.Fatal(err)
	}
	repo := catalog.NewProductRepo(testDB)
	list, total, err := repo.ListForAdmin(ctx, catalog.AdminListFilter{Color: color})
	if err != nil || total != 1 || len(list) != 1 || list[0].ID != f.ProductID {
		t.Fatalf("color filter: %+v total=%d err=%v", list, total, err)
	}
	colors, err := repo.DistinctVariantColors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range colors {
		found = found || c == color
	}
	if !found {
		t.Fatalf("DistinctVariantColors lacks %q", color)
	}
}

// A product made in the admin form (generated code) is still matched by
// the import by name and takes the file's article, as before codes
// existed — no duplicate product.
func TestImportMatchesHandMadeProductWithGeneratedCode(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	tag := uuid.NewString()[:8]
	var catID, pointID, productID, before string
	if err := testDB.QueryRowContext(ctx, `INSERT INTO categories (name_ru, name_ky, slug) VALUES ($1, $1, $2) RETURNING id`,
		"Код "+tag, "it-code-"+tag).Scan(&catID); err != nil {
		t.Fatal(err)
	}
	if err := testDB.QueryRowContext(ctx, `INSERT INTO points_of_sale (name, address) VALUES ($1, 'x') RETURNING id`,
		"Точка кода "+tag).Scan(&pointID); err != nil {
		t.Fatal(err)
	}
	if err := testDB.QueryRowContext(ctx, `INSERT INTO products (category_id, name_ru, name_ky, brand, base_price)
		VALUES ($1, $2, $2, 'Cozy', 100) RETURNING id, model_code`, catID, "Ботинки "+tag).Scan(&productID, &before); err != nil {
		t.Fatal(err)
	}

	art := "ART-" + tag
	file := importSheet(t, [][]any{
		{"Артикул", "Название*", "Категория*", "Бренд", "Цена*", "Размер", "Цвет", "SKU", "Остаток"},
		{art, "Ботинки " + tag, "Код " + tag, "Cozy", 4990, "40", "Чёрный", art + "-40", 2},
	})
	if _, err := catalog.ImportProducts(ctx, bytes.NewReader(file), catalog.ImportFormatXLSX,
		catalog.NewSQLImportStore(testDB), catalog.ImportOptions{PointID: pointID}); err != nil {
		t.Fatal(err)
	}
	var n int
	var code string
	var auto bool
	if err := testDB.QueryRowContext(ctx, `SELECT count(*), max(model_code), bool_or(model_code_auto) FROM products WHERE category_id = $1`, catID).
		Scan(&n, &code, &auto); err != nil {
		t.Fatal(err)
	}
	if n != 1 || code != art || auto {
		t.Fatalf("products=%d code=%q auto=%v (generated %q), want the hand-made product updated with the article", n, code, auto, before)
	}
}
