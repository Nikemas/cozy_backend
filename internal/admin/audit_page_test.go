package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nikemas/cozy_backend/internal/audit"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

type fakeAuditLister struct {
	filter audit.Filter
	rows   []audit.Row
	total  int
}

func (f *fakeAuditLister) List(_ context.Context, filter audit.Filter) ([]audit.Row, int, error) {
	f.filter = filter
	return f.rows, f.total, nil
}

func TestResolveAuditFilter(t *testing.T) {
	p := auditParams{Staff: uuid1, Entity: "product", From: "2026-09-01", To: "2026-09-24", Q: "abc", Page: 2}
	f, msg := resolveAuditFilter(ruTr, &p)
	if msg != "" || f.EntityType != "product" || f.StaffID != uuid1 || f.EntityQuery != "abc" || f.Page != 2 {
		t.Fatalf("filter = %+v msg=%q", f, msg)
	}
	if f.From.Format("2006-01-02 15:04 MST") != "2026-09-01 00:00 +06" || f.To.Format("2006-01-02") != "2026-09-25" {
		t.Errorf("range = %v .. %v, want Bishkek days with an exclusive end", f.From, f.To)
	}

	p = auditParams{Entity: "bogus", From: "01.09.2026", Page: 1}
	f, msg = resolveAuditFilter(ruTr, &p)
	if f.EntityType != "" || p.Entity != "" || !f.From.IsZero() || msg == "" {
		t.Errorf("bad input not dropped: %+v %q", f, msg)
	}
}

func TestAuditRowVM(t *testing.T) {
	at := time.Date(2026, 9, 24, 8, 30, 0, 0, time.UTC)
	vm := auditRowVM(ruTr, audit.Row{At: at, StaffName: "Айгерим", Action: audit.ActionOrderStatus, EntityType: audit.EntityOrder,
		EntityID: "o1", Summary: "Заказ COZY-1", Details: map[string]any{"from": "placed", "to": "confirmed"}})
	if vm.When != "24.09.2026 14:30" || vm.EntityURL != "/admin/orders/o1" || vm.Summary != "Заказ COZY-1: Оформлен → Подтверждён" {
		t.Errorf("order vm = %+v", vm)
	}

	vm = auditRowVM(ruTr, audit.Row{At: at, Action: audit.ActionStockUpdate, EntityType: audit.EntityStock, EntityID: "v1",
		Summary: "Остаток …", Details: map[string]any{"product_id": "p1", "point": "Дордой",
			"quantity": map[string]any{"from": 3.0, "to": 2.0}}})
	if vm.EntityURL != "/admin/products/p1" || vm.Staff != "—" || vm.ActionLabel != "Изменение остатка" {
		t.Errorf("stock vm = %+v", vm)
	}
	if vm.DetailsText != "точка: Дордой; количество: 3 → 2" {
		t.Errorf("details = %q", vm.DetailsText)
	}
}

func TestAuditPageRenders(t *testing.T) {
	lister := &fakeAuditLister{total: 51, rows: []audit.Row{{
		ID: "a1", At: time.Now(), StaffName: "Айгерим", Action: audit.ActionProductUpdate, EntityType: audit.EntityProduct,
		EntityID: uuid1, Summary: "Изменён товар «Кеды»: цена", IP: "203.0.113.7",
		Details: map[string]any{"base_price": map[string]any{"from": 4000.0, "to": 4500.0}},
	}}}
	h := &handlers{render: newTestRenderer(t), auditList: lister}
	r := requestAs(http.MethodGet, "/admin/audit?entity=product&q=1111&page=1", &staff.Staff{ID: "s1", Name: "Айгерим", Role: staff.RoleOwner}, nil)
	w := httptest.NewRecorder()
	h.auditPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	if lister.filter.EntityType != "product" || lister.filter.EntityQuery != "1111" || lister.filter.PageSize != auditPageSize {
		t.Errorf("filter = %+v", lister.filter)
	}
	body := w.Body.String()
	for _, want := range []string{
		"Журнал действий", "Изменён товар «Кеды»: цена", "цена: 4000 → 4500", "203.0.113.7",
		`href="/admin/products/` + uuid1 + `"`, "51 запись", "Страница 1 из 2", `href="/admin/audit?entity=product&amp;page=2&amp;q=1111"`,
		`<option value="product" selected>Товар</option>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q", want)
		}
	}
}

func TestAuditNavIsOwnerOnly(t *testing.T) {
	has := func(role staff.Role) bool {
		for _, it := range navItemsForRole(role, "") {
			if it.Key == "audit" {
				return true
			}
		}
		return false
	}
	if !has(staff.RoleOwner) || has(staff.RoleManager) || has(staff.RolePointStaff) {
		t.Error("Журнал must be visible to the owner only")
	}
}

func TestAuditPointAndCategoryEntries(t *testing.T) {
	e := categoryAuditEntry(audit.ActionCategoryDelete, "c1", "Кеды", nil)
	if e.Summary != "Удалена категория «Кеды»" || e.EntityType != audit.EntityCategory || e.Details != nil {
		t.Errorf("category entry = %+v", e)
	}
}
