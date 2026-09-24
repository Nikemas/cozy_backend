package web

import (
	"testing"

	"github.com/Nikemas/cozy_backend/internal/orders"
)

// TestCartPageFromLinesUsesChargedDeliveryFee: the cart summary shows the
// same delivery fee orders.Service adds to a delivery order's total, and
// flags unavailable lines.
func TestCartPageFromLinesUsesChargedDeliveryFee(t *testing.T) {
	page := cartPageFromLines([]orders.CartLine{
		{VariantID: "v1", ProductName: "Air", Qty: 2, Price: 3000, ProductActive: true, InStock: 5},
		{VariantID: "v2", ProductName: "Old", Qty: 1, Price: 1000, ProductActive: false, InStock: 5},
	}, 250)

	if page.ItemsTotal != 7000 || page.DeliveryFee != 250 || page.GrandTotal != 7250 {
		t.Errorf("items/fee/grand = %v/%v/%v, want 7000/250/7250", page.ItemsTotal, page.DeliveryFee, page.GrandTotal)
	}
	if !page.Lines[0].Available || page.Lines[1].Available || !page.HasUnavailable {
		t.Errorf("availability flags wrong: %+v", page)
	}
	if page.DeliveryLabel == "" {
		t.Error("delivery label must be set")
	}

	empty := cartPageFromLines(nil, 250)
	if empty.GrandTotal != 0 || empty.DeliveryFee != 0 || empty.Lines == nil {
		t.Errorf("empty cart: %+v", empty)
	}
}

func TestBuildOrderViewsShowCancelOnlyForPlacedUnpaid(t *testing.T) {
	paid := orders.PaymentPaid
	views := buildOrderViews([]orders.Order{
		{ID: "a", Status: orders.StatusPlaced, PaymentMethod: orders.PaymentCashOnDelivery},
		{ID: "b", Status: orders.StatusConfirmed, PaymentMethod: orders.PaymentCashOnDelivery},
		{ID: "c", Status: orders.StatusPlaced, PaymentMethod: orders.PaymentOnlineCard, PaymentStatus: &paid},
	}, func(k string) string { return k })
	if !views[0].ShowCancel || views[1].ShowCancel || views[2].ShowCancel {
		t.Errorf("ShowCancel = %v/%v/%v, want true/false/false", views[0].ShowCancel, views[1].ShowCancel, views[2].ShowCancel)
	}
}
