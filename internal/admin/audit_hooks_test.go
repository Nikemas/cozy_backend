package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/audit"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

func ownerCtx() context.Context {
	return staff.NewContextWithStaff(context.Background(), &staff.Staff{ID: "s1", Name: "Айгерим", Role: staff.RoleOwner})
}

func TestProductChangesOnlyChangedFields(t *testing.T) {
	brand := "Nike"
	old := &productSnapshot{CategoryID: "c1", NameRu: "Кеды", NameKy: "Кеды", Brand: &brand, BasePrice: 4500}
	in := catalog.ProductInput{CategoryID: "c1", NameRu: "Кеды", NameKy: "Кеды", Brand: &brand, BasePrice: 4900}
	got := productChanges(old, in)
	if len(got) != 1 {
		t.Fatalf("changes = %v, want only base_price", got)
	}
	if c := got["base_price"].(audit.Change); c.From != 4500.0 || c.To != 4900.0 {
		t.Errorf("base_price change = %+v", c)
	}
	if lbl := changedFieldsLabel(got); lbl != "цена" {
		t.Errorf("label = %q", lbl)
	}
}

func TestProductSaveEntriesVariants(t *testing.T) {
	in := productSaveInput{
		ProductID: "p1",
		Product:   catalog.ProductInput{CategoryID: "c1", NameRu: "Кеды", NameKy: "Кеды", BasePrice: 100},
		Variants: []variantRowInput{
			{Key: "v1", ID: "v1", Size: "42", Color: "Белый"}, // unchanged
			{Key: "v2", ID: "v2", Size: "43", Color: "Синий"}, // colour changed
			{Key: "n1", Size: "44", Color: "Белый"},           // new
		},
	}
	old := &productSnapshot{CategoryID: "c1", NameRu: "Кеды", NameKy: "Кеды", BasePrice: 100}
	oldVariants := map[string]variantSnapshot{"v1": {"42", "Белый"}, "v2": {"43", "Чёрный"}, "v3": {"45", "Белый"}}
	entries := productSaveEntries("p1", in, old, oldVariants, map[string]string{"v1": "v1", "v2": "v2", "n1": "vNew"})

	var actions []string
	for _, e := range entries {
		actions = append(actions, e.Action+":"+e.EntityID)
	}
	want := []string{"variant.update:v2", "variant.create:vNew", "variant.delete:v3"}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Errorf("entries = %v, want %v (no product.update: nothing changed)", actions, want)
	}
	if entries[0].Details["product_id"] != "p1" {
		t.Errorf("variant entry lacks product_id: %+v", entries[0].Details)
	}
}

func TestProductSaveEntriesCreate(t *testing.T) {
	in := productSaveInput{Product: catalog.ProductInput{CategoryID: "c1", NameRu: "Кеды", NameKy: "Кеды", BasePrice: 100},
		Variants: []variantRowInput{{Key: "n1", Size: "42", Color: "Белый"}}}
	entries := productSaveEntries("pNew", in, nil, nil, map[string]string{"n1": "vNew"})
	if len(entries) != 2 || entries[0].Action != audit.ActionProductCreate || entries[0].EntityID != "pNew" ||
		entries[1].Action != audit.ActionVariantCreate {
		t.Fatalf("entries = %+v", entries)
	}
}

// With a journal, a form save records the product diff and every
// changed stock cell (old → new with names) in the SAME transaction,
// isolated by savepoints.
func TestProductStoreSaveJournalsInTx(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.ValueConverterOption(stringSliceConverter{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectExec(`SAVEPOINT audit_log_write`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT category_id, name_ru, name_ky, description_ru, description_ky, brand, base_price\s+FROM products WHERE id = \$1`).
		WithArgs("p1").
		WillReturnRows(sqlmock.NewRows([]string{"category_id", "name_ru", "name_ky", "description_ru", "description_ky", "brand", "base_price"}).
			AddRow("cat1", "Nike", "Nike", nil, nil, nil, 4000.0))
	mock.ExpectQuery(`SELECT id, size, color FROM product_variants WHERE product_id = \$1`).WithArgs("p1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "size", "color"}).AddRow("v1", "42", "Белый"))
	mock.ExpectExec(`RELEASE SAVEPOINT audit_log_write`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`UPDATE products`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("p1"))
	mock.ExpectQuery(`SELECT id FROM product_variants WHERE product_id = \$1`).WithArgs("p1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("v1"))
	mock.ExpectExec(`UPDATE product_variants SET size`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE stock SET quantity`).WithArgs("v1", "pB", 2, 3).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM product_images`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`SAVEPOINT audit_log_write`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`FROM product_variants v JOIN products p`).WithArgs([]string{"v1"}).
		WillReturnRows(sqlmock.NewRows([]string{"id", "pid", "name", "size", "color"}).AddRow("v1", "p1", "Nike", "42", "Белый"))
	mock.ExpectQuery(`SELECT id, name FROM points_of_sale WHERE id = ANY`).WithArgs([]string{"pB"}).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow("pB", "Дордой"))
	mock.ExpectExec(`INSERT INTO audit_log`).
		WithArgs("s1", audit.ActionProductUpdate, audit.EntityProduct, "p1", "Изменён товар «Nike»: цена",
			`{"base_price":{"from":4000,"to":4500}}`, nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO audit_log`).
		WithArgs("s1", audit.ActionStockUpdate, audit.EntityStock, "v1", "Остаток «Nike» 42 / Белый, Дордой: 3 → 2",
			sqlmock.AnyArg(), nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`RELEASE SAVEPOINT audit_log_write`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	store := &productStore{db: db, audit: audit.New(db)}
	_, err = store.Save(ownerCtx(), productSaveInput{
		ProductID: "p1",
		Product:   validProductInput(),
		Variants:  []variantRowInput{{Key: "v1", ID: "v1", Size: "42", Color: "Белый"}},
		Stock:     []stockCellChange{{RowKey: "v1", PointID: "pB", Qty: 2, Orig: intPtr(3)}},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A journal failure (here: the name lookup) rolls back to the savepoint;
// the stock change itself still commits.
func TestStockPageApplyJournalFailureDoesNotFailSave(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.ValueConverterOption(stringSliceConverter{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE stock SET quantity`).WithArgs("v1", "pA", 7, 5).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`SAVEPOINT audit_log_write`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`FROM product_variants v JOIN products p`).WillReturnError(sql.ErrConnDone)
	mock.ExpectExec(`ROLLBACK TO SAVEPOINT audit_log_write`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	repo := &stockPageRepo{db: db, audit: audit.New(db)}
	if err := repo.Apply(ownerCtx(), []stockCellChange{{RowKey: "v1", PointID: "pA", Qty: 7, Orig: intPtr(5)}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestStockEntriesSkipUnchangedAndRemovedRows(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.ValueConverterOption(stringSliceConverter{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM product_variants v JOIN products p`).WithArgs([]string{"v1"}).
		WillReturnRows(sqlmock.NewRows([]string{"id", "pid", "name", "size", "color"}).AddRow("v1", "p1", "Nike", "42", "Белый"))
	mock.ExpectQuery(`FROM points_of_sale`).WithArgs([]string{"pA"}).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}))
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := stockEntriesTx(context.Background(), tx, map[string]string{"v1": "v1"}, []stockCellChange{
		{RowKey: "v1", PointID: "pA", Qty: 4},                  // new cell: 0 → 4
		{RowKey: "v1", PointID: "pB", Qty: 3, Orig: intPtr(3)}, // unchanged
		{RowKey: "gone", PointID: "pA", Qty: 9},                // row removed in the same save
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want 1", entries)
	}
	b, _ := json.Marshal(entries[0].Details)
	if !strings.Contains(string(b), `"quantity":{"from":0,"to":4}`) || !strings.Contains(entries[0].Summary, "pA: 0 → 4") {
		t.Errorf("entry = %s / %s", entries[0].Summary, b)
	}
}
