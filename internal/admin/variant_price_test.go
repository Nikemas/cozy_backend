package admin

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

func floatPtr(f float64) *float64 { return &f }

func TestParseVariantPrice(t *testing.T) {
	cases := []struct {
		in      string
		want    *float64
		wantErr bool
	}{
		{"", nil, false},
		{"   ", nil, false},
		{"4500", floatPtr(4500), false},
		{"4 500,50", floatPtr(4500.5), false},
		{"0", nil, true},
		{"-10", nil, true},
		{"abc", nil, true},
		{"100000000", nil, true}, // NUMERIC(10,2) overflow
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := parseVariantPrice(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if c.wantErr {
				return
			}
			if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestParseProductFormVariantPrices(t *testing.T) {
	f := baseProductForm()
	f["variant_key"] = []string{"v1", "n1"}
	f["variant_id"] = []string{"v1", ""}
	f["variant_size"] = []string{"42", "43"}
	f["variant_color"] = []string{"Белый", "Чёрный"}
	f["variant_price"] = []string{"5200", ""}

	got := parseProductForm(ruTr, f, "p1", true, parsePoints)
	if len(got.Errs) != 0 {
		t.Fatalf("Errs = %v", got.Errs)
	}
	v0, v1 := got.Input.Variants[0], got.Input.Variants[1]
	if !v0.PriceSet || v0.PriceOverride == nil || *v0.PriceOverride != 5200 {
		t.Errorf("v0 = %+v, want price override 5200", v0)
	}
	if !v1.PriceSet || v1.PriceOverride != nil {
		t.Errorf("v1 = %+v, want PriceSet with nil override (cleared)", v1)
	}
	if got.Rows[0].Price != "5200" || got.Rows[1].Price != "" {
		t.Errorf("row prices = %q, %q", got.Rows[0].Price, got.Rows[1].Price)
	}
}

func TestParseProductFormInvalidVariantPriceIsFormError(t *testing.T) {
	f := baseProductForm()
	f["variant_key"] = []string{"v1"}
	f["variant_id"] = []string{"v1"}
	f["variant_size"] = []string{"42"}
	f["variant_color"] = []string{"Белый"}
	f["variant_price"] = []string{"-5"}

	got := parseProductForm(ruTr, f, "p1", true, parsePoints)
	if len(got.Errs) == 0 {
		t.Fatal("want a form error for a negative variant price")
	}
	if !strings.Contains(strings.Join(got.Errs, " "), "42") {
		t.Errorf("error should name the row: %v", got.Errs)
	}
	if !got.Rows[0].PriceInvalid || got.Rows[0].Price != "-5" {
		t.Errorf("row = %+v, want invalid mark and raw value kept", got.Rows[0])
	}
}

// A form without variant_price (or with a misaligned array) must not touch
// stored overrides — PriceSet stays false for every row.
func TestParseProductFormMissingVariantPriceLeavesOverrideAlone(t *testing.T) {
	f := baseProductForm()
	f["variant_key"] = []string{"v1", "v2"}
	f["variant_id"] = []string{"v1", "v2"}
	f["variant_size"] = []string{"42", "43"}
	f["variant_color"] = []string{"Белый", "Чёрный"}

	got := parseProductForm(ruTr, f, "p1", true, parsePoints)
	for _, v := range got.Input.Variants {
		if v.PriceSet {
			t.Errorf("variant %s: PriceSet without a variant_price field", v.Key)
		}
	}

	f["variant_price"] = []string{"100"} // one value for two rows
	got = parseProductForm(ruTr, f, "p1", true, parsePoints)
	for _, v := range got.Input.Variants {
		if v.PriceSet {
			t.Errorf("variant %s: PriceSet from a misaligned variant_price array", v.Key)
		}
	}
}

func TestBuildVariantRowsShowsPriceOverride(t *testing.T) {
	rows := buildVariantRows(ruTr,
		[]catalog.Variant{{ID: "v1", Size: "42", Color: "Белый", PriceOverride: floatPtr(5200.5)}, {ID: "v2", Size: "43", Color: "Белый"}},
		nil, parsePoints)
	if rows[0].Price != "5200.5" || rows[1].Price != "" {
		t.Errorf("prices = %q, %q", rows[0].Price, rows[1].Price)
	}
}

func TestRenderProductFormShowsVariantPrice(t *testing.T) {
	rr := newTestRenderer(t)
	manager := &staff.Staff{ID: "s2", Name: "Данияр К.", Role: staff.RoleManager, IsActive: true}
	data := ProductFormData{
		IsEdit: true, ProductID: "p1", NameRu: "Nike", NameKy: "Nike", BasePrice: "4500",
		Variants: []VariantRowVM{{Key: "v1", ID: "v1", Size: "42", Color: "Белый", Price: "5200", PriceInvalid: true}},
	}
	pageData := PageData{Screen: "product_form", PageTitle: "Товар", ShowSidebar: true, Staff: manager,
		NavItems: navItemsForRole(manager.Role, "products"), Data: data}
	w := httptest.NewRecorder()
	if err := rr.Render(w, "product_form", pageData); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, `name="variant_price" value="5200"`) {
		t.Error("variant_price input with stored value not rendered")
	}
	if !strings.Contains(body, ruTr.T("admin.product.variant_price")) {
		t.Error("variant price column header missing")
	}
	if !strings.Contains(body, "variant_price") || !strings.Contains(body, "L.variantPrice") {
		t.Error("JS-added rows must also get a variant_price input (parallel arrays)")
	}
}

func TestProductStoreSavePersistsPriceOverride(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE products`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("p1"))
	mock.ExpectQuery(`SELECT id FROM product_variants WHERE product_id = \$1`).WithArgs("p1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("v1").AddRow("v2"))
	mock.ExpectExec(`UPDATE product_variants SET size = \$2, color = \$3, price_override = \$4 WHERE id = \$1`).
		WithArgs("v1", "42", "Белый", 5200.0).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE product_variants SET size = \$2, color = \$3 WHERE id = \$1`).
		WithArgs("v2", "43", "Белый").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`INSERT INTO product_variants \(product_id, size, color, price_override\)`).
		WithArgs("p1", "44", "Белый", 6100.0).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("vNew"))
	mock.ExpectExec(`DELETE FROM product_images`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	_, err = newProductStore(db).Save(context.Background(), productSaveInput{
		ProductID: "p1",
		Product:   validProductInput(),
		Variants: []variantRowInput{
			{Key: "v1", ID: "v1", Size: "42", Color: "Белый", PriceSet: true, PriceOverride: floatPtr(5200)},
			{Key: "v2", ID: "v2", Size: "43", Color: "Белый"},
			{Key: "n1", Size: "44", Color: "Белый", PriceSet: true, PriceOverride: floatPtr(6100)},
		},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestProductStoreSaveRejectsNonPositivePriceOverride(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	_, err = newProductStore(db).Save(context.Background(), productSaveInput{
		Product:  validProductInput(),
		Variants: []variantRowInput{{Key: "n1", Size: "42", Color: "Белый", PriceSet: true, PriceOverride: floatPtr(0)}},
	})
	if err == nil {
		t.Fatal("want an error for a zero price override")
	}
}

func TestProductSaveEntriesJournalsPriceOverrideChange(t *testing.T) {
	in := productSaveInput{
		ProductID: "p1", Product: validProductInput(),
		Variants: []variantRowInput{{Key: "v1", ID: "v1", Size: "42", Color: "Белый", PriceSet: true, PriceOverride: floatPtr(5200)}},
	}
	old := map[string]variantSnapshot{"v1": {Size: "42", Color: "Белый"}}
	entries := productSaveEntries("p1", in, nil, old, map[string]string{"v1": "v1"})

	var found bool
	for _, e := range entries {
		if _, ok := e.Details["price_override"]; ok {
			found = true
		}
	}
	if !found {
		t.Errorf("no journal entry with a price_override change: %+v", entries)
	}

	// unchanged price: no variant entry at all
	old["v1"] = variantSnapshot{Size: "42", Color: "Белый", PriceOverride: floatPtr(5200)}
	for _, e := range productSaveEntries("p1", in, nil, old, map[string]string{"v1": "v1"}) {
		if _, ok := e.Details["price_override"]; ok {
			t.Errorf("journaled an unchanged price: %+v", e)
		}
	}
}
