//go:build integration

package integration

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/staff"
)

// fix/admin-ux-followups: a link carrying free text in ?toast= (or a bulk
// result) must not make the panel show it to staff — only known keys
// render, as fixed locale text.
func TestAdminToastIgnoresCraftedText(t *testing.T) {
	t.Parallel()
	a := app(t)
	f := newFixture(t, 1, 1)
	o := placePickupOrder(t, f)
	owner, _ := a.newStaffSession(t, staff.RoleOwner, nil)

	const phish = "PHISH-MARKER войдите на evil.test"
	crafted := "?" + url.Values{
		"toast": {phish}, "toast_code": {phish}, "toast_st": {phish},
		"bulk_fail": {phish, "COZY-1~" + phish}, "status_error": {phish},
	}.Encode()
	for _, path := range []string{"/admin/products", "/admin/orders", "/admin/orders/" + o.ID, "/admin/stock", "/admin/points", "/admin/categories"} {
		w := a.get(t, path+crafted, owner)
		wantStatus(t, w, http.StatusOK)
		if strings.Contains(w.Body.String(), "PHISH-MARKER") {
			b := w.Body.String()
			i := strings.Index(b, "PHISH-MARKER")
			t.Errorf("%s rendered the crafted text: …%s…", path, b[max(0, i-200):min(len(b), i+60)])
		}
	}

	w := a.get(t, "/admin/products?toast=product_saved", owner)
	wantBody(t, w, "Товар сохранён")
	w = a.get(t, "/admin/orders/"+o.ID+"?toast=order_status&toast_st=confirmed", owner)
	wantBody(t, w, "Статус заказа: Подтверждён")
}

// The «Заказы» badge endpoint counts only actionable orders of the
// viewer's point: an online-card order still waiting for payment is left
// out (and marked «Ожидает оплаты» on the list), a paid one counts.
func TestAdminOrdersBadgeSkipsUnpaidOnline(t *testing.T) {
	t.Parallel()
	a := app(t)
	f := newFixture(t, 5, 5)
	cash := placePickupOrder(t, f)
	online := placePickupOrder(t, f)
	if _, err := testDB.ExecContext(ctxT(t),
		`UPDATE orders SET payment_method = 'online_card', payment_status = 'pending' WHERE id = $1`, online.ID); err != nil {
		t.Fatal(err)
	}
	seller, _ := a.newStaffSession(t, staff.RolePointStaff, strptr(f.PointA))

	w := a.get(t, "/admin/orders/badge", seller)
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, `id="admin-orders-badge"`, `data-count="1"`, `hx-swap-oob="outerHTML"`)
	if strings.Contains(w.Body.String(), "<html") {
		t.Error("badge endpoint returned a full page")
	}

	w = a.get(t, "/admin/orders?status=placed", seller)
	wantBody(t, w, cash.OrderNumber, online.OrderNumber, "Ожидает оплаты")

	if _, err := testDB.ExecContext(ctxT(t), `UPDATE orders SET payment_status = 'paid' WHERE id = $1`, online.ID); err != nil {
		t.Fatal(err)
	}
	wantBody(t, a.get(t, "/admin/orders/badge", seller), `data-count="2"`)

	if w := a.get(t, "/admin/orders/badge"); w.Code == http.StatusOK && strings.Contains(w.Body.String(), "data-count") {
		t.Error("guest got a count")
	}
}
