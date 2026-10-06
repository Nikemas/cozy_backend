package admin

import (
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

func TestMapSearchURL(t *testing.T) {
	cases := map[string]string{
		"ул. Киевская, 5/1": "https://2gis.kg/bishkek/search/%D1%83%D0%BB.%20%D0%9A%D0%B8%D0%B5%D0%B2%D1%81%D0%BA%D0%B0%D1%8F%2C%205%2F1",
		"  Ахунбаева 1  ":   "https://2gis.kg/bishkek/search/%D0%90%D1%85%D1%83%D0%BD%D0%B1%D0%B0%D0%B5%D0%B2%D0%B0%201",
		"":                  "",
		"   ":               "",
	}
	for in, want := range cases {
		if got := mapSearchURL(in); got != want {
			t.Errorf("mapSearchURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOrderDetailDeliveryMapLink(t *testing.T) {
	body := renderOrderDetailUX(t, OrderDetailData{
		ID: "o1", Number: "COZY-1", AddressText: "Дом: Ахунбаева 1", MapURL: mapSearchURL("Ахунбаева 1"),
	}, "")
	for _, want := range []string{
		`href="https://2gis.kg/bishkek/search/%D0%90%D1%85%D1%83%D0%BD%D0%B1%D0%B0%D0%B5%D0%B2%D0%B0%201"`,
		`target="_blank"`, `rel="noopener noreferrer"`, "На карте",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("detail missing %q", want)
		}
	}
	if body := renderOrderDetailUX(t, OrderDetailData{ID: "o1", Number: "COZY-1", AddressText: "—"}, ""); strings.Contains(body, "2gis.kg") {
		t.Error("map link shown without a delivery address")
	}
}

func TestOrderDetailActionsAndHistoryLayout(t *testing.T) {
	d := OrderDetailData{
		ID: "o1", Number: "COZY-1",
		StatusButtons: buildStatusButtons(ruTr, orders.StatusPlaced, staff.RoleOwner),
	}
	d.History = []OrderHistoryRowView{{DateLabel: "01.10.2026 10:00", ToLabel: "Оформлен", Actor: "Покупатель"}}
	body := renderOrderDetailUX(t, d, "")
	for _, want := range []string{`admin-order-detail__actions`, `admin-order-detail__history-table`} {
		if !strings.Contains(body, want) {
			t.Errorf("detail missing %q", want)
		}
	}
}
