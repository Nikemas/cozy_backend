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

// fakeAddressStore is an in-memory addressStore for tests, recording calls
// (including which customerID each call was scoped to) so tests can assert
// a handler never lets one customer touch another's address by id alone.
type fakeAddressStore struct {
	listCustomerID string
	list           []storefront.Address

	createCustomerID string
	createInput      storefront.AddressInput
	createResult     *storefront.Address

	updateCustomerID, updateID string
	updateInput                storefront.AddressInput
	updateResult               *storefront.Address
	updateErr                  error

	deleteCustomerID, deleteID string
	deleteErr                  error
}

func (f *fakeAddressStore) List(_ context.Context, customerID string) ([]storefront.Address, error) {
	f.listCustomerID = customerID
	return f.list, nil
}

func (f *fakeAddressStore) Create(_ context.Context, customerID string, in storefront.AddressInput) (*storefront.Address, error) {
	f.createCustomerID, f.createInput = customerID, in
	if f.createResult != nil {
		return f.createResult, nil
	}
	return &storefront.Address{ID: "new-id", CustomerID: customerID, AddressText: in.AddressText}, nil
}

func (f *fakeAddressStore) Update(_ context.Context, customerID, id string, in storefront.AddressInput) (*storefront.Address, error) {
	f.updateCustomerID, f.updateID, f.updateInput = customerID, id, in
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	if f.updateResult != nil {
		return f.updateResult, nil
	}
	return &storefront.Address{ID: id, CustomerID: customerID, AddressText: in.AddressText}, nil
}

func (f *fakeAddressStore) Delete(_ context.Context, customerID, id string) error {
	f.deleteCustomerID, f.deleteID = customerID, id
	return f.deleteErr
}

func TestListAddressesHandlerScopesToCustomer(t *testing.T) {
	fake := &fakeAddressStore{list: []storefront.Address{{ID: "a1", AddressText: "ул. Чуй 1"}}}
	handler := apperr.Wrap(listAddressesHandler(fake))

	req := withCustomer(httptest.NewRequest(http.MethodGet, "/api/v1/addresses", nil), "customer-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if fake.listCustomerID != "customer-1" {
		t.Errorf("List called with customerID = %q, want customer-1", fake.listCustomerID)
	}
	if !strings.Contains(rec.Body.String(), "ул. Чуй 1") {
		t.Errorf("body = %s, want it to contain the listed address", rec.Body.String())
	}
}

func TestListAddressesHandlerRequiresAuth(t *testing.T) {
	handler := apperr.Wrap(listAddressesHandler(&fakeAddressStore{}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/addresses", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestCreateAddressHandlerMapsBodyAndScopesToCustomer(t *testing.T) {
	fake := &fakeAddressStore{}
	handler := apperr.Wrap(createAddressHandler(fake))

	body := `{"label":"Дом","address_text":"ул. Чуй 123","lat":42.87,"lng":74.59,"is_default":true}`
	req := withCustomer(httptest.NewRequest(http.MethodPost, "/api/v1/addresses", strings.NewReader(body)), "customer-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if fake.createCustomerID != "customer-1" {
		t.Errorf("Create called with customerID = %q, want customer-1", fake.createCustomerID)
	}
	if fake.createInput.AddressText != "ул. Чуй 123" || !fake.createInput.IsDefault {
		t.Errorf("Create called with input = %+v, want mapped body", fake.createInput)
	}
	if fake.createInput.Label == nil || *fake.createInput.Label != "Дом" {
		t.Errorf("Create called with Label = %v, want \"Дом\"", fake.createInput.Label)
	}
	if respBody := rec.Body.String(); !strings.Contains(respBody, `"address_text"`) || !strings.Contains(respBody, `"is_default"`) {
		t.Errorf("body = %s, want snake_case JSON keys (storefront.Address has no json tags of its own)", respBody)
	}
}

func TestUpdateAddressHandlerScopesToCustomerAndPathID(t *testing.T) {
	fake := &fakeAddressStore{}
	handler := apperr.Wrap(updateAddressHandler(fake))

	body := `{"address_text":"новый адрес"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/addresses/addr-1", strings.NewReader(body))
	req.SetPathValue("id", "addr-1")
	req = withCustomer(req, "customer-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if fake.updateCustomerID != "customer-1" || fake.updateID != "addr-1" {
		t.Errorf("Update called with (%q, %q), want (customer-1, addr-1)", fake.updateCustomerID, fake.updateID)
	}
}

func TestUpdateAddressHandlerPropagatesNotFound(t *testing.T) {
	fake := &fakeAddressStore{updateErr: apperr.NotFound("address_not_found", "адрес не найден")}
	handler := apperr.Wrap(updateAddressHandler(fake))

	req := httptest.NewRequest(http.MethodPut, "/api/v1/addresses/other-customers-addr", strings.NewReader(`{"address_text":"x"}`))
	req.SetPathValue("id", "other-customers-addr")
	req = withCustomer(req, "customer-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (can't touch another customer's address by id): %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteAddressHandlerScopesToCustomerAndPathID(t *testing.T) {
	fake := &fakeAddressStore{}
	handler := apperr.Wrap(deleteAddressHandler(fake))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/addresses/addr-1", nil)
	req.SetPathValue("id", "addr-1")
	req = withCustomer(req, "customer-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if fake.deleteCustomerID != "customer-1" || fake.deleteID != "addr-1" {
		t.Errorf("Delete called with (%q, %q), want (customer-1, addr-1)", fake.deleteCustomerID, fake.deleteID)
	}
}
