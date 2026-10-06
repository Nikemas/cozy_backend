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

func TestSearchTokensDropsTokensBeyondMax(t *testing.T) {
	got := searchTokens("a b c d e f g")
	want := []string{"a", "b", "c", "d", "e"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tokens = %v, want %v", got, want)
	}
}

func TestSearchTokensCapsQueryRunes(t *testing.T) {
	long := strings.Repeat("ж", maxSearchQueryRunes+50)
	got := searchTokens(long)
	if len(got) != 1 || len([]rune(got[0])) != maxSearchQueryRunes {
		t.Fatalf("got %d tokens, first has %d runes", len(got), len([]rune(got[0])))
	}
}

func TestSearchTokensRuneCapAppliesBeforeSplitting(t *testing.T) {
	// The 101st rune starts a token that must not appear at all.
	q := strings.Repeat("x", maxSearchQueryRunes) + " tail"
	got := searchTokens(q)
	if len(got) != 1 {
		t.Fatalf("tokens = %v", got)
	}
}

func TestSearchConditionBindsAtMostMaxTokens(t *testing.T) {
	cond, args := searchCondition(strings.Repeat("nike ", 50), nil)
	if len(args) != maxSearchTokens {
		t.Fatalf("args = %d, want %d", len(args), maxSearchTokens)
	}
	if strings.Count(cond, "product_variants pv") != maxSearchTokens {
		t.Fatalf("cond has %d token clauses", strings.Count(cond, "product_variants pv"))
	}
}

func TestBuildListConditionsCapsSearchTokens(t *testing.T) {
	_, args := buildListConditions(ListFilter{Query: "a b c d e f g h"})
	if len(args) != maxSearchTokens {
		t.Fatalf("args = %v", args)
	}
}

func TestFacetConditionsCapSearchTokens(t *testing.T) {
	_, args := buildFacetConditions(FacetFilter{Query: "a b c d e f g h"})
	if len(args) != maxSearchTokens {
		t.Fatalf("args = %v", args)
	}
}

func TestAdminConditionsCapSearchTokens(t *testing.T) {
	_, args := buildAdminListConditions(AdminListFilter{Query: "a b c d e f g h"})
	if len(args) != maxSearchTokens {
		t.Fatalf("args = %v", args)
	}
}
