package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/storefront"
)

// fakeCustomerProfileStore is an in-memory customerProfileStore for tests,
// recording calls so tests can assert a handler never touches another
// customer's profile — mirrors fakeAddressStore in addresses_test.go.
type fakeCustomerProfileStore struct {
	getByIDCustomerID string
	getByIDResult     *storefront.Customer
	getByIDErr        error

	setNameCustomerID, setNameName string
	setNameErr                     error
}

func (f *fakeCustomerProfileStore) GetByID(_ context.Context, id string) (*storefront.Customer, error) {
	f.getByIDCustomerID = id
	if f.getByIDErr != nil {
		return nil, f.getByIDErr
	}
	return f.getByIDResult, nil
}

func (f *fakeCustomerProfileStore) SetName(_ context.Context, id, name string) error {
	f.setNameCustomerID, f.setNameName = id, name
	return f.setNameErr
}

func TestGetCustomerProfileHandlerReturnsOwnProfile(t *testing.T) {
	name := "Айбек"
	fake := &fakeCustomerProfileStore{getByIDResult: &storefront.Customer{ID: "customer-1", Phone: "+996700000000", Name: &name}}
	handler := apperr.Wrap(getCustomerProfileHandler(fake))

	req := withCustomer(httptest.NewRequest(http.MethodGet, "/api/v1/customer", nil), "customer-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if fake.getByIDCustomerID != "customer-1" {
		t.Errorf("GetByID called with id = %q, want customer-1", fake.getByIDCustomerID)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"id":"customer-1"`) || !strings.Contains(body, `"phone":"+996700000000"`) || !strings.Contains(body, `"name":"Айбек"`) {
		t.Errorf("body = %s, want id/phone/name snake_case fields", body)
	}
}

func TestGetCustomerProfileHandlerRequiresAuth(t *testing.T) {
	handler := apperr.Wrap(getCustomerProfileHandler(&fakeCustomerProfileStore{}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/customer", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestGetCustomerProfileHandlerDefendsAgainstMissingCustomer(t *testing.T) {
	// GetByID returning nil, nil for a customer id that came from a valid
	// JWT shouldn't happen, but the handler must return a clean 404 instead
	// of panicking on a nil pointer dereference.
	fake := &fakeCustomerProfileStore{getByIDResult: nil}
	handler := apperr.Wrap(getCustomerProfileHandler(fake))

	req := withCustomer(httptest.NewRequest(http.MethodGet, "/api/v1/customer", nil), "customer-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

func TestUpdateCustomerProfileHandlerSetsNameAndReturnsProfile(t *testing.T) {
	name := "Нурлан"
	fake := &fakeCustomerProfileStore{getByIDResult: &storefront.Customer{ID: "customer-1", Phone: "+996700000001", Name: &name}}
	handler := apperr.Wrap(updateCustomerProfileHandler(fake))

	body := `{"name":"Нурлан"}`
	req := withCustomer(httptest.NewRequest(http.MethodPut, "/api/v1/customer", strings.NewReader(body)), "customer-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if fake.setNameCustomerID != "customer-1" || fake.setNameName != "Нурлан" {
		t.Errorf("SetName called with (%q, %q), want (customer-1, Нурлан)", fake.setNameCustomerID, fake.setNameName)
	}
	if !strings.Contains(rec.Body.String(), `"name":"Нурлан"`) {
		t.Errorf("body = %s, want the updated name", rec.Body.String())
	}
}

func TestUpdateCustomerProfileHandlerRequiresAuth(t *testing.T) {
	handler := apperr.Wrap(updateCustomerProfileHandler(&fakeCustomerProfileStore{}))

	req := httptest.NewRequest(http.MethodPut, "/api/v1/customer", strings.NewReader(`{"name":"x"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestUpdateCustomerProfileHandlerPropagatesBlankNameError(t *testing.T) {
	// SetName owns blank-name validation (apperr.BadRequest("invalid_name",
	// ...)) — the handler must not duplicate it, just propagate it.
	fake := &fakeCustomerProfileStore{setNameErr: apperr.BadRequest("invalid_name", "укажите имя")}
	handler := apperr.Wrap(updateCustomerProfileHandler(fake))

	req := withCustomer(httptest.NewRequest(http.MethodPut, "/api/v1/customer", strings.NewReader(`{"name":"   "}`)), "customer-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid_name") {
		t.Errorf("body = %s, want invalid_name error code", rec.Body.String())
	}
}
