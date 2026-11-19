//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/phonenorm"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// courierPhoneOf reads orders.courier_phone (nil = NULL).
func courierPhoneOf(t *testing.T, orderID string) *string {
	t.Helper()
	var p *string
	if err := testDB.QueryRowContext(ctxT(t), `SELECT courier_phone FROM orders WHERE id = $1`, orderID).Scan(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestOrderCourierPhone: the courier number from the mobile API and from
// the site checkout is normalized into orders.courier_phone, returned in
// the Order JSON and shown on the admin order page; a bad number is a 400
// and a pickup order stores none.
func TestOrderCourierPhone(t *testing.T) {
	t.Parallel()
	a := app(t)
	ctx := ctxT(t)
	f := newFixture(t, 10, 10)
	var phone string
	if err := testDB.QueryRowContext(ctx, `SELECT phone FROM customers WHERE id = $1`, f.CustomerID).Scan(&phone); err != nil {
		t.Fatal(err)
	}
	access := a.accessToken(t, ctx, phone)
	apiOrder := func(body string, wantCode int) map[string]json.RawMessage {
		t.Helper()
		hr := httptest.NewRequest(http.MethodPost, "/api/v1/orders", strings.NewReader(body))
		hr.Header.Set("Authorization", "Bearer "+access)
		hr.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		a.handler.ServeHTTP(w, hr)
		wantStatus(t, w, wantCode)
		var out map[string]json.RawMessage
		decodeJSON(t, w, &out)
		return out
	}

	// Mobile API, delivery: local form → canonical +996.
	o := apiOrder(`{"items":[{"variant_id":"`+f.VariantA+`","quantity":1}],"address_id":"`+f.AddressID+
		`","courier_phone":"0555 12-34-56"}`, http.StatusCreated)
	if string(o["courier_phone"]) != `"+996555123456"` {
		t.Errorf("API courier_phone = %s, want \"+996555123456\"", o["courier_phone"])
	}
	var apiID string
	_ = json.Unmarshal(o["id"], &apiID)
	if p := courierPhoneOf(t, apiID); p == nil || *p != "+996555123456" {
		t.Errorf("orders.courier_phone = %v", p)
	}

	// Invalid number → 400 invalid_courier_phone, nothing ordered.
	bad := apiOrder(`{"items":[{"variant_id":"`+f.VariantA+`","quantity":1}],"address_id":"`+f.AddressID+
		`","courier_phone":"+7 700 123 45 67"}`, http.StatusBadRequest)
	if !strings.Contains(string(bad["code"])+string(bad["error"]), "invalid_courier_phone") {
		t.Errorf("error body = %v, want invalid_courier_phone", bad)
	}

	// Older clients send none → NULL, JSON null.
	o = apiOrder(`{"items":[{"variant_id":"`+f.VariantA+`","quantity":1}],"address_id":"`+f.AddressID+`"}`, http.StatusCreated)
	if string(o["courier_phone"]) != "null" {
		t.Errorf("courier_phone without input = %s, want null", o["courier_phone"])
	}

	// Pickup ignores it.
	pickup := placePickupOrder(t, f)
	if p := courierPhoneOf(t, pickup.ID); p != nil {
		t.Errorf("pickup courier_phone = %s, want NULL", *p)
	}

	// Site checkout: the form is pre-filled with the account phone and
	// the submitted number lands on the order.
	c := []*http.Cookie{{Name: "cozy_session", Value: access}}
	wantRedirect(t, a.do(t, req{method: http.MethodPost, path: "/cart/items", form: url.Values{"variant_id": {f.VariantB}}, cookies: c}), "/cart")
	w := a.get(t, "/checkout", c...)
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, `name="courier_phone" value="`+strings.Replace(phonenorm.Display(phone), "+", "&#43;", 1)+`"`)

	w = a.do(t, req{method: http.MethodPost, path: "/checkout", cookies: c, form: url.Values{
		"fulfillment": {"delivery"}, "address_id": {f.AddressID}, "courier_phone": {"+996 700 99 88 77"},
	}})
	wantStatus(t, w, http.StatusSeeOther)
	var webID string
	if err := testDB.QueryRowContext(ctx,
		`SELECT id FROM orders WHERE customer_id = $1 ORDER BY created_at DESC LIMIT 1`, f.CustomerID).Scan(&webID); err != nil {
		t.Fatal(err)
	}
	if p := courierPhoneOf(t, webID); p == nil || *p != "+996700998877" {
		t.Fatalf("web order courier_phone = %v, want +996700998877", p)
	}

	// Admin sees it on the order page.
	owner, _ := a.newStaffSession(t, staff.RoleOwner, nil)
	r := a.get(t, "/admin/orders/"+webID, owner)
	wantStatus(t, r, http.StatusOK)
	wantBody(t, r, "Номер для курьера", `href="tel:&#43;996700998877"`, "&#43;996 700 998 877")
}
