package admin

import (
	"net/url"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/catalog"
)

var parsePoints = []StockPointVM{{ID: "pA", Name: "Главный склад"}, {ID: "pB", Name: "Дордой"}}

func baseProductForm() url.Values {
	return url.Values{
		"category_id": {"cat1"},
		"name_ru":     {"Nike Air"},
		"name_ky":     {"Nike Air"},
		"base_price":  {"4500"},
	}
}

// The regression this guards: an untouched stock cell must never be
// written. The old save upserted every variant's (cross-point SUM)
// quantity into the first point on every save — overwriting concurrent
// sales and inflating stock with 2+ points.
func TestParseProductFormOnlyChangedCellsBecomeWrites(t *testing.T) {
	f := baseProductForm()
	f["variant_key"] = []string{"v1", "v2"}
	f["variant_id"] = []string{"v1", "v2"}
	f["variant_size"] = []string{"42", "43"}
	f["variant_color"] = []string{"Белый", "Чёрный"}
	// v1: pA unchanged (5 -> 5), pB changed (3 -> 2, e.g. a recount)
	f.Set("qty_v1_pA", "5")
	f.Set("orig_v1_pA", "5")
	f.Set("qty_v1_pB", "2")
	f.Set("orig_v1_pB", "3")
	// v2: pA had no stock row and stays 0; pB had no row and gets 4
	f.Set("qty_v2_pA", "0")
	f.Set("orig_v2_pA", "")
	f.Set("qty_v2_pB", "4")
	f.Set("orig_v2_pB", "")

	got := parseProductForm(ruTr, f, "p1", true, parsePoints)
	if len(got.Errs) != 0 {
		t.Fatalf("Errs = %v", got.Errs)
	}
	if len(got.Input.Stock) != 2 {
		t.Fatalf("Stock changes = %+v, want exactly 2", got.Input.Stock)
	}
	c0, c1 := got.Input.Stock[0], got.Input.Stock[1]
	if c0.RowKey != "v1" || c0.PointID != "pB" || c0.Qty != 2 || c0.Orig == nil || *c0.Orig != 3 {
		t.Errorf("change[0] = %+v (orig %v)", c0, c0.Orig)
	}
	if c1.RowKey != "v2" || c1.PointID != "pB" || c1.Qty != 4 || c1.Orig != nil {
		t.Errorf("change[1] = %+v, want insert (nil orig) of 4", c1)
	}
	if got.Rows[0].Qty != 7 || got.Rows[1].Qty != 4 {
		t.Errorf("row totals = %d, %d; want 7, 4", got.Rows[0].Qty, got.Rows[1].Qty)
	}
	if len(got.Input.Variants) != 2 || got.Input.Variants[0].ID != "v1" {
		t.Errorf("Variants = %+v", got.Input.Variants)
	}
}

func TestParseProductFormInvalidQtyIsFormError(t *testing.T) {
	f := baseProductForm()
	f["variant_key"] = []string{"v1"}
	f["variant_id"] = []string{"v1"}
	f["variant_size"] = []string{"42"}
	f["variant_color"] = []string{"Белый"}
	f.Set("qty_v1_pA", "abc")
	f.Set("orig_v1_pA", "5")
	f.Set("qty_v1_pB", "")
	f.Set("orig_v1_pB", "2")

	got := parseProductForm(ruTr, f, "p1", true, parsePoints)
	if len(got.Errs) == 0 {
		t.Fatal("want a form error for a non-numeric and a cleared quantity")
	}
	if !got.Rows[0].Cells[0].Invalid || !got.Rows[0].Cells[1].Invalid {
		t.Errorf("cells not marked invalid: %+v", got.Rows[0].Cells)
	}
	if got.Rows[0].Cells[0].Value != "abc" {
		t.Errorf("invalid value not kept for re-render: %q", got.Rows[0].Cells[0].Value)
	}
	if len(got.Input.Stock) != 0 {
		t.Errorf("Stock = %+v, want none", got.Input.Stock)
	}
}

func TestParseProductFormPriceAndVariantValidation(t *testing.T) {
	f := baseProductForm()
	f.Set("base_price", "дорого")
	f["variant_key"] = []string{"n1"}
	f["variant_id"] = []string{""}
	f["variant_size"] = []string{"42"}
	f["variant_color"] = []string{""}

	got := parseProductForm(ruTr, f, "", true, parsePoints)
	joined := strings.Join(got.Errs, " | ")
	if !strings.Contains(joined, "Цена") || !strings.Contains(joined, "размер и цвет") {
		t.Errorf("Errs = %v, want price and size/color errors", got.Errs)
	}
}

func TestParseProductFormNewRowAndBlankRowSkipped(t *testing.T) {
	f := baseProductForm()
	f["variant_key"] = []string{"n1", "n2"}
	f["variant_id"] = []string{"", ""}
	f["variant_size"] = []string{"44", ""}
	f["variant_color"] = []string{"Синий", ""}
	f.Set("qty_n1_pA", "3")
	f.Set("qty_n1_pB", "0")
	f.Set("qty_n2_pA", "0")

	got := parseProductForm(ruTr, f, "", true, parsePoints)
	if len(got.Errs) != 0 {
		t.Fatalf("Errs = %v", got.Errs)
	}
	if len(got.Input.Variants) != 1 || got.Input.Variants[0].Key != "n1" {
		t.Fatalf("Variants = %+v, want only n1", got.Input.Variants)
	}
	if len(got.Input.Stock) != 1 || got.Input.Stock[0].Qty != 3 || got.Input.Stock[0].Orig != nil {
		t.Fatalf("Stock = %+v", got.Input.Stock)
	}
}

func TestParseProductFormTamperedOrigIsError(t *testing.T) {
	f := baseProductForm()
	f["variant_key"] = []string{"v1"}
	f["variant_id"] = []string{"v1"}
	f["variant_size"] = []string{"42"}
	f["variant_color"] = []string{"Белый"}
	f.Set("qty_v1_pA", "1")
	f.Set("orig_v1_pA", "x")

	if got := parseProductForm(ruTr, f, "p1", true, parsePoints); len(got.Errs) == 0 {
		t.Fatal("want an error for a non-numeric orig value")
	}
}

func TestMarkStockConflictsRefreshesOnlyConflictingCells(t *testing.T) {
	rows := buildVariantRows(ruTr,
		[]catalog.Variant{{ID: "v1", Size: "42", Color: "Белый"}},
		[]catalog.StockEntry{{VariantID: "v1", PointID: "pA", Quantity: 5}, {VariantID: "v1", PointID: "pB", Quantity: 3}},
		parsePoints,
	)
	rows[0].Cells[0].Value = "9" // staff typed 9 at pA
	rows[0].Cells[1].Value = "7" // and 7 at pB, which was sold down to 1 meanwhile

	markStockConflicts(ruTr, rows, []stockConflict{{RowKey: "v1", PointID: "pB", Current: 1, Exists: true}})

	if rows[0].Cells[0].Value != "9" || rows[0].Cells[0].Conflict {
		t.Errorf("non-conflicting cell changed: %+v", rows[0].Cells[0])
	}
	c := rows[0].Cells[1]
	if !c.Conflict || c.Value != "1" || c.Orig != "1" || !c.HasOrig {
		t.Errorf("conflicting cell = %+v, want value/orig refreshed to 1", c)
	}
	if rows[0].Qty != 10 {
		t.Errorf("row total = %d, want 10", rows[0].Qty)
	}
}

func TestBuildVariantRowsPerPointCells(t *testing.T) {
	rows := buildVariantRows(ruTr,
		[]catalog.Variant{{ID: "v1", Size: "42", Color: "Белый"}},
		[]catalog.StockEntry{{VariantID: "v1", PointID: "pB", Quantity: 4}},
		parsePoints,
	)
	cells := rows[0].Cells
	if len(cells) != 2 {
		t.Fatalf("cells = %d, want 2", len(cells))
	}
	if cells[0].Value != "0" || cells[0].Orig != "" {
		t.Errorf("pA (no stock row) = %+v, want value 0 / empty orig", cells[0])
	}
	if cells[1].Value != "4" || cells[1].Orig != "4" || cells[1].Name != "qty_v1_pB" || cells[1].OrigName != "orig_v1_pB" {
		t.Errorf("pB = %+v", cells[1])
	}
	if rows[0].Qty != 4 {
		t.Errorf("total = %d, want 4 (per-point, not written anywhere)", rows[0].Qty)
	}
}
