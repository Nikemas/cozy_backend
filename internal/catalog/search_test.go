package catalog

import (
	"strings"
	"testing"
)

func TestSearchConditionMatchesNameBrandAndSKU(t *testing.T) {
	cond, args := searchCondition("Nike", nil)
	for _, want := range []string{"name_ru ILIKE $1", "name_ky ILIKE $1", "brand ILIKE $1", "pv.sku ILIKE $1", "product_variants pv"} {
		if !strings.Contains(cond, want) {
			t.Errorf("condition missing %q: %s", want, cond)
		}
	}
	if len(args) != 1 || args[0] != "%Nike%" {
		t.Fatalf("args = %v", args)
	}
}

func TestSearchConditionEmptyQueryAddsNothing(t *testing.T) {
	cond, args := searchCondition("   ", []any{"x"})
	if cond != "" || len(args) != 1 {
		t.Fatalf("cond=%q args=%v", cond, args)
	}
}

func TestSearchConditionEscapesLikeWildcards(t *testing.T) {
	_, args := searchCondition(`50%_\`, nil)
	if args[0] != `%50\%\_\\%` {
		t.Fatalf("args = %v", args)
	}
}

func TestSearchConditionTokensAreAnded(t *testing.T) {
	cond, args := searchCondition("Кроссовки nike", []any{"a"})
	if len(args) != 3 || args[1] != "%Кроссовки%" || args[2] != "%nike%" {
		t.Fatalf("args = %v", args)
	}
	if !strings.Contains(cond, "$2") || !strings.Contains(cond, "$3") || !strings.Contains(cond, ") AND (") {
		t.Fatalf("cond = %s", cond)
	}
}

func TestAdminConditionsSearchBrandAndSKU(t *testing.T) {
	conds, _ := buildAdminListConditions(AdminListFilter{Query: "nike"})
	if len(conds) != 1 || !strings.Contains(conds[0], "brand ILIKE") || !strings.Contains(conds[0], "sku ILIKE") {
		t.Fatalf("conds = %v", conds)
	}
}
