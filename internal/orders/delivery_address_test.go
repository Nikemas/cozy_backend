package orders

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// TestOrderDeliveryAddressJSON pins the wire shape the mobile app reads:
// "delivery_address": {"label": string|null, "address_text": string} | null,
// always present (never omitted), next to the unchanged address_id.
func TestOrderDeliveryAddressJSON(t *testing.T) {
	addr := "addr-1"
	cases := []struct {
		name string
		addr *OrderDeliveryAddress
		want string
	}{
		{"pickup / no snapshot", nil, `null`},
		{"with label", &OrderDeliveryAddress{Label: strPtr("Дом"), AddressText: "ул. Киевская, 5"},
			`{"label":"Дом","address_text":"ул. Киевская, 5"}`},
		{"without label", &OrderDeliveryAddress{AddressText: "ул. Киевская, 5"},
			`{"label":null,"address_text":"ул. Киевская, 5"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := json.Marshal(Order{AddressID: &addr, DeliveryAddress: c.addr})
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			raw, ok := got["delivery_address"]
			if !ok {
				t.Fatalf("delivery_address missing from %s", b)
			}
			if string(raw) != c.want {
				t.Errorf("delivery_address = %s, want %s", raw, c.want)
			}
			if string(got["address_id"]) != `"addr-1"` {
				t.Errorf("address_id = %s, want \"addr-1\"", got["address_id"])
			}
		})
	}
}

func TestDeliveryAddressFromSnapshot(t *testing.T) {
	ns := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	cases := []struct {
		name        string
		label, text sql.NullString
		want        *OrderDeliveryAddress
	}{
		{"no snapshot", sql.NullString{}, sql.NullString{}, nil},
		{"label without text is no snapshot", ns("Дом"), sql.NullString{}, nil},
		{"text only", sql.NullString{}, ns("ул. Киевская, 5"), &OrderDeliveryAddress{AddressText: "ул. Киевская, 5"}},
		{"blank label is null", ns("  "), ns("ул. Киевская, 5"), &OrderDeliveryAddress{AddressText: "ул. Киевская, 5"}},
		{"both", ns("Дом"), ns("ул. Киевская, 5"), &OrderDeliveryAddress{Label: strPtr("Дом"), AddressText: "ул. Киевская, 5"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := deliveryAddressFromSnapshot(c.label, c.text)
			switch {
			case c.want == nil && got != nil:
				t.Errorf("got %+v, want nil", got)
			case c.want != nil && got == nil:
				t.Errorf("got nil, want %+v", c.want)
			case c.want != nil:
				if got.AddressText != c.want.AddressText || (got.Label == nil) != (c.want.Label == nil) ||
					(got.Label != nil && *got.Label != *c.want.Label) {
					t.Errorf("got %+v, want %+v", got, c.want)
				}
			}
		})
	}
}

// TestGetOrderReadsAddressSnapshot: reads fill DeliveryAddress from the
// orders row's snapshot columns, not from customer_addresses.
func TestGetOrderReadsAddressSnapshot(t *testing.T) {
	svc, mock := newMockService(t)
	row := codOrder(StatusPlaced)
	row.address, row.addrLabel, row.addrText = "addr-1", nil, "ул. Ахунбаева, 1"
	mock.ExpectQuery(regexp.QuoteMeta("FROM orders WHERE customer_id = $1")).WillReturnRows(row.rows())
	expectNoItems(mock)

	o, err := svc.GetOrder(context.Background(), "cust-1", "order-1")
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if o.DeliveryAddress == nil || o.DeliveryAddress.AddressText != "ул. Ахунбаева, 1" || o.DeliveryAddress.Label != nil {
		t.Errorf("DeliveryAddress = %+v, want {nil, ул. Ахунбаева, 1}", o.DeliveryAddress)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// TestCreateOrderUnknownAddress: an address that isn't the customer's
// stops the order before anything is written.
func TestCreateOrderUnknownAddress(t *testing.T) {
	svc, mock := newMockService(t)
	svc.WithSettings(Settings{DeliveryFee: 200})
	expectOrderPrologue(mock, -1)
	mock.ExpectQuery(regexp.QuoteMeta("FROM customer_addresses")).
		WithArgs("addr-1", "cust-1").WillReturnRows(sqlmock.NewRows(addressColumns))
	mock.ExpectRollback()

	addr := "addr-1"
	_, _, err := svc.PlaceOrder(context.Background(), PlaceOrderInput{
		CustomerID: "cust-1", Items: []OrderItemInput{{VariantID: testVar1, Quantity: 1}}, AddressID: &addr,
	})
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) || appErr.Code != "address_not_found" {
		t.Fatalf("err = %v, want address_not_found", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
