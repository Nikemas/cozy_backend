package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/storefront"
)

func TestOrderThumbQueriesPreferVariantColor(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.ValueConverterOption(stringSliceConverter{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	repo := newOrderListMetaRepo(db)

	mock.ExpectQuery(`SELECT DISTINCT ON \(pv.id\) pv.id, pi.object_key.*WHERE pv.id = ANY\(\$1\)\s+ORDER BY pv.id, CASE WHEN pi.color = pv.color THEN 0 WHEN pi.color IS NULL THEN 1 ELSE 2 END, pi.sort_order`).
		WithArgs([]string{"v1"}).
		WillReturnRows(sqlmock.NewRows([]string{"id", "object_key"}).AddRow("v1", "products/white.jpg"))
	mock.ExpectQuery(`SELECT DISTINCT ON \(oi.order_id\) oi.order_id, pi.object_key.*WHERE oi.order_id = ANY\(\$1\)\s+ORDER BY oi.order_id, oi.id, CASE`).
		WithArgs([]string{"o1"}).
		WillReturnRows(sqlmock.NewRows([]string{"order_id", "object_key"}).AddRow("o1", "products/a.jpg"))

	vt, err := repo.VariantThumbs(context.Background(), []string{"v1"})
	if err != nil || vt["v1"] != "products/white.jpg" {
		t.Fatalf("VariantThumbs = %v, %v", vt, err)
	}
	ot, err := repo.OrderThumbs(context.Background(), []string{"o1"})
	if err != nil || ot["o1"] != "products/a.jpg" {
		t.Fatalf("OrderThumbs = %v, %v", ot, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestOrdersListShowsThumbnails(t *testing.T) {
	meta := &fakeOrderListMeta{searchResult: []orders.Order{{ID: "o1", OrderNumber: "COZY-1", CustomerID: "c1", CreatedAt: time.Now()}}}
	h := &handlers{render: newTestRenderer(t), orderMeta: meta, cfg: &config.Config{MinIOEndpoint: "localhost:9000", MinIOBucket: "cozy-media"}}

	w := httptest.NewRecorder()
	h.ordersListPage(w, requestAs(http.MethodGet, "/admin/orders", pointStaff(strPtr("p1")), nil))

	if !strings.Contains(w.Body.String(), `class="admin-orders-thumb" src="http://localhost:9000/cozy-media/products/order-o1`) {
		t.Error("order thumbnail not rendered")
	}
}

func TestOrderDetailShowsItemThumbnails(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	h := &handlers{
		orderMeta: &fakeOrderListMeta{},
		customers: storefront.NewCustomerRepo(db), // unexpected query -> error -> empty phone
		addresses: storefront.NewAddressRepo(db),
		cfg:       &config.Config{MinIOEndpoint: "localhost:9000", MinIOBucket: "cozy-media"},
	}
	o := &orders.Order{ID: "o1", Items: []orders.OrderItem{
		{VariantID: "v1", ProductNameSnapshot: "Nike", Quantity: 1, Price: 100},
	}}

	view := h.buildOrderDetailView(context.Background(), o, "owner")
	if len(view.Items) != 1 || !strings.Contains(view.Items[0].ThumbURL, "products/v1") {
		t.Fatalf("items = %+v", view.Items)
	}
}
