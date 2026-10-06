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
