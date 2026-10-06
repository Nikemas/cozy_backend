package admin

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// NaN/Inf parse as floats and slip past every comparison; amounts that
// round to 0.00 in NUMERIC(10,2) are zero prices in disguise.
func TestParsePriceRejectsNonFiniteAndRoundsToCents(t *testing.T) {
	for _, in := range []string{"NaN", "nan", "Inf", "+Inf", "-Inf", "infinity", "0.004", "0", "1e400"} {
		if v, err := parsePrice(in); err == nil {
			t.Errorf("parsePrice(%q) = %v, want an error", in, v)
		}
		if v, err := parseVariantPrice(in); err == nil {
			t.Errorf("parseVariantPrice(%q) = %v, want an error", in, *v)
		}
	}
	cases := map[string]float64{"0.005": 0.01, "10.126": 10.13, "4500": 4500, "99999999.99": 99999999.99}
	for in, want := range cases {
		got, err := parsePrice(in)
		if err != nil || got != want {
			t.Errorf("parsePrice(%q) = %v, %v; want %v", in, got, err, want)
		}
		vp, err := parseVariantPrice(in)
		if err != nil || vp == nil || *vp != want {
			t.Errorf("parseVariantPrice(%q) = %v, %v; want %v", in, vp, err, want)
		}
	}
}

// wantBadRequest asserts err is a validation error (400 with code) — not
// merely a failure from the database, which sqlmock without expectations
// would also produce.
func wantBadRequest(t *testing.T, err error, code string) {
	t.Helper()
	var ae *apperr.AppError
	if !errors.As(err, &ae) || ae.Status != http.StatusBadRequest || ae.Code != code {
		t.Errorf("err = %v, want a 400 %s", err, code)
	}
}

func TestProductStoreSaveRejectsNonFinitePrices(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	store := newProductStore(db)

	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		in := validProductInput()
		in.BasePrice = bad
		_, err := store.Save(context.Background(), productSaveInput{Product: in})
		wantBadRequest(t, err, "invalid_base_price")

		_, err = store.Save(context.Background(), productSaveInput{
			Product:  validProductInput(),
			Variants: []variantRowInput{{Key: "n1", Size: "42", Color: "Белый", PriceSet: true, PriceOverride: floatPtr(bad)}},
		})
		wantBadRequest(t, err, "invalid_variant_price")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func priceForm(price, orig string, withOrig bool) map[string][]string {
	f := baseProductForm()
	f["variant_key"] = []string{"v1"}
	f["variant_id"] = []string{"v1"}
	f["variant_size"] = []string{"42"}
	f["variant_color"] = []string{"Белый"}
	f["variant_price"] = []string{price}
	if withOrig {
		f[variantPriceOrigFieldName("v1")] = []string{orig}
	}
	return f
}

// The lost-update guard: an existing row's price_override is only written
// when the submitted value differs from the one the form was rendered with.
func TestParseProductFormWritesVariantPriceOnlyWhenChanged(t *testing.T) {
	cases := []struct {
		name, price, orig string
		withOrig          bool
		wantSet           bool
		wantPrice         *float64
	}{
		{"unchanged", "5200", "5200", true, false, nil},
		{"unchanged, different spelling", "5 200,00", "5200", true, false, nil},
		{"unchanged blank", "", "", true, false, nil},
		{"changed", "5300", "5200", true, true, floatPtr(5300)},
		{"cleared", "", "5200", true, true, nil},
		{"added", "5300", "", true, true, floatPtr(5300)},
		{"stored zero rendered blank is cleared", "", "0", true, true, nil},
		{"no orig field (old form) writes", "5200", "", false, true, floatPtr(5200)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseProductForm(ruTr, priceForm(c.price, c.orig, c.withOrig), "p1", true, parsePoints)
			if len(got.Errs) != 0 {
				t.Fatalf("Errs = %v", got.Errs)
			}
			v := got.Input.Variants[0]
			if v.PriceSet != c.wantSet {
				t.Fatalf("PriceSet = %v, want %v", v.PriceSet, c.wantSet)
			}
			if c.wantSet && !sameMoneyPtr(v.PriceOverride, c.wantPrice) {
				t.Errorf("PriceOverride = %v, want %v", v.PriceOverride, c.wantPrice)
			}
			if c.withOrig && (!got.Rows[0].HasPriceOrig || got.Rows[0].PriceOrig != c.orig) {
				t.Errorf("row orig = %q (has %v), want %q kept for re-render", got.Rows[0].PriceOrig, got.Rows[0].HasPriceOrig, c.orig)
			}
		})
	}
}

// A row added in the browser has no orig: its price is always taken.
func TestParseProductFormNewRowAlwaysCarriesPrice(t *testing.T) {
	f := baseProductForm()
	f["variant_key"] = []string{"n1", "n2"}
	f["variant_id"] = []string{"", ""}
	f["variant_size"] = []string{"42", "43"}
	f["variant_color"] = []string{"Белый", "Белый"}
	f["variant_price"] = []string{"6100", ""}
	got := parseProductForm(ruTr, f, "p1", true, parsePoints)
	if len(got.Errs) != 0 {
		t.Fatalf("Errs = %v", got.Errs)
	}
	v0, v1 := got.Input.Variants[0], got.Input.Variants[1]
	if !v0.PriceSet || v0.PriceOverride == nil || *v0.PriceOverride != 6100 {
		t.Errorf("new row with price = %+v", v0)
	}
	if !v1.PriceSet || v1.PriceOverride != nil {
		t.Errorf("new row without price = %+v", v1)
	}
}

// A stored override of 0 (legacy/import data) renders as blank — "no
// override" — with the raw stored value as orig, so saving the form clears
// it instead of failing validation on every save.
func TestBuildVariantRowsRendersNonPositiveOverrideBlank(t *testing.T) {
	rows := buildVariantRows(ruTr,
		[]catalog.Variant{
			{ID: "v1", Size: "42", Color: "Белый", PriceOverride: floatPtr(0)},
			{ID: "v2", Size: "43", Color: "Белый", PriceOverride: floatPtr(5200.5)},
			{ID: "v3", Size: "44", Color: "Белый"},
		}, nil, parsePoints)
	if rows[0].Price != "" || rows[0].PriceOrig != "0" || !rows[0].HasPriceOrig {
		t.Errorf("zero override row = %+v", rows[0])
	}
	if rows[1].Price != "5200.5" || rows[1].PriceOrig != "5200.5" || !rows[1].HasPriceOrig {
		t.Errorf("override row = %+v", rows[1])
	}
	if rows[2].Price != "" || rows[2].PriceOrig != "" || !rows[2].HasPriceOrig {
		t.Errorf("no-override row = %+v", rows[2])
	}

	// Round trip: re-submitting the form untouched saves fine and clears the 0.
	f := priceForm(rows[0].Price, rows[0].PriceOrig, true)
	got := parseProductForm(ruTr, f, "p1", true, parsePoints)
	if len(got.Errs) != 0 || !got.Input.Variants[0].PriceSet || got.Input.Variants[0].PriceOverride != nil {
		t.Errorf("errs = %v, variant = %+v; want a clean save writing NULL", got.Errs, got.Input.Variants[0])
	}
}

func TestRenderProductFormEmitsVariantPriceOrig(t *testing.T) {
	rr := newTestRenderer(t)
	manager := &staff.Staff{ID: "s2", Name: "Данияр К.", Role: staff.RoleManager, IsActive: true}
	data := ProductFormData{
		IsEdit: true, ProductID: "p1", NameRu: "Nike", NameKy: "Nike", BasePrice: "4500",
		Variants: []VariantRowVM{{Key: "v1", ID: "v1", Size: "42", Color: "Белый", Price: "5200", PriceOrig: "5200", HasPriceOrig: true}},
	}
	pageData := PageData{Screen: "product_form", PageTitle: "Товар", ShowSidebar: true, Staff: manager,
		NavItems: navItemsForRole(manager.Role, "products"), Data: data}
	w := httptest.NewRecorder()
	if err := rr.Render(w, "product_form", pageData); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(w.Body.String(), `name="orig_variant_price_v1" value="5200"`) {
		t.Error("hidden orig_variant_price_<key> input not rendered")
	}
}

// An unchanged price on an existing row must not write price_override.
func TestProductStoreSaveSkipsUnchangedPriceOverride(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE products`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("p1"))
	mock.ExpectQuery(`SELECT id FROM product_variants WHERE product_id = \$1`).WithArgs("p1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("v1"))
	mock.ExpectExec(`UPDATE product_variants SET size = \$2, color = \$3 WHERE id = \$1`).
		WithArgs("v1", "42", "Белый").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM product_images`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	in := parseProductForm(ruTr, priceForm("5200", "5200", true), "p1", true, parsePoints).Input
	if _, err := newProductStore(db).Save(context.Background(), in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
