package admin

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

func payStatus(s orders.PaymentStatus) *orders.PaymentStatus { return &s }

func TestAwaitingPayment(t *testing.T) {
	cases := []struct {
		name string
		o    orders.Order
		want bool
	}{
		{"online pending", orders.Order{Status: orders.StatusPlaced, PaymentMethod: orders.PaymentOnlineCard, PaymentStatus: payStatus(orders.PaymentPending)}, true},
		{"online failed", orders.Order{Status: orders.StatusPlaced, PaymentMethod: orders.PaymentOnlineCard, PaymentStatus: payStatus(orders.PaymentFailed)}, true},
		{"online paid", orders.Order{Status: orders.StatusPlaced, PaymentMethod: orders.PaymentOnlineCard, PaymentStatus: payStatus(orders.PaymentPaid)}, false},
		{"cash", orders.Order{Status: orders.StatusPlaced, PaymentMethod: orders.PaymentCashOnDelivery}, false},
		{"online pending but cancelled", orders.Order{Status: orders.StatusCancelled, PaymentMethod: orders.PaymentOnlineCard, PaymentStatus: payStatus(orders.PaymentPending)}, false},
	}
	for _, c := range cases {
		if got := awaitingPayment(&c.o); got != c.want {
			t.Errorf("%s: awaitingPayment = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestOrderDetailMarksAwaitingPayment(t *testing.T) {
	body := renderOrderDetailUX(t, OrderDetailData{ID: "o1", Number: "COZY-1", AwaitingPayment: true}, "")
	if !strings.Contains(body, "Ожидает оплаты") {
		t.Error("detail has no «Ожидает оплаты» label")
	}
	body = renderOrderDetailUX(t, OrderDetailData{ID: "o1", Number: "COZY-1"}, "")
	if strings.Contains(body, "Ожидает оплаты") {
		t.Error("label shown for a payable order")
	}
}

func TestOrdersListMarksAwaitingPayment(t *testing.T) {
	rr := newTestRenderer(t)
	h := &handlers{}
	st := &staff.Staff{ID: "s1", Name: "Owner", Role: staff.RoleOwner}
	list := []orders.Order{
		{ID: uuid1, OrderNumber: "COZY-1", Status: orders.StatusPlaced, PaymentMethod: orders.PaymentOnlineCard, PaymentStatus: payStatus(orders.PaymentPending)},
		{ID: uuid2, OrderNumber: "COZY-2", Status: orders.StatusPlaced, PaymentMethod: orders.PaymentCashOnDelivery},
	}
	data := h.buildOrdersListView(context.Background(), list, 2, "", "", 1)
	if !data.Rows[0].AwaitingPayment || data.Rows[1].AwaitingPayment {
		t.Fatalf("rows = %+v", data.Rows)
	}
	pd := h.shellPageData("orders", "Заказы", st)
	pd.Data = data
	w := httptest.NewRecorder()
	if err := rr.Render(w, "orders", pd); err != nil {
		t.Fatalf("Render: %v", err)
	}
	// Desktop table + mobile card: once each, for COZY-1 only.
	if n := strings.Count(w.Body.String(), "Ожидает оплаты"); n != 2 {
		t.Errorf("«Ожидает оплаты» shown %d times, want 2", n)
	}
}
