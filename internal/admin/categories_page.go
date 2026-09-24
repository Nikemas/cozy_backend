// categories_page.go implements the "Категории" screen (GET
// /admin/categories) plus its three mutations — POST /admin/categories to
// create, POST /admin/categories/{id} to edit, POST
// /admin/categories/{id}/delete to remove — the HTML counterpart of the
// JSON admin API internal/httpapi/admin_catalog.go already exposes at
// /admin/api/categories. Owner/manager only, same RBAC as products (see
// routes.go's ownerOrManager). Follows points_page.go's list+modal pattern:
// plain <form method="post">, POST-redirect-GET on success, re-render with
// an error banner on failure.
package admin

import (
	"net/http"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/audit"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// categoryRow is one row of the flattened category tree, indented by Depth
// so categories.gohtml only ranges and prints — no tree-walking in the
// template itself.
type categoryRow struct {
	ID        string
	ParentID  string // "" for a root category
	NameRu    string
	NameKy    string
	Slug      string
	SortOrder int
	Depth     int
	IndentPx  int // Depth*20, precomputed since templates can't do arithmetic
}

// categoryParentOption is one <select name="parent_id"> option — every
// category, indented the same way as categoryRow, so a subcategory can be
// picked as another category's parent.
type categoryParentOption struct {
	ID   string
	Name string
}

// flattenCategoryRows walks tree depth-first into an indented list.
func flattenCategoryRows(tree []*catalog.Category, depth int) []categoryRow {
	rows := make([]categoryRow, 0, len(tree))
	for _, c := range tree {
		parentID := ""
		if c.ParentID != nil {
			parentID = *c.ParentID
		}
		rows = append(rows, categoryRow{
			ID:        c.ID,
			ParentID:  parentID,
			NameRu:    c.NameRu,
			NameKy:    c.NameKy,
			Slug:      c.Slug,
			SortOrder: c.SortOrder,
			Depth:     depth,
			IndentPx:  depth * 20,
		})
		rows = append(rows, flattenCategoryRows(c.Children, depth+1)...)
	}
	return rows
}

// flattenCategoryParentOptions mirrors flattenCategoryRows but produces the
// parent <select>'s options, indenting with "— " per level instead of a
// numeric Depth.
func flattenCategoryParentOptions(tree []*catalog.Category, depth int) []categoryParentOption {
	opts := make([]categoryParentOption, 0, len(tree))
	for _, c := range tree {
		opts = append(opts, categoryParentOption{ID: c.ID, Name: strings.Repeat("— ", depth) + c.NameRu})
		opts = append(opts, flattenCategoryParentOptions(c.Children, depth+1)...)
	}
	return opts
}

// categoriesPageData is categories.gohtml's PageData.Data payload.
type categoriesPageData struct {
	Rows    []categoryRow
	Parents []categoryParentOption
	Error   string
}

// categoriesPage handles GET /admin/categories.
func (h *handlers) categoriesPage(w http.ResponseWriter, r *http.Request) {
	h.renderCategoriesPage(w, r, "")
}

// renderCategoriesPage reloads the category tree and renders the screen,
// optionally with errMsg surfaced as a banner — used by the plain GET and
// by the create/update/delete handlers below when their mutation fails.
func (h *handlers) renderCategoriesPage(w http.ResponseWriter, r *http.Request, errMsg string) {
	st, _ := staff.FromContext(r.Context())

	tree, err := h.categories.Tree(r.Context())
	if err != nil {
		http.Error(w, "не удалось загрузить категории", http.StatusInternalServerError)
		return
	}

	data := h.shellPageData("categories", "Категории", st)
	data.Data = categoriesPageData{
		Rows:    flattenCategoryRows(tree, 0),
		Parents: flattenCategoryParentOptions(tree, 0),
		Error:   errMsg,
	}
	if err := h.render.Render(w, "categories", data); err != nil {
		http.Error(w, "ошибка рендеринга страницы", http.StatusInternalServerError)
	}
}

// categoryInputFromForm reads the fields shared by create and edit out of
// an already-parsed r.Form. An empty parent_id (the "— нет (корневая) —"
// option) becomes a nil ParentID, matching CategoryInput's own root-category
// convention.
func categoryInputFromForm(r *http.Request) catalog.CategoryInput {
	in := catalog.CategoryInput{
		NameRu:    strings.TrimSpace(r.FormValue("name_ru")),
		NameKy:    strings.TrimSpace(r.FormValue("name_ky")),
		Slug:      strings.TrimSpace(r.FormValue("slug")),
		SortOrder: parsePositiveInt(r.FormValue("sort_order"), 0),
	}
	if parentID := strings.TrimSpace(r.FormValue("parent_id")); parentID != "" {
		in.ParentID = &parentID
	}
	return in
}

// categoriesCreate handles POST /admin/categories — the add-modal form.
func (h *handlers) categoriesCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderCategoriesPage(w, r, "не удалось прочитать форму")
		return
	}

	in := categoryInputFromForm(r)
	created, err := h.categories.Create(r.Context(), in)
	if err != nil {
		h.renderCategoriesPage(w, r, appErrMessage(err))
		return
	}
	h.auditCategory(r.Context(), audit.ActionCategoryCreate, created.ID, in.NameRu, &in)

	http.Redirect(w, r, "/admin/categories", http.StatusSeeOther)
}

// categoriesUpdate handles POST /admin/categories/{id} — the edit-modal
// form, prefilled client-side from the row's data-* attributes (see
// categories.gohtml).
func (h *handlers) categoriesUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderCategoriesPage(w, r, "не удалось прочитать форму")
		return
	}

	id := r.PathValue("id")
	in := categoryInputFromForm(r)
	if _, err := h.categories.Update(r.Context(), id, in); err != nil {
		h.renderCategoriesPage(w, r, appErrMessage(err))
		return
	}
	h.auditCategory(r.Context(), audit.ActionCategoryUpdate, id, in.NameRu, &in)

	http.Redirect(w, r, "/admin/categories", http.StatusSeeOther)
}

// categoriesDelete handles POST /admin/categories/{id}/delete — the row's
// delete button (via the shared confirm modal). CategoryRepo.Delete already
// turns "still referenced by a product or subcategory" into
// apperr.Conflict, which appErrMessage surfaces as the banner instead of a
// raw 500.
func (h *handlers) categoriesDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name := h.categoryName(r.Context(), id)
	if err := h.categories.Delete(r.Context(), id); err != nil {
		h.renderCategoriesPage(w, r, appErrMessage(err))
		return
	}
	h.auditCategory(r.Context(), audit.ActionCategoryDelete, id, name, nil)

	http.Redirect(w, r, "/admin/categories", http.StatusSeeOther)
}
