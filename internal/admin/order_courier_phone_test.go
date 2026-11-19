package admin

import (
	"context"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/storefront"
)

func TestOrderDetailViewCarriesCourierPhone(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	h := &handlers{
		orderMeta: &fakeOrderListMeta{},
		customers: storefront.NewCustomerRepo(db),
		addresses: storefront.NewAddressRepo(db),
		cfg:       &config.Config{},
	}
	phone := "+996555123456"
	view := h.buildOrderDetailView(context.Background(), &orders.Order{ID: "o1", CourierPhone: &phone}, "owner")
	if view.CourierContact.Raw != phone || view.CourierContact.Display != "+996 555 123 456" {
		t.Errorf("CourierContact = %+v", view.CourierContact)
	}

	view = h.buildOrderDetailView(context.Background(), &orders.Order{ID: "o2"}, "owner")
	if view.CourierContact.Tel != "" {
		t.Errorf("no courier phone → empty contact, got %+v", view.CourierContact)
	}
}

func TestOrderDetailRendersCourierPhone(t *testing.T) {
	body := renderOrderDetailUX(t, OrderDetailData{
		ID: "o1", Number: "COZY-1", AddressText: "ул. Киевская, 5",
		CourierContact: phoneContact("+996555123456"),
	}, "")
	for _, want := range []string{
		"Номер для курьера",
		`href="tel:&#43;996555123456"`,
		"&#43;996 555 123 456",
		`data-copy="&#43;996555123456"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("order detail missing %q", want)
		}
	}

	body = renderOrderDetailUX(t, OrderDetailData{ID: "o1", Number: "COZY-1"}, "")
	if strings.Contains(body, "Номер для курьера") {
		t.Error("courier phone row shown for an order without one")
	}
}
