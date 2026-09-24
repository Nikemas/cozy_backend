package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/orders"
)

func TestAppConfigEndpoint(t *testing.T) {
	orders.SetDefaultSettings(orders.Settings{DeliveryFee: 250, MaxOpenOrders: 5, PaymentPendingTTL: time.Hour})
	t.Cleanup(func() { orders.SetDefaultSettings(orders.DefaultSettings()) })
	mux := http.NewServeMux()
	RegisterAppConfigRoutes(mux, &config.Config{
		AppMinVersion: "1.2.0", AppLatestVersion: "1.3.1",
		AppStoreURLIOS: "https://apps.apple.com/app/id1", AppStoreURLAndroid: "https://play.google.com/store/apps/details?id=kg.cozy",
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/app/config", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=300" {
		t.Errorf("Cache-Control = %q", cc)
	}
	var raw map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if fee, ok := raw["delivery_fee"].(float64); !ok || fee != 250 {
		t.Errorf("delivery_fee = %v, want 250 (from DELIVERY_FEE_SOM settings)", raw["delivery_fee"])
	}
	delete(raw, "delivery_fee")
	got := map[string]string{}
	for k, v := range raw {
		got[k], _ = v.(string)
	}
	want := map[string]string{
		"min_version": "1.2.0", "latest_version": "1.3.1",
		"store_url_ios": "https://apps.apple.com/app/id1", "store_url_android": "https://play.google.com/store/apps/details?id=kg.cozy",
	}
	if len(got) != len(want) {
		t.Errorf("got keys %v, want exactly %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}
