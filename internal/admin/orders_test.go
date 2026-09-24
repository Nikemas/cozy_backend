package admin

import (
	"net/http/httptest"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// --- pure helpers: no DB, no *handlers receiver ---

func TestOrderStatusMetaForCoversEveryKnownStatus(t *testing.T) {
	cases := []orders.OrderStatus{
		orders.StatusPlaced, orders.StatusConfirmed, orders.StatusCourierAssigned,
		orders.StatusDelivered, orders.StatusCancelled,
	}
	seen := map[string]bool{}
	for _, s := range cases {
		m := orderStatusMetaFor(ruTr, s)
		if m.Label == "" || m.Class == "" {
			t.Errorf("orderStatusMetaFor(%q) = %+v, want non-empty Label/Class", s, m)
		}
		if seen[m.Class] {
			t.Errorf("orderStatusMetaFor(%q).Class %q collides with another status", s, m.Class)
		}
		seen[m.Class] = true
	}
}

func TestOrderStatusMetaForFallsBackOnUnknownStatus(t *testing.T) {
	m := orderStatusMetaFor(ruTr, orders.OrderStatus("some_future_status"))
	if m.Label == "" || m.Class == "" {
		t.Fatalf("orderStatusMetaFor(unknown) = %+v, want a non-empty fallback", m)
	}
}

func TestPaymentLabel(t *testing.T) {
	if got := paymentLabel(ruTr, orders.PaymentCashOnDelivery, nil); got != "При получении" {
		t.Errorf("paymentLabel(cash_on_delivery) = %q", got)
	}
	cases := map[orders.PaymentStatus]string{
		orders.PaymentPaid:      "Онлайн, оплачено",
		orders.PaymentPending:   "Онлайн, ожидает оплаты",
		orders.PaymentFailed:    "Онлайн, оплата не прошла",
		orders.PaymentCancelled: "Онлайн, оплата отменена",
		orders.PaymentRefunded:  "Онлайн, возврат",
	}
	for st, want := range cases {
		st := st
		if got := paymentLabel(ruTr, orders.PaymentOnlineCard, &st); got != want {
			t.Errorf("paymentLabel(online_card, %s) = %q, want %q", st, got, want)
		}
	}
}

func TestFormatSom(t *testing.T) {
	cases := []struct {
		amount float64
		want   string
	}{
		{0, "0 сом"},
		{200, "200 сом"},
		{7900, "7 900 сом"},
		{1234567, "1 234 567 сом"},
		{4999.6, "5 000 сом"},
	}
	for _, c := range cases {
		if got := formatSom(c.amount); got != c.want {
			t.Errorf("formatSom(%v) = %q, want %q", c.amount, got, c.want)
		}
	}
}

func TestPluralRu(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{1, "заказ"}, {21, "заказ"}, {101, "заказ"},
		{2, "заказа"}, {3, "заказа"}, {4, "заказа"}, {22, "заказа"},
		{0, "заказов"}, {5, "заказов"}, {11, "заказов"}, {12, "заказов"}, {100, "заказов"},
	}
	for _, c := range cases {
		if got := pluralRu(c.n, "заказ", "заказа", "заказов"); got != c.want {
			t.Errorf("pluralRu(%d, ...) = %q, want %q", c.n, got, c.want)
		}
	}
}

// nextStatusOptions mirrors internal/orders/order.go's unexported
// validStatusTransition (see orders.go's doc comment on nextStatusOptions
// for why it's duplicated) — this test pins it against the exact table
// documented in that source function's own comment, so the two can't
// silently drift without a test failing here.
func TestNextStatusOptionsMatchesOrderStateMachine(t *testing.T) {
	cases := []struct {
		from orders.OrderStatus
		want []orders.OrderStatus
	}{
		{orders.StatusPlaced, []orders.OrderStatus{orders.StatusConfirmed, orders.StatusCancelled}},
		{orders.StatusConfirmed, []orders.OrderStatus{orders.StatusCourierAssigned, orders.StatusCancelled}},
		{orders.StatusCourierAssigned, []orders.OrderStatus{orders.StatusDelivered, orders.StatusCancelled}},
		{orders.StatusDelivered, nil},
		{orders.StatusCancelled, nil},
	}
	for _, c := range cases {
		got := nextStatusOptions(c.from)
		if len(got) != len(c.want) {
			t.Fatalf("nextStatusOptions(%q) = %v, want %v", c.from, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("nextStatusOptions(%q) = %v, want %v", c.from, got, c.want)
			}
		}
	}
}

func TestBuildStatusButtonsOwnerSeesCancel(t *testing.T) {
	buttons := buildStatusButtons(ruTr, orders.StatusPlaced, staff.RoleOwner)
	if len(buttons) != 2 {
		t.Fatalf("owner: got %d buttons, want 2: %+v", len(buttons), buttons)
	}
	foundCancel := false
	for _, b := range buttons {
		if b.Value == string(orders.StatusCancelled) {
			foundCancel = true
		}
	}
	if !foundCancel {
		t.Errorf("owner: expected a cancel button among %+v", buttons)
	}
}

// TestBuildStatusButtonsManagerNeverSeesCancel is the pinned UI-level
// restriction from the Wave 4 Task 3 brief: manager can advance an order
// through the state machine but never cancel it, even though
// internal/orders.Service.AdminUpdateStatus itself would allow the
// transition — see buildStatusButtons' doc comment for the full
// rationale and the accepted backend gap.
func TestBuildStatusButtonsManagerNeverSeesCancel(t *testing.T) {
	for _, from := range []orders.OrderStatus{orders.StatusPlaced, orders.StatusConfirmed, orders.StatusCourierAssigned} {
		buttons := buildStatusButtons(ruTr, from, staff.RoleManager)
		for _, b := range buttons {
			if b.Value == string(orders.StatusCancelled) {
				t.Errorf("manager, from %q: got a cancel button among %+v, want none", from, buttons)
			}
		}
		// Exactly the non-cancel transitions should remain.
		want := 0
		for _, s := range nextStatusOptions(from) {
			if s != orders.StatusCancelled {
				want++
			}
		}
		if len(buttons) != want {
			t.Errorf("manager, from %q: got %d buttons, want %d: %+v", from, len(buttons), want, buttons)
		}
	}
}

func TestBuildStatusButtonsTerminalStatusHasNone(t *testing.T) {
	for _, from := range []orders.OrderStatus{orders.StatusDelivered, orders.StatusCancelled} {
		for _, role := range []staff.Role{staff.RoleOwner, staff.RoleManager} {
			if got := buildStatusButtons(ruTr, from, role); len(got) != 0 {
				t.Errorf("buildStatusButtons(%q, %q) = %+v, want none (terminal status)", from, role, got)
			}
		}
	}
}

func TestOrdersListURLOmitsDefaults(t *testing.T) {
	if got := ordersListURL("", "all", 1); got != "/admin/orders" {
		t.Errorf("ordersListURL defaults = %q, want /admin/orders", got)
	}
	got := ordersListURL(string(orders.StatusConfirmed), "30", 2)
	want := "/admin/orders?page=2&range=30&status=confirmed"
	if got != want {
		t.Errorf("ordersListURL(...) = %q, want %q", got, want)
	}
}

// --- template rendering: exercises orders.gohtml / order_detail.gohtml
// end to end with hand-built view models, the same "static render in a
// test" substitute for a live-DB manual check that render_exec_test.go
// already uses for the other screens. ---

func TestRenderOrdersListExecutes(t *testing.T) {
	rr := newTestRenderer(t)
	owner := &staff.Staff{ID: "s1", Name: "Айгерим Б.", Role: staff.RoleOwner, IsActive: true}

	cases := []struct {
		name string
		data OrdersListData
	}{
		{
			name: "with rows",
			data: OrdersListData{
				StatusChips:  []StatusChipLink{{Label: "Все", URL: "/admin/orders", Class: "admin-chip--all", Active: true}},
				RangeOptions: []RangeOptionLink{{Value: "all", Label: "Весь период", URL: "/admin/orders", Selected: true}},
				Rows: []OrderRowView{{
					URL: "/admin/orders/o1", Number: "COZY-20260101-001", DateLabel: "01.01.2026",
					Phone: "+996555123456", ItemsCount: 2, ItemsLabel: "2 товара", TotalLabel: "7 900 сом",
					PaymentLabel: "При получении", StatusLabel: "Оформлен", StatusClass: "admin-chip--placed",
				}},
				CountLabel: "1 заказ",
			},
		},
		{
			name: "empty",
			data: OrdersListData{
				StatusChips:  []StatusChipLink{{Label: "Все", URL: "/admin/orders", Class: "admin-chip--all", Active: true}},
				RangeOptions: []RangeOptionLink{{Value: "all", Label: "Весь период", URL: "/admin/orders", Selected: true}},
				Empty:        true,
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pageData := PageData{
				Screen: "orders", PageTitle: "Заказы", ShowSidebar: true,
				Staff: owner, Initials: initialsFor(owner.Name), RoleLabel: roleLabel(owner.Role),
				NavItems: navItemsForRole(owner.Role, "orders"),
				Data:     c.data,
			}
			w := httptest.NewRecorder()
			if err := rr.Render(w, "orders", pageData); err != nil {
				t.Fatalf("Render: %v", err)
			}
			if w.Code != 200 {
				t.Fatalf("status = %d, want 200", w.Code)
			}
		})
	}
}

func TestRenderOrderDetailExecutes(t *testing.T) {
	rr := newTestRenderer(t)
	manager := &staff.Staff{ID: "s2", Name: "Данияр К.", Role: staff.RoleManager, IsActive: true}

	data := OrderDetailData{
		ID: "o1", Number: "COZY-20260101-001", DateLabel: "01.01.2026 12:00",
		StatusLabel: "Оформлен", StatusClass: "admin-chip--placed",
		Phone: "+996555123456", PaymentLabel: "При получении",
		AddressText: "г. Бишкек, ул. Киевская 95", Comment: "—",
		Items: []OrderDetailItemView{
			{Name: "Nike Air Max 90", Variant: "Размер 42, чёрный", Qty: 1, PriceLabel: "6 500 сом"},
		},
		TotalLabel:    "6 500 сом",
		StatusButtons: buildStatusButtons(ruTr, orders.StatusPlaced, manager.Role),
	}

	pageData := PageData{
		Screen: "order_detail", PageTitle: "Заказ COZY-20260101-001", ShowSidebar: true, ShowBack: true,
		Staff: manager, Initials: initialsFor(manager.Name), RoleLabel: roleLabel(manager.Role),
		NavItems: navItemsForRole(manager.Role, "orders"),
		Data:     data,
	}
	w := httptest.NewRecorder()
	if err := rr.Render(w, "order_detail", pageData); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

// TestRenderOrderDetailExecutesWithNoStatusButtons covers a terminal
// order (delivered/cancelled): StatusButtons is empty, and
// order_detail.gohtml's {{if .Data.StatusButtons}} guard must skip the
// "Сменить статус" card entirely rather than rendering an empty one.
func TestRenderOrderDetailExecutesWithNoStatusButtons(t *testing.T) {
	rr := newTestRenderer(t)
	owner := &staff.Staff{ID: "s1", Name: "Айгерим Б.", Role: staff.RoleOwner, IsActive: true}

	data := OrderDetailData{
		ID: "o2", Number: "COZY-20260101-002", DateLabel: "01.01.2026 12:00",
		StatusLabel: "Доставлен", StatusClass: "admin-chip--delivered",
		Phone: "+996555123456", PaymentLabel: "При получении",
		AddressText: "Самовывоз: Центральный, ул. Советская 1", Comment: "—",
		TotalLabel:    "0 сом",
		StatusButtons: nil,
	}

	pageData := PageData{
		Screen: "order_detail", PageTitle: "Заказ COZY-20260101-002", ShowSidebar: true, ShowBack: true,
		Staff: owner, Initials: initialsFor(owner.Name), RoleLabel: roleLabel(owner.Role),
		NavItems: navItemsForRole(owner.Role, "orders"),
		Data:     data,
	}
	w := httptest.NewRecorder()
	if err := rr.Render(w, "order_detail", pageData); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}
