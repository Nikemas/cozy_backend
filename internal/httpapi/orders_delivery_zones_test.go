package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/orders"
)

type fakeZoneLister struct{ zones []orders.DeliveryZone }

func (f fakeZoneLister) ListActive(context.Context) ([]orders.DeliveryZone, error) {
	return f.zones, nil
}

func TestListDeliveryZonesHandlerShape(t *testing.T) {
	free := 5000.0
	h := apperr.Wrap(listDeliveryZonesHandler(fakeZoneLister{zones: []orders.DeliveryZone{
		{ID: "z1", NameRu: "Бишкек", NameKy: "Бишкек ш.", Fee: 200, FreeFrom: &free, IsActive: true, SortOrder: 1},
		{ID: "z2", NameRu: "Пригород", NameKy: "Шаар четиндеги", Fee: 400, IsActive: true},
	}}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/delivery-zones", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d zones", len(got))
	}
	want0 := map[string]any{"id": "z1", "name_ru": "Бишкек", "name_ky": "Бишкек ш.", "fee": 200.0, "free_from": 5000.0}
	if len(got[0]) != len(want0) {
		t.Errorf("zone keys = %v, want exactly %v", got[0], want0)
	}
	for k, v := range want0 {
		if got[0][k] != v {
			t.Errorf("zone[0].%s = %v, want %v", k, got[0][k], v)
		}
	}
	if v, ok := got[1]["free_from"]; !ok || v != nil {
		t.Errorf("zone[1].free_from = %v (present %v), want explicit null", v, ok)
	}
}

func TestListDeliveryZonesHandlerEmptyIsArray(t *testing.T) {
	h := apperr.Wrap(listDeliveryZonesHandler(fakeZoneLister{zones: []orders.DeliveryZone{}}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/delivery-zones", nil))
	if body := rec.Body.String(); body != "[]\n" && body != "[]" {
		t.Errorf("body = %q, want []", body)
	}
}

func TestCreateOrderHandlerPassesDeliveryZone(t *testing.T) {
	fake := &fakeOrderService{createResult: &orders.Order{ID: "order-1"}}
	handler := apperr.Wrap(createOrderHandler(fake, nil))
	body := `{"items":[{"variant_id":"var-1","quantity":1}],"address_id":"addr-1","delivery_zone_id":"zone-1"}`
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newCustomerRequest(http.MethodPost, "/api/v1/orders", "cust-1", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if z := fake.lastInput.DeliveryZoneID; z == nil || *z != "zone-1" {
		t.Errorf("DeliveryZoneID = %v, want zone-1", z)
	}
}

// TestOrderJSONDeliveryZone: Order JSON always carries delivery_zone —
// {id,name_ru,name_ky} or null.
func TestOrderJSONDeliveryZone(t *testing.T) {
	b, _ := json.Marshal(orders.Order{ID: "o1"})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if v, ok := m["delivery_zone"]; !ok || v != nil {
		t.Errorf("delivery_zone = %v (present %v), want null", v, ok)
	}
	b, _ = json.Marshal(orders.Order{ID: "o1", DeliveryZone: &orders.OrderDeliveryZone{ID: "z1", NameRu: "Б", NameKy: "Б2"}})
	m = nil
	_ = json.Unmarshal(b, &m)
	z, _ := m["delivery_zone"].(map[string]any)
	if z["id"] != "z1" || z["name_ru"] != "Б" || z["name_ky"] != "Б2" || len(z) != 3 {
		t.Errorf("delivery_zone = %v", m["delivery_zone"])
	}
}
