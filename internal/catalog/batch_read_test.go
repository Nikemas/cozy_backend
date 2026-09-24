package catalog

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestVariantListByProductIDsGroupsInOneQuery(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.ValueConverterOption(passthroughConverter{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(`FROM product_variants\s+WHERE product_id = ANY\(\$1\)`).
		WithArgs([]string{"p1", "p2"}).
		WillReturnRows(sqlmock.NewRows([]string{"id", "product_id", "size", "color", "sku", "price_override"}).
			AddRow("v1", "p1", "40", "black", nil, nil).
			AddRow("v2", "p1", "41", "black", "SKU", 3000.0).
			AddRow("v3", "p2", "38", "white", nil, nil))

	got, err := NewVariantRepo(db).ListByProductIDs(context.Background(), []string{"p1", "p2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got["p1"]) != 2 || len(got["p2"]) != 1 || got["p1"][1].ID != "v2" {
		t.Errorf("grouped = %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestImageListByProductIDsEmptySkipsQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	got, err := NewImageRepo(db).ListByProductIDs(context.Background(), nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestImageListByProductIDsGroupsInOneQuery(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.ValueConverterOption(passthroughConverter{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectQuery(`FROM product_images\s+WHERE product_id = ANY\(\$1\)`).
		WithArgs([]string{"p1", "p2"}).
		WillReturnRows(sqlmock.NewRows([]string{"id", "product_id", "object_key", "sort_order", "color"}).
			AddRow("i1", "p1", "a.jpg", 0, nil).
			AddRow("i2", "p2", "b.jpg", 0, "black"))
	got, err := NewImageRepo(db).ListByProductIDs(context.Background(), []string{"p1", "p2"})
	if err != nil || len(got["p1"]) != 1 || got["p2"][0].ObjectKey != "b.jpg" {
		t.Fatalf("got %+v, %v", got, err)
	}
}
