package admin

import (
	"net/http/httptest"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/staff"
)

// TestRenderProductsListExecutes exercises products.gohtml with a
// realistic *ProductsPageData (list with rows, chips, empty state) — the
// generic render_exec_test.go smoke test only renders this screen with a
// nil .Data (guarded by {{with .Data}} in the template), so it alone
// wouldn't catch a panic from a nil field reference inside the real
// content.
func TestRenderProductsListExecutes(t *testing.T) {
	rr := newTestRenderer(t)
	owner := &staff.Staff{ID: "s1", Name: "Айгерим Б.", Role: staff.RoleOwner, IsActive: true}

	cases := []struct {
		name string
		data ProductsPageData
	}{
		{"with rows", ProductsPageData{
			CanEdit:   true,
			CanDelete: true,
			CategoryChips: []ChipVM{
				{Label: "Все", URL: "/admin/products", Active: true},
				{Label: "Мужская обувь", URL: "/admin/products?cat=men", Active: false},
			},
			Products: []ProductRowVM{
				{
					ID: "p1", Name: "Nike Air Max 90", Brand: "Nike", CategoryPath: "Мужская обувь",
					PriceText: "4 500 сом", VariantsLabel: "4 вариации", StockLabel: "12 шт",
					StockFG: "#2E7D32", StockBG: "#E8F5E9", StatusLabel: "Активен",
					HasPhoto: true, PhotoURL: "http://localhost:9000/cozy-media/products/p1.jpg",
					EditURL: "/admin/products/p1", ToggleActiveURL: "/admin/products/p1/toggle-active",
					DeactivateLabel: "Деактивировать", DeleteURL: "/admin/products/p1/delete",
				},
				{
					ID: "p2", Name: "No Photo Shoe", Brand: "", CategoryPath: "Женская обувь",
					PriceText: "0 сом", VariantsLabel: "0 вариаций", StockLabel: "Нет в наличии",
					StockFG: "#C62828", StockBG: "#FFEBEE", StatusLabel: "Неактивен",
					HasPhoto: false, EditURL: "/admin/products/p2", ToggleActiveURL: "/admin/products/p2/toggle-active",
					DeactivateLabel: "Активировать", DeleteURL: "/admin/products/p2/delete",
				},
			},
			Empty:      false,
			CountLabel: "2 товара",
			PageSize:   25,
			NewURL:     "/admin/products/new",
			ImportURL:  "/admin/products/import",
		}},
		{"empty state", ProductsPageData{
			CanEdit: true, Empty: true, CountLabel: "0 товаров",
			CategoryChips: []ChipVM{{Label: "Все", URL: "/admin/products", Active: true}},
			NewURL:        "/admin/products/new", ImportURL: "/admin/products/import",
		}},
		{"with subcategory chips, no delete", ProductsPageData{
			CanEdit: true, CanDelete: false, ShowSubs: true,
			CategoryChips: []ChipVM{{Label: "Все", URL: "/admin/products", Active: false}},
			SubChips:      []ChipVM{{Label: "Все", URL: "/admin/products?cat=men", Active: true}, {Label: "Классика", URL: "/admin/products?cat=men&sub=classic", Active: false}},
			Products: []ProductRowVM{
				{ID: "p1", Name: "Shoe", CategoryPath: "Мужская обувь", PriceText: "1 000 сом", VariantsLabel: "1 вариация", StockLabel: "1 шт", StockFG: "#B85C00", StockBG: "#FFF3E0", StatusLabel: "Активен", EditURL: "/admin/products/p1", ToggleActiveURL: "/admin/products/p1/toggle-active", DeactivateLabel: "Деактивировать", DeleteURL: "/admin/products/p1/delete"},
			},
			CountLabel: "1 товар", NewURL: "/admin/products/new", ImportURL: "/admin/products/import",
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pageData := PageData{
				Screen: "products", PageTitle: "Товары", ShowSidebar: true, ShowSearch: true,
				Staff: owner, Initials: initialsFor(owner.Name), RoleLabel: roleLabel(owner.Role),
				NavItems: navItemsForRole(owner.Role, "products"),
				Data:     c.data,
			}
			w := httptest.NewRecorder()
			if err := rr.Render(w, "products", pageData); err != nil {
				t.Fatalf("Render: %v", err)
			}
			if w.Code != 200 {
				t.Fatalf("status = %d, want 200", w.Code)
			}
		})
	}
}

// TestRenderProductFormExecutes exercises product_form.gohtml for both the
// "new product" (empty) and "edit product" (pre-filled, with variants and
// photos) states.
func TestRenderProductFormExecutes(t *testing.T) {
	rr := newTestRenderer(t)
	manager := &staff.Staff{ID: "s2", Name: "Данияр К.", Role: staff.RoleManager, IsActive: true}

	categories := []CategoryOptionVM{
		{ID: "men", Name: "Мужская обувь", Slug: "men", Children: []CategoryOptionVM{
			{ID: "men-classic", Name: "Классика", Slug: "classic"},
		}},
		{ID: "women", Name: "Женская обувь", Slug: "women"},
	}

	cases := []struct {
		name string
		data ProductFormData
	}{
		{"new", ProductFormData{Categories: categories, CategoriesJSON: categoryOptionsJSON(categories), CanDelete: false}},
		{"edit", ProductFormData{
			IsEdit: true, ProductID: "p1", NameRu: "Nike Air Max 90", NameKy: "Nike Air Max 90",
			CategoryID: "men-classic", TopCategoryID: "men", SubCategoryID: "men-classic",
			Brand: "Nike", BasePrice: "4500", DescriptionRu: "Описание", DescriptionKy: "Баяны",
			Categories: categories, CategoriesJSON: categoryOptionsJSON(categories),
			Images: []ImageRowVM{{ObjectKey: "products/a.jpg", URL: "http://localhost:9000/cozy-media/products/a.jpg"}},
			Variants: []VariantRowVM{
				{ID: "v1", Size: "42", Color: "Белый", Qty: 12, BadgeLbl: "12 шт", BadgeFG: "#2E7D32", BadgeBG: "#E8F5E9"},
				{ID: "v2", Size: "43", Color: "Чёрный", Qty: 0, BadgeLbl: "Нет в наличии", BadgeFG: "#C62828", BadgeBG: "#FFEBEE"},
			},
			CanDelete: true,
		}},
		{"with error", ProductFormData{
			Categories: categories, CategoriesJSON: categoryOptionsJSON(categories),
			Err: "категория не найдена",
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pageData := PageData{
				Screen: "product_form", PageTitle: "Товар", ShowSidebar: true, ShowBack: true,
				Staff: manager, Initials: initialsFor(manager.Name), RoleLabel: roleLabel(manager.Role),
				NavItems: navItemsForRole(manager.Role, "products"),
				Data:     c.data,
			}
			w := httptest.NewRecorder()
			if err := rr.Render(w, "product_form", pageData); err != nil {
				t.Fatalf("Render: %v", err)
			}
			if w.Code != 200 {
				t.Fatalf("status = %d, want 200", w.Code)
			}
		})
	}
}

// TestRenderProductImportExecutes exercises product_import.gohtml.
func TestRenderProductImportExecutes(t *testing.T) {
	rr := newTestRenderer(t)
	owner := &staff.Staff{ID: "s1", Name: "Айгерим Б.", Role: staff.RoleOwner, IsActive: true}

	pageData := PageData{
		Screen: "product_import", PageTitle: "Импорт товаров", ShowSidebar: true, ShowBack: true,
		Staff: owner, Initials: initialsFor(owner.Name), RoleLabel: roleLabel(owner.Role),
		NavItems: navItemsForRole(owner.Role, "products"),
		Data:     ImportPageData{ImportURL: "/admin/products/import"},
	}
	w := httptest.NewRecorder()
	if err := rr.Render(w, "product_import", pageData); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}
