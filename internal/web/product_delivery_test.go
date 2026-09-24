package web

import (
	"context"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/orders"
)

func TestApplyProductDeliveryWithoutZonesUsesFlatFee(t *testing.T) {
	pd := &ProductData{Price: 4500}
	if err := (&handlers{}).applyProductDelivery(context.Background(), pd); err != nil {
		t.Fatal(err)
	}
	if want := orders.CurrentSettings().DeliveryFee; pd.DeliveryFee != want || pd.DeliveryFrom {
		t.Errorf("fee/from = %v/%v, want %v/false", pd.DeliveryFee, pd.DeliveryFrom, want)
	}
}

func TestApplyZoneDeliveryForSinglePairHonorsFreeFrom(t *testing.T) {
	free := 4000.0
	page := &CartPageData{Lines: []CartLineView{{}}, ItemsTotal: 4500}
	applyZoneDelivery(page, []orders.DeliveryZone{{Fee: 150, FreeFrom: &free}, {Fee: 300}})
	if page.DeliveryFee != 0 || !page.DeliveryFrom {
		t.Errorf("fee/from = %v/%v, want 0/true", page.DeliveryFee, page.DeliveryFrom)
	}
}
