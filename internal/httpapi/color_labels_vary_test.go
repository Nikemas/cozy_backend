package httpapi

import (
	"net/http"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/orders"
)

func TestAddVaryKeepsSingleEntry(t *testing.T) {
	tests := []struct {
		name     string
		existing []string
		want     []string
	}{
		{"empty header", nil, []string{"Accept-Language"}},
		{"other field kept", []string{"Origin"}, []string{"Origin", "Accept-Language"}},
		{"already in comma list", []string{"Origin, accept-language"}, []string{"Origin, accept-language"}},
		{"already as own value", []string{"Origin", "Accept-Language"}, []string{"Origin", "Accept-Language"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			for _, v := range tt.existing {
				h.Add("Vary", v)
			}

			addVary(h, varyHeaderLanguage)

			got := h.Values("Vary")
			if len(got) != len(tt.want) {
				t.Fatalf("Vary = %q, want %q", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("Vary = %q, want %q", got, tt.want)
				}
			}
		})
	}
}

func TestLocalizeOrdersNilAndEmpty(t *testing.T) {
	if got := localizeOrders(nil, "ky"); got != nil {
		t.Errorf("localizeOrders(nil) = %v, want nil", got)
	}
	got := localizeOrders([]orders.Order{{ID: "o1"}}, "ky")
	if len(got) != 1 || got[0].Items != nil || got[0].ID != "o1" {
		t.Errorf("order without items = %+v, want passthrough with nil items", got)
	}
	if out := localizeOrder(nil, "ru"); out.Order != nil || out.Items != nil {
		t.Errorf("localizeOrder(nil) = %+v, want zero value", out)
	}
}
