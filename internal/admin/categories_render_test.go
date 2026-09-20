package admin

import (
	"net/http/httptest"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/staff"
)

// TestRenderCategoriesExecutesWithRows exercises categories.gohtml with a
// nested (parent + child) tree plus an error banner — the create/edit
// modals' onclick handlers interpolate every row field into a JS object
// literal, which is exactly the kind of contextual-autoescaping edge case
// TestRenderShellScreensExecute's nil-.Data case doesn't cover.
func TestRenderCategoriesExecutesWithRows(t *testing.T) {
	rr := newTestRenderer(t)
	owner := &staff.Staff{ID: "s1", Name: "Айгерим Б.", Role: staff.RoleOwner, IsActive: true}

	data := PageData{
		Screen:      "categories",
		PageTitle:   "Категории",
		ShowSidebar: true,
		Staff:       owner,
		Initials:    initialsFor(owner.Name),
		RoleLabel:   roleLabel(owner.Role),
		NavItems:    navItemsForRole(owner.Role, "categories"),
		Data: categoriesPageData{
			Error: "категория с таким slug уже существует",
			Rows: []categoryRow{
				{ID: "c1", NameRu: "Кроссовки", NameKy: "Кроссовкалар", Slug: "krossovki", SortOrder: 1, Depth: 0, IndentPx: 0},
				{ID: "c2", ParentID: "c1", NameRu: "Мужские", NameKy: "Эркектер", Slug: "krossovki-muzh", SortOrder: 1, Depth: 1, IndentPx: 20},
			},
			Parents: []categoryParentOption{
				{ID: "c1", Name: "Кроссовки"},
				{ID: "c2", Name: "— Мужские"},
			},
		},
	}

	w := httptest.NewRecorder()
	if err := rr.Render(w, "categories", data); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}
