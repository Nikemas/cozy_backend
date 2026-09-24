package admin

import (
	"testing"
	"time"

	"github.com/Nikemas/cozy_backend/internal/orders"
)

func TestBuildOrderHistoryData(t *testing.T) {
	placed := orders.StatusPlaced
	name := "Айгуль"
	note := "не оплачен вовремя"
	at := time.Date(2026, 9, 24, 10, 5, 0, 0, time.UTC)
	d := buildOrderHistoryData(ruTr, &orders.Order{
		DeliveryFee:    200,
		RefundRequired: true,
		History: []orders.StatusChange{
			{ToStatus: orders.StatusPlaced, ActorType: orders.ActorCustomer, CreatedAt: at},
			{FromStatus: &placed, ToStatus: orders.StatusConfirmed, ActorType: orders.ActorStaff, ActorName: &name, CreatedAt: at},
			{FromStatus: &placed, ToStatus: orders.StatusCancelled, ActorType: orders.ActorSystem, Note: &note, CreatedAt: at},
		},
	})
	if !d.RefundRequired || d.DeliveryFeeLabel == "" || len(d.History) != 3 {
		t.Fatalf("data = %+v", d)
	}
	if d.History[0].FromLabel != "" || d.History[0].Actor != "покупатель" || d.History[0].DateLabel != "24.09.2026 10:05" {
		t.Errorf("row 0 = %+v", d.History[0])
	}
	if d.History[1].Actor != "Айгуль" || d.History[1].FromLabel == "" {
		t.Errorf("row 1 = %+v", d.History[1])
	}
	if d.History[2].Actor != "система" || d.History[2].Note != note {
		t.Errorf("row 2 = %+v", d.History[2])
	}
	if (buildOrderHistoryData(ruTr, &orders.Order{})).DeliveryFeeLabel != "" {
		t.Error("pickup order must not show a delivery line")
	}
}
