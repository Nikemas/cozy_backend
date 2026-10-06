package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/storefront"
)

// The mobile consent screen reads promo_push_asked to decide whether to
// show the "Акции и скидки" opt-in prompt (App Store guideline 4.5.4).
func TestCustomerProfileExposesPromoPushAsked(t *testing.T) {
	tests := []struct {
		name  string
		asked bool
		want  string
	}{
		{"never asked", false, `"promo_push_asked":false`},
		{"answered", true, `"promo_push_asked":true`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeCustomerProfileStore{getByIDResult: &storefront.Customer{
				ID: "c", Phone: "+996700000000", Lang: "ru", PromoPushAsked: tt.asked,
			}}
			rec := httptest.NewRecorder()
			apperr.Wrap(getCustomerProfileHandler(fake)).ServeHTTP(rec,
				withCustomer(httptest.NewRequest(http.MethodGet, "/api/v1/customer", nil), "c"))

			if got := rec.Body.String(); !strings.Contains(got, tt.want) || !strings.Contains(got, `"promo_push":false`) {
				t.Errorf("body = %s, want %s and promo_push:false", got, tt.want)
			}
		})
	}
}

func TestUpdateCustomerProfileReturnsPromoPushAsked(t *testing.T) {
	fake := &fakeCustomerProfileStore{updateResult: &storefront.Customer{
		ID: "c", Phone: "+996700000000", Lang: "ru", PromoPush: true, PromoPushAsked: true,
	}}
	rec := httptest.NewRecorder()
	req := withCustomer(httptest.NewRequest(http.MethodPut, "/api/v1/customer", strings.NewReader(`{"promo_push":true}`)), "c")
	apperr.Wrap(updateCustomerProfileHandler(fake)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if fake.update.PromoPush == nil || !*fake.update.PromoPush {
		t.Errorf("update = %+v, want promo_push=true", fake.update)
	}
	if got := rec.Body.String(); !strings.Contains(got, `"promo_push":true`) || !strings.Contains(got, `"promo_push_asked":true`) {
		t.Errorf("body = %s, want promo_push and promo_push_asked true", got)
	}
}
