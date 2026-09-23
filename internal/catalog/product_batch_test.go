package catalog

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// passthroughConverter lets sqlmock accept a []string argument the way
// pgx's stdlib driver does (as a Postgres array for = ANY($1)); sqlmock's
// default converter would reject it before the query is even matched.
type passthroughConverter struct{}

func (passthroughConverter) ConvertValue(v any) (driver.Value, error) {
	if s, ok := v.([]string); ok {
		return s, nil
	}
	return driver.DefaultParameterConverter.ConvertValue(v)
}

func TestGetActiveByIDsSingleQuery(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.ValueConverterOption(passthroughConverter{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	now := time.Now()
	cols := []string{"id", "category_id", "name_ru", "name_ky", "description_ru", "description_ky",
		"brand", "base_price", "is_active", "created_at", "updated_at"}
	mock.ExpectQuery(`FROM products\s+WHERE id = ANY\(\$1\) AND is_active = true`).
		WithArgs([]string{"p1", "p2", "gone"}).
		WillReturnRows(sqlmock.NewRows(cols).
			AddRow("p1", "c1", "Кроссовки", "Кроссовка", nil, nil, "Nike", 4500.0, true, now, now).
			AddRow("p2", "c1", "Ботинки", "Бут", nil, nil, nil, 5200.0, true, now, now))

	got, err := NewProductRepo(db).GetActiveByIDs(context.Background(), []string{"p1", "p2", "gone"})
	if err != nil {
		t.Fatalf("GetActiveByIDs: %v", err)
	}
	if len(got) != 2 || got["p1"].NameRu != "Кроссовки" || got["p2"].BasePrice != 5200 {
		t.Fatalf("got %+v, want p1 and p2 only", got)
	}
	if _, ok := got["gone"]; ok {
		t.Fatal("inactive/missing id must be absent from the map")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetActiveByIDsEmptySkipsQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	got, err := NewProductRepo(db).GetActiveByIDs(context.Background(), nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want empty map, nil", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
