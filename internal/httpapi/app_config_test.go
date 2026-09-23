package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/config"
)

func TestAppConfigEndpoint(t *testing.T) {
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
	var got map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
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
