package web

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/orders"
)

func TestStatusViewMapsEveryKnownStatus(t *testing.T) {
	cases := []struct {
		status       orders.OrderStatus
		wantKey      string
		wantInFlight bool
	}{
		{orders.StatusPlaced, "order.status.placed", true},
		{orders.StatusConfirmed, "order.status.confirmed", true},
		{orders.StatusCourierAssigned, "order.status.courier_assigned", true},
		{orders.StatusDelivered, "order.status.delivered", false},
		{orders.StatusCancelled, "order.status.cancelled", false},
	}
	for _, c := range cases {
		got := statusView(c.status)
		if got.Key != c.wantKey {
			t.Errorf("statusView(%q).Key = %q, want %q", c.status, got.Key, c.wantKey)
		}
		if got.InFlight != c.wantInFlight {
			t.Errorf("statusView(%q).InFlight = %v, want %v", c.status, got.InFlight, c.wantInFlight)
		}
		if got.Class == "" {
			t.Errorf("statusView(%q).Class is empty", c.status)
		}
	}
}

func TestStatusViewFallsBackOnUnknownStatus(t *testing.T) {
	got := statusView(orders.OrderStatus("some_future_status"))
	if got.Key == "" || got.Class == "" {
		t.Fatalf("statusView(unknown) = %+v, want a non-empty fallback", got)
	}
}

func TestFormatAmount(t *testing.T) {
	cases := []struct {
		amount float64
		want   string
	}{
		{0, "0 сом"},
		{200, "200 сом"},
		{7900, "7 900 сом"},
		{1234567, "1 234 567 сом"},
		{4999.6, "5 000 сом"}, // rounds to nearest whole som
		{-1500, "-1 500 сом"},
	}
	for _, c := range cases {
		if got := formatAmount(c.amount, "сом"); got != c.want {
			t.Errorf("formatAmount(%v) = %q, want %q", c.amount, got, c.want)
		}
	}
}

func TestBuildOrderViewsShowsTrackForInFlightAndRepeatForFinished(t *testing.T) {
	noop := func(key string) string { return key }
	list := []orders.Order{
		{
			ID:          "o1",
			OrderNumber: "COZY-20260101-001",
			Status:      orders.StatusCourierAssigned,
			TotalAmount: 3000,
			CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			Items: []orders.OrderItem{
				{VariantID: "v1", ProductNameSnapshot: "Air Runner", SizeSnapshot: "42", ColorSnapshot: "Чёрный", Quantity: 1},
			},
		},
		{
			ID:          "o2",
			OrderNumber: "COZY-20260102-002",
			Status:      orders.StatusDelivered,
			TotalAmount: 4700,
			CreatedAt:   time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		},
	}

	views := buildOrderViews(list, noop, map[string]string{"v1": "https://m/v1/thumb.jpg"})
	if len(views) != 2 {
		t.Fatalf("buildOrderViews returned %d views, want 2", len(views))
	}
	if !views[0].ShowTrack || views[0].ShowRepeat {
		t.Errorf("in-flight order: ShowTrack=%v ShowRepeat=%v, want true/false", views[0].ShowTrack, views[0].ShowRepeat)
	}
	if views[1].ShowTrack || !views[1].ShowRepeat {
		t.Errorf("delivered order: ShowTrack=%v ShowRepeat=%v, want false/true", views[1].ShowTrack, views[1].ShowRepeat)
	}
	if got := views[0].Items[0].PhotoURL; got != "https://m/v1/thumb.jpg" {
		t.Errorf("item PhotoURL = %q, want the variant's thumbnail", got)
	}
	if views[1].TotalLabel != "4 700 common.currency" {
		t.Errorf("TotalLabel = %q", views[1].TotalLabel)
	}
	if views[0].DateLabel != "01.01.2026" {
		t.Errorf("DateLabel = %q, want 01.01.2026", views[0].DateLabel)
	}
}

func TestIsNotImplemented(t *testing.T) {
	notImpl := apperr.New(http.StatusNotImplemented, "not_implemented", "orders.Service.ListOrders: not implemented")
	if !isNotImplemented(notImpl) {
		t.Error("isNotImplemented(501 apperr) = false, want true")
	}

	badReq := apperr.BadRequest("bad_request", "nope")
	if isNotImplemented(badReq) {
		t.Error("isNotImplemented(400 apperr) = true, want false")
	}

	if isNotImplemented(errors.New("plain error")) {
		t.Error("isNotImplemented(non-apperr) = true, want false")
	}
}
