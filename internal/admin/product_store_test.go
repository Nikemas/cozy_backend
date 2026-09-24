package admin

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/catalog"
)

func intPtr(n int) *int { return &n }

func validProductInput() catalog.ProductInput {
	return catalog.ProductInput{CategoryID: "cat1", NameRu: "Nike", NameKy: "Nike", BasePrice: 4500, IsActive: true}
}

// Happy path: everything runs in one transaction, stock is written with an
// optimistic UPDATE ... WHERE quantity = <orig> (never an absolute upsert),
// and a new cell is an INSERT ... ON CONFLICT DO NOTHING.
func TestProductStoreSaveWritesOnlyChangedCellsInOneTx(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE products`).WithArgs("p1", "cat1", "Nike", "Nike", nil, nil, nil, 4500.0).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("p1"))
	mock.ExpectQuery(`SELECT id FROM product_variants WHERE product_id = \$1`).WithArgs("p1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("v1"))
	mock.ExpectExec(`UPDATE product_variants SET size = \$2, color = \$3 WHERE id = \$1`).WithArgs("v1", "42", "Белый").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`INSERT INTO product_variants`).WithArgs("p1", "43", "Чёрный").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("vNew"))
	mock.ExpectExec(`UPDATE stock SET quantity = \$3, updated_at = now\(\)\s+WHERE variant_id = \$1 AND point_id = \$2 AND quantity = \$4`).
		WithArgs("v1", "pB", 2, 3).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO stock .* ON CONFLICT \(variant_id, point_id\) DO NOTHING`).
		WithArgs("vNew", "pA", 6).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM product_images WHERE product_id = \$1`).WithArgs("p1").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	store := newProductStore(db)
	id, err := store.Save(context.Background(), productSaveInput{
		ProductID: "p1",
		Product:   validProductInput(),
		Variants: []variantRowInput{
			{Key: "v1", ID: "v1", Size: "42", Color: "Белый"},
			{Key: "n1", Size: "43", Color: "Чёрный"},
		},
		Stock: []stockCellChange{
			{RowKey: "v1", PointID: "pB", Qty: 2, Orig: intPtr(3)},
			{RowKey: "n1", PointID: "pA", Qty: 6},
		},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if id != "p1" {
		t.Errorf("id = %q", id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A cell whose stock moved since the form was rendered (e.g. a sale took
// it from 3 to 1) must not be overwritten: the save reports a conflict
// with the current value and the whole transaction rolls back.
func TestProductStoreSaveConflictRollsBack(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE products`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("p1"))
	mock.ExpectQuery(`SELECT id FROM product_variants`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("v1"))
	mock.ExpectExec(`UPDATE product_variants`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE stock SET quantity`).WithArgs("v1", "pA", 7, 3).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT quantity FROM stock WHERE variant_id = \$1 AND point_id = \$2`).WithArgs("v1", "pA").
		WillReturnRows(sqlmock.NewRows([]string{"quantity"}).AddRow(1))
	mock.ExpectRollback()

	_, err = newProductStore(db).Save(context.Background(), productSaveInput{
		ProductID: "p1",
		Product:   validProductInput(),
		Variants:  []variantRowInput{{Key: "v1", ID: "v1", Size: "42", Color: "Белый"}},
		Stock:     []stockCellChange{{RowKey: "v1", PointID: "pA", Qty: 7, Orig: intPtr(3)}},
	})
	var conflict *stockConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want *stockConflictError", err)
	}
	if len(conflict.Cells) != 1 || conflict.Cells[0].Current != 1 || !conflict.Cells[0].Exists {
		t.Errorf("conflict = %+v", conflict.Cells)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Deleting a variant that has orders used to leave the product half-saved
// (product + earlier variants written, later ones not). Now the FK error
// rolls everything back and surfaces as a readable conflict.
func TestProductStoreSaveVariantInUseRollsBack(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE products`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("p1"))
	mock.ExpectQuery(`SELECT id FROM product_variants`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("v1").AddRow("vOld"))
	mock.ExpectExec(`DELETE FROM product_variants WHERE id = \$1`).WithArgs("vOld").
		WillReturnError(&pgconn.PgError{Code: pgForeignKeyViolation})
	mock.ExpectRollback()

	_, err = newProductStore(db).Save(context.Background(), productSaveInput{
		ProductID: "p1",
		Product:   validProductInput(),
		Variants:  []variantRowInput{{Key: "v1", ID: "v1", Size: "42", Color: "Белый"}},
	})
	var ae *apperr.AppError
	if !errors.As(err, &ae) || ae.Code != "variant_in_use" {
		t.Fatalf("err = %v, want variant_in_use", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestProductStoreSaveValidatesBeforeTouchingDB(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	in := validProductInput()
	in.NameKy = " "
	if _, err := newProductStore(db).Save(context.Background(), productSaveInput{Product: in}); err == nil {
		t.Fatal("want validation error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
