package catalog

import (
	"strings"
	"testing"
)

func TestBuildListConditionsSearchMatchesBrand(t *testing.T) {
	conds, args := buildListConditions(ListFilter{Query: "nike"})
	joined := strings.Join(conds, " AND ")
	if !strings.Contains(joined, "brand ILIKE $1") {
		t.Fatalf("search should match brand too: %s", joined)
	}
	if len(args) != 1 || args[0] != "%nike%" {
		t.Fatalf("args = %v, want [%%nike%%]", args)
	}
}

func TestBuildListConditionsPriceUsesVariantOverrides(t *testing.T) {
	lo, hi := 2000.0, 5000.0
	conds, args := buildListConditions(ListFilter{PriceMin: &lo, PriceMax: &hi})
	joined := strings.Join(conds, " AND ")
	if strings.Contains(joined, "base_price >=") || strings.Contains(joined, "base_price <=") {
		t.Fatalf("price filter must not compare bare base_price: %s", joined)
	}
	if !strings.Contains(joined, maxPriceExpr+" >= $1") || !strings.Contains(joined, minPriceExpr+" <= $2") {
		t.Fatalf("price filter should use the effective price range: %s", joined)
	}
	if !strings.Contains(minPriceExpr, "price_override") {
		t.Fatal("minPriceExpr must account for price_override")
	}
	if len(args) != 2 || args[0] != lo || args[1] != hi {
		t.Fatalf("args = %v", args)
	}
}

func TestBuildListConditionsInStockCombinesWithSizeAndColor(t *testing.T) {
	conds, args := buildListConditions(ListFilter{Size: "42", Color: "Черный", InStock: true})
	if len(conds) != 2 {
		t.Fatalf("want is_active + one variant EXISTS, got %d: %v", len(conds), conds)
	}
	v := conds[1]
	for _, want := range []string{"size = $1", "color = $2", "stock.quantity > 0", "points_of_sale.is_active"} {
		if !strings.Contains(v, want) {
			t.Errorf("variant condition missing %q: %s", want, v)
		}
	}
	if len(args) != 2 {
		t.Fatalf("args = %v, want size+color", args)
	}

	conds, _ = buildListConditions(ListFilter{})
	if len(conds) != 1 || conds[0] != "is_active = true" {
		t.Fatalf("empty filter = %v, want only is_active", conds)
	}
}

func TestListOrderBy(t *testing.T) {
	if got := listOrderBy(SortPriceAsc); !strings.HasPrefix(got, minPriceExpr+" ASC") {
		t.Errorf("price_asc = %q", got)
	}
	if got := listOrderBy(SortPriceDesc); !strings.HasPrefix(got, minPriceExpr+" DESC") {
		t.Errorf("price_desc = %q", got)
	}
	if got := listOrderBy("bogus"); !strings.HasPrefix(got, "created_at DESC") {
		t.Errorf("unknown sort = %q, want newest first", got)
	}
}

func TestBuildListConditionsCategoryIDsTakePrecedence(t *testing.T) {
	conds, args := buildListConditions(ListFilter{CategoryID: "a", CategoryIDs: []string{"a", "a-child"}})
	if !strings.Contains(strings.Join(conds, " AND "), "category_id = ANY($1)") {
		t.Fatalf("conds = %v, want category_id = ANY($1)", conds)
	}
	if ids, ok := args[0].([]string); !ok || len(ids) != 2 {
		t.Fatalf("args = %v, want the id list", args)
	}
}
