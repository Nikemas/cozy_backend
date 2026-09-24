package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/points"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

var testStockPoints = []StockPointVM{{ID: "pt1", Name: "Главный склад"}, {ID: "pt2", Name: "Дордой", Inactive: true}}

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
			Variants: buildVariantRows(
				[]catalog.Variant{{ID: "v1", Size: "42", Color: "Белый"}, {ID: "v2", Size: "43", Color: "Чёрный"}},
				[]catalog.StockEntry{{VariantID: "v1", PointID: "pt1", Quantity: 12}},
				testStockPoints,
			),
			StockPoints:     testStockPoints,
			StockPointsJSON: marshalJS(testStockPoints),
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

// fakeSaver records whether the handler reached the transactional save.
type fakeSaver struct {
	called bool
	in     productSaveInput
	err    error
}

func (f *fakeSaver) Save(_ context.Context, in productSaveInput) (string, error) {
	f.called = true
	f.in = in
	return "p-new", f.err
}

func expectFormLookups(mock sqlmock.Sqlmock) {
	pointsRows := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"id", "name", "address", "is_active", "created_at"}).
			AddRow("pA", "Главный склад", "ул. 1", true, time.Now())
	}
	mock.ExpectQuery(`FROM points_of_sale`).WillReturnRows(pointsRows())
	mock.ExpectQuery(`FROM categories`).WillReturnRows(sqlmock.NewRows([]string{"id", "parent_id", "name_ru", "name_ky", "slug", "sort_order"}).
		AddRow("cat1", nil, "Мужская", "Эркек", "men", 0))
	mock.ExpectQuery(`SELECT DISTINCT brand FROM products`).WillReturnRows(sqlmock.NewRows([]string{"brand"}))
	mock.ExpectQuery(`FROM points_of_sale`).WillReturnRows(pointsRows())
}

func newProductFormHandlers(t *testing.T, saver productSaver) (*handlers, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &handlers{
		render:       newTestRenderer(t),
		categories:   catalog.NewCategoryRepo(db),
		products:     catalog.NewProductRepo(db),
		pointsRepo:   points.NewPointsRepo(db),
		productStore: saver,
		cfg:          &config.Config{},
	}, mock
}

func postProductForm(h *handlers, form url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/admin/products", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	owner := &staff.Staff{ID: "s1", Name: "Айгерим Б.", Role: staff.RoleOwner, IsActive: true}
	r = r.WithContext(staff.NewContextWithStaff(r.Context(), owner))
	w := httptest.NewRecorder()
	h.productCreate(w, r)
	return w
}

// An invalid quantity re-renders the form with an error; nothing is saved.
func TestSaveProductInvalidQtyDoesNotSave(t *testing.T) {
	saver := &fakeSaver{}
	h, mock := newProductFormHandlers(t, saver)
	expectFormLookups(mock)

	form := url.Values{
		"category_id": {"cat1"}, "name_ru": {"Nike"}, "name_ky": {"Nike"}, "base_price": {"4500"},
		"variant_key": {"n1"}, "variant_id": {""}, "variant_size": {"42"}, "variant_color": {"Белый"},
		"qty_n1_pA": {"пять"},
	}
	w := postProductForm(h, form)

	if saver.called {
		t.Fatal("Save called despite an invalid quantity")
	}
	body := w.Body.String()
	if !strings.Contains(body, "admin-form-error") || !strings.Contains(body, "целым числом") {
		t.Errorf("form error not rendered; body excerpt: %.300s", body)
	}
	if !strings.Contains(body, `value="пять"`) || !strings.Contains(body, "is-invalid") {
		t.Error("submitted value / invalid mark not kept on re-render")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A stock conflict re-renders the form with the fresh value in the
// conflicting cell (and as its new orig), so a resubmit can succeed.
func TestSaveProductConflictRerendersWithCurrentValue(t *testing.T) {
	saver := &fakeSaver{err: &stockConflictError{Cells: []stockConflict{{RowKey: "v1", PointID: "pA", Current: 1, Exists: true}}}}
	h, mock := newProductFormHandlers(t, saver)
	expectFormLookups(mock)

	form := url.Values{
		"category_id": {"cat1"}, "name_ru": {"Nike"}, "name_ky": {"Nike"}, "base_price": {"4500"},
		"variant_key": {"v1"}, "variant_id": {"v1"}, "variant_size": {"42"}, "variant_color": {"Белый"},
		"qty_v1_pA": {"9"}, "orig_v1_pA": {"3"},
	}
	w := postProductForm(h, form)

	if !saver.called || len(saver.in.Stock) != 1 || saver.in.Stock[0].Qty != 9 {
		t.Fatalf("saver input = %+v", saver.in)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Остаток изменился") {
		t.Error("conflict message not rendered")
	}
	if !strings.Contains(body, `name="qty_v1_pA" value="1"`) || !strings.Contains(body, `name="orig_v1_pA" value="1"`) {
		t.Error("conflicting cell not refreshed to the current value")
	}
	if !strings.Contains(body, "is-conflict") {
		t.Error("conflicting cell not highlighted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
