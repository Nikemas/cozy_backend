package admin

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

func renderOrderDetail(t *testing.T, role staff.Role) string {
	t.Helper()
	rr := newTestRenderer(t)
	st := &staff.Staff{ID: "s", Name: "Айгерим Б.", Role: role, IsActive: true}
	pd := PageData{
		Screen: "order_detail", PageTitle: "Заказ", ShowSidebar: true, Staff: st,
		NavItems: navItemsForRole(role, "orders"),
		Data:     OrderDetailData{ID: "o1", Number: "COZY-1", StatusButtons: buildStatusButtons(ruTr, orders.StatusPlaced, role)},
	}
	w := httptest.NewRecorder()
	if err := rr.Render(w, "order_detail", pd); err != nil {
		t.Fatal(err)
	}
	return w.Body.String()
}

// Cancelling an order is irreversible: its button must go through the
// confirm dialog; the other status buttons must not.
func TestOrderDetailCancelNeedsConfirmation(t *testing.T) {
	body := renderOrderDetail(t, staff.RoleOwner)
	if strings.Count(body, "data-confirm=") != 1 || !strings.Contains(body, `data-confirm-title="Отменить заказ?"`) {
		t.Errorf("want exactly one confirm (cancel), got %d", strings.Count(body, "data-confirm="))
	}
	if body := renderOrderDetail(t, staff.RoleManager); strings.Contains(body, "data-confirm=") {
		t.Error("manager has no cancel button, so no confirm either")
	}
}

func TestSharedModalsAreAccessibleDialogs(t *testing.T) {
	body := renderOrderDetail(t, staff.RoleOwner)
	for _, want := range []string{
		`role="alertdialog" aria-modal="true" aria-labelledby="admin-confirm-title"`,
		`id="admin-confirm-cancel" data-modal-close`,
		"form[data-confirm]",   // submit interception
		"evt.key === 'Escape'", // Esc closes
		"evt.key !== 'Tab'",    // focus trap
		"openers.set(backdrop", // focus return
	} {
		if !strings.Contains(body, want) {
			t.Errorf("layout missing %q", want)
		}
	}
}
