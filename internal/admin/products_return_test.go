package admin

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/staff"
)

func TestWithRowReturnAddsBackToDelete(t *testing.T) {
	rows := []ProductRowVM{{ID: "p1", DeleteURL: "/admin/products/p1/delete"}}
	got := withRowReturn(rows, "/admin/products?cat=men&page=2")
	if got[0].DeleteURL != "/admin/products/p1/delete?back=%2Fadmin%2Fproducts%3Fcat%3Dmen%26page%3D2" {
		t.Errorf("DeleteURL = %q", got[0].DeleteURL)
	}
	if rows[0].DeleteURL != "/admin/products/p1/delete" {
		t.Error("input rows mutated")
	}
	if got := withRowReturn(rows, productsListPath); got[0].DeleteURL != "/admin/products/p1/delete" {
		t.Errorf("plain list: %q", got[0].DeleteURL)
	}
}

func TestProductsRowMenuKeepsListPosition(t *testing.T) {
	rr := newTestRenderer(t)
	owner := &staff.Staff{ID: "s1", Name: "Owner", Role: staff.RoleOwner, IsActive: true}
	h := &handlers{}
	pd := h.productsShellData("products", "admin.nav.products", owner)
	pd.ShowSearch = true
	pd.Data = ProductsPageData{
		CanEdit: true, CanDelete: true, ReturnURL: "/admin/products?cat=men&page=2",
		Products: []ProductRowVM{{ID: "p1", Name: "Shoe", EditURL: "/admin/products/p1", ToggleActiveURL: "/admin/products/p1/toggle-active", DeactivateLabel: "Деактивировать", DeleteURL: "/admin/products/p1/delete"}},
	}
	w := httptest.NewRecorder()
	if err := rr.Render(w, "products", pd); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := w.Body.String()
	i := strings.Index(body, `action="/admin/products/p1/toggle-active"`)
	if i < 0 || !strings.Contains(body[i:i+300], `name="back" value="/admin/products?cat=men&amp;page=2"`) {
		t.Error("row toggle form doesn't send the list position back")
	}
	// Header search keeps the current filters (only q, page and toasts change).
	if !strings.Contains(body, "new URL(window.location.href)") {
		t.Error("header search starts from a blank URL")
	}
}
