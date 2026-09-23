package admin

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/orders"
)

// stringSliceConverter lets sqlmock accept []string args the way pgx's
// stdlib driver does for = ANY($1).
type stringSliceConverter struct{}

func (stringSliceConverter) ConvertValue(v any) (driver.Value, error) {
	if s, ok := v.([]string); ok {
		return s, nil
	}
	return driver.DefaultParameterConverter.ConvertValue(v)
}

func TestOrderListMetaRepoBatchQueries(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.ValueConverterOption(stringSliceConverter{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	repo := newOrderListMetaRepo(db)

	mock.ExpectQuery(`FROM order_items\s+WHERE order_id = ANY\(\$1\)\s+GROUP BY order_id`).
		WithArgs([]string{"o1", "o2"}).
		WillReturnRows(sqlmock.NewRows([]string{"order_id", "count"}).AddRow("o1", 3))
	mock.ExpectQuery(`FROM customers WHERE id = ANY\(\$1\)`).
		WithArgs([]string{"c1"}).
		WillReturnRows(sqlmock.NewRows([]string{"id", "phone"}).AddRow("c1", "+996555000111"))

	counts, err := repo.ItemCounts(context.Background(), []string{"o1", "o2"})
	if err != nil || counts["o1"] != 3 || counts["o2"] != 0 {
		t.Fatalf("ItemCounts = %v, %v; want o1=3, o2 absent", counts, err)
	}
	phones, err := repo.CustomerPhones(context.Background(), []string{"c1"})
	if err != nil || phones["c1"] != "+996555000111" {
		t.Fatalf("CustomerPhones = %v, %v", phones, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// fakeOrderListMeta records how many batch calls a page render makes.
type fakeOrderListMeta struct {
	countCalls, phoneCalls int
	phoneIDs               []string
	err                    error
}

func (f *fakeOrderListMeta) ItemCounts(_ context.Context, ids []string) (map[string]int, error) {
	f.countCalls++
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]int{}
	for _, id := range ids {
		out[id] = 2
	}
	return out, nil
}

func (f *fakeOrderListMeta) CustomerPhones(_ context.Context, ids []string) (map[string]string, error) {
	f.phoneCalls++
	f.phoneIDs = ids
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]string{}
	for _, id := range ids {
		out[id] = "phone-" + id
	}
	return out, nil
}

func TestBuildOrdersListViewBatchesLookups(t *testing.T) {
	meta := &fakeOrderListMeta{}
	h := &handlers{orderMeta: meta}
	list := []orders.Order{
		{ID: "o1", CustomerID: "c1", CreatedAt: time.Now()},
		{ID: "o2", CustomerID: "c1", CreatedAt: time.Now()},
		{ID: "o3", CustomerID: "c2", CreatedAt: time.Now()},
	}

	data := h.buildOrdersListView(context.Background(), list, 3, "", "all", 1)

	if meta.countCalls != 1 || meta.phoneCalls != 1 {
		t.Fatalf("batch calls = %d counts / %d phones, want 1 each", meta.countCalls, meta.phoneCalls)
	}
	if len(meta.phoneIDs) != 2 {
		t.Errorf("phone lookup ids = %v, want customers de-duplicated", meta.phoneIDs)
	}
	if len(data.Rows) != 3 || data.Rows[0].Phone != "phone-c1" || data.Rows[2].Phone != "phone-c2" || data.Rows[1].ItemsCount != 2 {
		t.Fatalf("rows = %+v, want phones and counts filled from the batch", data.Rows)
	}
}

func TestBuildOrdersListViewDegradesOnLookupError(t *testing.T) {
	h := &handlers{orderMeta: &fakeOrderListMeta{err: errors.New("db down")}}
	list := []orders.Order{{ID: "o1", CustomerID: "c1", CreatedAt: time.Now()}}

	data := h.buildOrdersListView(context.Background(), list, 1, "", "all", 1)

	if len(data.Rows) != 1 || data.Rows[0].ItemsCount != 0 || data.Rows[0].Phone != "" {
		t.Fatalf("rows = %+v, want the row rendered with 0 items / empty phone", data.Rows)
	}
}
