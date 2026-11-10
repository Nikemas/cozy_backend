//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

const (
	fixtureAddressLabel = "Дом"
	fixtureAddressText  = "г. Бишкек, ул. Тестовая, 10"
	editedAddressText   = "г. Бишкек, ул. Изменённая, 99"
)

// deliveryAddressJSON is the wire shape of Order.delivery_address.
type deliveryAddressJSON struct {
	Label       *string `json:"label"`
	AddressText string  `json:"address_text"`
}

type orderAddressJSON struct {
	ID              string               `json:"id"`
	AddressID       *string              `json:"address_id"`
	DeliveryAddress *deliveryAddressJSON `json:"delivery_address"`
}

// bearerGet runs an authenticated customer API GET and decodes the JSON.
func (a *testApp) bearerGet(t *testing.T, path, access string, v any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("Authorization", "Bearer "+access)
	w := httptest.NewRecorder()
	a.handler.ServeHTTP(w, r)
	wantStatus(t, w, http.StatusOK)
	decodeJSON(t, w, v)
}

func wantSnapshot(t *testing.T, where string, got *deliveryAddressJSON) {
	t.Helper()
	if got == nil || got.AddressText != fixtureAddressText || got.Label == nil || *got.Label != fixtureAddressLabel {
		t.Errorf("%s: delivery_address = %+v, want {%s, %s}", where, got, fixtureAddressLabel, fixtureAddressText)
	}
}

// TestOrderAddressSnapshot: a delivery order keeps the address text/label
// it was placed with, in the DB and in every Order JSON (customer API,
// admin API) and on the admin order page, after the customer edits the
// saved address. A pickup order carries delivery_address: null.
func TestOrderAddressSnapshot(t *testing.T) {
	t.Parallel()
	a := app(t)
	ctx := ctxT(t)
	f := newFixture(t, 5, 5)
	svc, _ := newService()

	o, err := svc.CreateOrder(ctx, f.CustomerID,
		[]orders.OrderItemInput{{VariantID: f.VariantA, Quantity: 1}}, strptr(f.AddressID), nil)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if o.DeliveryAddress == nil || o.DeliveryAddress.AddressText != fixtureAddressText {
		t.Fatalf("created order DeliveryAddress = %+v, want snapshot", o.DeliveryAddress)
	}
	pickup := placePickupOrder(t, f)

	var dbLabel, dbText *string
	if err := testDB.QueryRowContext(ctx,
		`SELECT delivery_address_label, delivery_address_text FROM orders WHERE id = $1`, o.ID).Scan(&dbLabel, &dbText); err != nil {
		t.Fatal(err)
	}
	if dbLabel == nil || *dbLabel != fixtureAddressLabel || dbText == nil || *dbText != fixtureAddressText {
		t.Fatalf("orders row snapshot = %v/%v, want %s/%s", dbLabel, dbText, fixtureAddressLabel, fixtureAddressText)
	}

	// The customer edits the saved address through the API.
	var phone string
	if err := testDB.QueryRowContext(ctx, `SELECT phone FROM customers WHERE id = $1`, f.CustomerID).Scan(&phone); err != nil {
		t.Fatal(err)
	}
	access := a.accessToken(t, ctx, phone)
	put := httptest.NewRequest(http.MethodPut, "/api/v1/addresses/"+f.AddressID,
		strings.NewReader(`{"label":"Работа","address_text":"`+editedAddressText+`","is_default":true}`))
	put.Header.Set("Authorization", "Bearer "+access)
	put.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.handler.ServeHTTP(w, put)
	wantStatus(t, w, http.StatusOK)

	var one orderAddressJSON
	a.bearerGet(t, "/api/v1/orders/"+o.ID, access, &one)
	wantSnapshot(t, "GET /api/v1/orders/{id}", one.DeliveryAddress)
	if one.AddressID == nil || *one.AddressID != f.AddressID {
		t.Errorf("address_id = %v, want %s", one.AddressID, f.AddressID)
	}

	var list []orderAddressJSON
	a.bearerGet(t, "/api/v1/orders", access, &list)
	seen := 0
	for _, it := range list {
		switch it.ID {
		case o.ID:
			seen++
			wantSnapshot(t, "GET /api/v1/orders (delivery)", it.DeliveryAddress)
		case pickup.ID:
			seen++
			if it.DeliveryAddress != nil {
				t.Errorf("pickup order delivery_address = %+v, want null", it.DeliveryAddress)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("GET /api/v1/orders listed %d of the 2 orders", seen)
	}

	// The key is present (null), not omitted, for pickup.
	var raw map[string]json.RawMessage
	a.bearerGet(t, "/api/v1/orders/"+pickup.ID, access, &raw)
	if v, ok := raw["delivery_address"]; !ok || string(v) != "null" {
		t.Errorf("pickup delivery_address = %s (present=%v), want null", v, ok)
	}

	owner, _ := a.newStaffSession(t, staff.RoleOwner, nil)
	r := a.get(t, "/admin/api/orders/"+o.ID, owner)
	wantStatus(t, r, http.StatusOK)
	var adm orderAddressJSON
	decodeJSON(t, r, &adm)
	wantSnapshot(t, "GET /admin/api/orders/{id}", adm.DeliveryAddress)

	r = a.get(t, "/admin/orders/"+o.ID, owner)
	wantStatus(t, r, http.StatusOK)
	wantBody(t, r, fixtureAddressLabel+": "+fixtureAddressText)
	if strings.Contains(r.Body.String(), editedAddressText) {
		t.Errorf("admin order page shows the edited live address, want the snapshot")
	}
}

// TestOrderAddressSnapshotFallback: an order without a snapshot (placed
// before migration 000045 with its address gone from the backfill's
// reach) still shows the live address on the admin page, and
// delivery_address is null in the JSON.
func TestOrderAddressSnapshotFallback(t *testing.T) {
	t.Parallel()
	a := app(t)
	ctx := ctxT(t)
	f := newFixture(t, 5, 5)
	svc, _ := newService()

	o, err := svc.CreateOrder(ctx, f.CustomerID,
		[]orders.OrderItemInput{{VariantID: f.VariantA, Quantity: 1}}, strptr(f.AddressID), nil)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if _, err := testDB.ExecContext(ctx,
		`UPDATE orders SET delivery_address_label = NULL, delivery_address_text = NULL WHERE id = $1`, o.ID); err != nil {
		t.Fatal(err)
	}

	owner, _ := a.newStaffSession(t, staff.RoleOwner, nil)
	r := a.get(t, "/admin/api/orders/"+o.ID, owner)
	wantStatus(t, r, http.StatusOK)
	var adm orderAddressJSON
	decodeJSON(t, r, &adm)
	if adm.DeliveryAddress != nil {
		t.Errorf("delivery_address = %+v, want null without a snapshot", adm.DeliveryAddress)
	}

	r = a.get(t, "/admin/orders/"+o.ID, owner)
	wantStatus(t, r, http.StatusOK)
	wantBody(t, r, fixtureAddressLabel+": "+fixtureAddressText)
}
