package orders

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// stringSliceConverter lets sqlmock accept a []string argument the way
// pgx's stdlib driver does (a Postgres array for = ANY($1)).
type stringSliceConverter struct{}

func (stringSliceConverter) ConvertValue(v any) (driver.Value, error) {
	if s, ok := v.([]string); ok {
		return s, nil
	}
	return driver.DefaultParameterConverter.ConvertValue(v)
}

func withItemPhotoURLs(t *testing.T) {
	t.Helper()
	SetItemPhotoURLs(func(key string) (string, string) {
		return "https://m/" + key, "https://m/thumb/" + key
	})
	t.Cleanup(func() { SetItemPhotoURLs(ItemPhotoURLs(nil)) })
}

func TestAttachItemPhotosOneBatchedQuery(t *testing.T) {
	withItemPhotoURLs(t)
	db, mock, err := sqlmock.New(sqlmock.ValueConverterOption(stringSliceConverter{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(regexp.QuoteMeta("FROM product_variants pv")).
		WithArgs([]string{testVar1, testVar2}).
		WillReturnRows(sqlmock.NewRows([]string{"id", "product_id", "object_key"}).
			AddRow(testVar1, "prod-1", "products/a.jpg").
			AddRow(testVar2, "prod-2", nil))

	list := []Order{
		{ID: "o1", Items: []OrderItem{{VariantID: testVar1}, {VariantID: testVar2}}},
		{ID: "o2", Items: []OrderItem{{VariantID: testVar1}}}, // same variant, no second lookup
	}
	NewService(db).attachItemPhotos(context.Background(), list)

	a := list[0].Items[0]
	if a.ProductID == nil || *a.ProductID != "prod-1" || a.PhotoURL == nil || *a.PhotoURL != "https://m/products/a.jpg" ||
		a.ThumbURL == nil || *a.ThumbURL != "https://m/thumb/products/a.jpg" {
		t.Errorf("item with photo = %+v", a)
	}
	b := list[0].Items[1]
	if b.ProductID == nil || *b.ProductID != "prod-2" || b.PhotoURL != nil || b.ThumbURL != nil {
		t.Errorf("item without photo = %+v, want product_id and null photos", b)
	}
	if list[1].Items[0].PhotoURL == nil {
		t.Error("second order's item not filled")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestAttachItemPhotosIsBestEffortAndOptional(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	list := []Order{{Items: []OrderItem{{VariantID: testVar1}}}}

	// No URL builder installed → no query at all.
	NewService(db).attachItemPhotos(context.Background(), list)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}

	// Query error → items left as they are, no panic.
	withItemPhotoURLs(t)
	mock.ExpectQuery("FROM product_variants").WillReturnError(context.DeadlineExceeded)
	NewService(db).attachItemPhotos(context.Background(), list)
	if list[0].Items[0].ProductID != nil {
		t.Error("failed lookup must leave items untouched")
	}
}

func TestOrderItemJSONHasNullablePhotoFields(t *testing.T) {
	raw, err := json.Marshal(OrderItem{ID: "i1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"product_id":null`, `"photo_url":null`, `"thumb_url":null`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("json %s missing %s", raw, want)
		}
	}
}
