package admin

import (
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/catalog"
)

func TestStockChipThresholds(t *testing.T) {
	cases := []struct {
		qty       int
		wantLabel string
		wantFG    string
		wantBG    string
	}{
		{0, "Нет в наличии", "#C62828", "#FFEBEE"},
		{1, "1 шт", "#B85C00", "#FFF3E0"},
		{4, "4 шт", "#B85C00", "#FFF3E0"},
		{5, "5 шт", "#2E7D32", "#E8F5E9"},
		{100, "100 шт", "#2E7D32", "#E8F5E9"},
	}
	for _, c := range cases {
		label, fg, bg := stockChip(c.qty)
		if label != c.wantLabel || fg != c.wantFG || bg != c.wantBG {
			t.Errorf("stockChip(%d) = (%q,%q,%q), want (%q,%q,%q)", c.qty, label, fg, bg, c.wantLabel, c.wantFG, c.wantBG)
		}
	}
}

// TestPluralRuProducts exercises the shared pluralRu (also covered by
// orders_test.go's TestPluralRu against different word forms — both kept,
// same function, different edge cases per word set) with product counts.
func TestPluralRuProducts(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{1, "товар"}, {21, "товар"}, {101, "товар"},
		{2, "товара"}, {3, "товара"}, {4, "товара"}, {22, "товара"},
		{0, "товаров"}, {5, "товаров"}, {11, "товаров"}, {12, "товаров"}, {19, "товаров"}, {20, "товаров"}, {25, "товаров"},
	}
	for _, c := range cases {
		if got := pluralRu(c.n, "товар", "товара", "товаров"); got != c.want {
			t.Errorf("pluralRu(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestCountLabel(t *testing.T) {
	if got := countLabel(1); got != "1 товар" {
		t.Errorf("countLabel(1) = %q, want %q", got, "1 товар")
	}
	if got := countLabel(20); got != "20 товаров" {
		t.Errorf("countLabel(20) = %q, want %q", got, "20 товаров")
	}
}

func TestVariantsLabel(t *testing.T) {
	if got := variantsLabel(1); got != "1 вариация" {
		t.Errorf("variantsLabel(1) = %q, want %q", got, "1 вариация")
	}
	if got := variantsLabel(3); got != "3 вариации" {
		t.Errorf("variantsLabel(3) = %q, want %q", got, "3 вариации")
	}
	if got := variantsLabel(0); got != "0 вариаций" {
		t.Errorf("variantsLabel(0) = %q, want %q", got, "0 вариаций")
	}
}

func TestFormatMoney(t *testing.T) {
	cases := []struct {
		v    float64
		want string
	}{
		{4500, "4 500 сом"},
		{0, "0 сом"},
		{1234567, "1 234 567 сом"},
		{-500, "-500 сом"},
	}
	for _, c := range cases {
		if got := formatMoney(c.v); got != c.want {
			t.Errorf("formatMoney(%v) = %q, want %q", c.v, got, c.want)
		}
	}
}

func TestStatusAndDeactivateLabel(t *testing.T) {
	if got := statusLabel(true); got != "Активен" {
		t.Errorf("statusLabel(true) = %q", got)
	}
	if got := statusLabel(false); got != "Неактивен" {
		t.Errorf("statusLabel(false) = %q", got)
	}
	if got := deactivateLabel(true); got != "Деактивировать" {
		t.Errorf("deactivateLabel(true) = %q", got)
	}
	if got := deactivateLabel(false); got != "Активировать" {
		t.Errorf("deactivateLabel(false) = %q", got)
	}
}

// --- category tree helpers ---

func sampleTree() []*catalog.Category {
	men := &catalog.Category{ID: "men", NameRu: "Мужская обувь", Slug: "men"}
	menClassic := &catalog.Category{ID: "men-classic", ParentID: &men.ID, NameRu: "Классика", Slug: "classic"}
	men.Children = []*catalog.Category{menClassic}

	women := &catalog.Category{ID: "women", NameRu: "Женская обувь", Slug: "women"}

	return []*catalog.Category{men, women}
}

func TestFindCategoryBySlug(t *testing.T) {
	tree := sampleTree()

	if c := findCategoryBySlug(tree, "men"); c == nil || c.ID != "men" {
		t.Fatalf("findCategoryBySlug(men) = %v, want men", c)
	}
	if c := findCategoryBySlug(tree, "classic"); c == nil || c.ID != "men-classic" {
		t.Fatalf("findCategoryBySlug(classic) = %v, want men-classic", c)
	}
	if c := findCategoryBySlug(tree, "nope"); c != nil {
		t.Fatalf("findCategoryBySlug(nope) = %v, want nil", c)
	}
}

func TestCollectCategoryIDs(t *testing.T) {
	tree := sampleTree()
	ids := collectCategoryIDs(tree[0]) // men
	want := map[string]bool{"men": true, "men-classic": true}
	if len(ids) != len(want) {
		t.Fatalf("collectCategoryIDs = %v, want 2 ids", ids)
	}
	for _, id := range ids {
		if !want[id] {
			t.Errorf("unexpected id %q", id)
		}
	}
}

func TestResolveCategoryFilterNoFilter(t *testing.T) {
	tree := sampleTree()
	ids, top, sub := resolveCategoryFilter(tree, "", "")
	if ids != nil || top != nil || sub != nil {
		t.Fatalf("resolveCategoryFilter(empty) = (%v,%v,%v), want all nil", ids, top, sub)
	}
	ids, top, sub = resolveCategoryFilter(tree, "all", "")
	if ids != nil || top != nil || sub != nil {
		t.Fatalf("resolveCategoryFilter(all) = (%v,%v,%v), want all nil", ids, top, sub)
	}
}

func TestResolveCategoryFilterTopOnly(t *testing.T) {
	tree := sampleTree()
	ids, top, sub := resolveCategoryFilter(tree, "men", "")
	if top == nil || top.ID != "men" {
		t.Fatalf("top = %v, want men", top)
	}
	if sub != nil {
		t.Fatalf("sub = %v, want nil", sub)
	}
	if len(ids) != 2 {
		t.Fatalf("ids = %v, want men+men-classic", ids)
	}
}

func TestResolveCategoryFilterWithSub(t *testing.T) {
	tree := sampleTree()
	ids, top, sub := resolveCategoryFilter(tree, "men", "classic")
	if top == nil || top.ID != "men" {
		t.Fatalf("top = %v, want men", top)
	}
	if sub == nil || sub.ID != "men-classic" {
		t.Fatalf("sub = %v, want men-classic", sub)
	}
	if len(ids) != 1 || ids[0] != "men-classic" {
		t.Fatalf("ids = %v, want [men-classic]", ids)
	}
}

func TestResolveCategoryFilterUnknownSlug(t *testing.T) {
	tree := sampleTree()
	ids, top, sub := resolveCategoryFilter(tree, "unknown", "")
	if ids != nil || top != nil || sub != nil {
		t.Fatalf("resolveCategoryFilter(unknown) = (%v,%v,%v), want all nil/empty", ids, top, sub)
	}
}

func TestCategoryPath(t *testing.T) {
	tree := sampleTree()
	if got := categoryPath(tree, "men"); got != "Мужская обувь" {
		t.Errorf("categoryPath(men) = %q", got)
	}
	if got := categoryPath(tree, "men-classic"); got != "Мужская обувь / Классика" {
		t.Errorf("categoryPath(men-classic) = %q", got)
	}
	if got := categoryPath(tree, "missing"); got != "" {
		t.Errorf("categoryPath(missing) = %q, want empty", got)
	}
}

func TestResolveTopAndSubIDs(t *testing.T) {
	tree := sampleTree()
	top, sub := resolveTopAndSubIDs(tree, "men-classic")
	if top != "men" || sub != "men-classic" {
		t.Errorf("resolveTopAndSubIDs(men-classic) = (%q,%q), want (men,men-classic)", top, sub)
	}
	top, sub = resolveTopAndSubIDs(tree, "women")
	if top != "women" || sub != "" {
		t.Errorf("resolveTopAndSubIDs(women) = (%q,%q), want (women,\"\")", top, sub)
	}
	top, sub = resolveTopAndSubIDs(tree, "missing")
	if top != "" || sub != "" {
		t.Errorf("resolveTopAndSubIDs(missing) = (%q,%q), want empty", top, sub)
	}
}

func TestProductsListURL(t *testing.T) {
	if got := productsListURL("", "", ""); got != "/admin/products" {
		t.Errorf("productsListURL(empty) = %q", got)
	}
	if got := productsListURL("men", "", ""); got != "/admin/products?cat=men" {
		t.Errorf("productsListURL(men) = %q", got)
	}
	if got := productsListURL("men", "classic", "nike"); got != "/admin/products?cat=men&q=nike&sub=classic" {
		t.Errorf("productsListURL(men,classic,nike) = %q", got)
	}
}

func TestBuildCategoryChipsMarksActive(t *testing.T) {
	tree := sampleTree()
	chips := buildCategoryChips(tree, tree[0], "")
	if len(chips) != 3 { // "Все" + men + women
		t.Fatalf("len(chips) = %d, want 3", len(chips))
	}
	if chips[0].Active {
		t.Errorf("\"Все\" chip should not be active when a category is selected")
	}
	if !chips[1].Active {
		t.Errorf("men chip should be active")
	}
	if chips[2].Active {
		t.Errorf("women chip should not be active")
	}
}

func TestBuildSubChipsNoChildren(t *testing.T) {
	tree := sampleTree()
	if got := buildSubChips(tree[1], nil, ""); got != nil { // women has no children
		t.Errorf("buildSubChips(women) = %v, want nil", got)
	}
	if got := buildSubChips(nil, nil, ""); got != nil {
		t.Errorf("buildSubChips(nil) = %v, want nil", got)
	}
}

func TestBuildSubChipsWithChildren(t *testing.T) {
	tree := sampleTree()
	chips := buildSubChips(tree[0], tree[0].Children[0], "")
	if len(chips) != 2 { // "Все" + classic
		t.Fatalf("len(chips) = %d, want 2", len(chips))
	}
	if chips[0].Active {
		t.Errorf("sub \"Все\" chip should not be active when a subcategory is selected")
	}
	if !chips[1].Active {
		t.Errorf("classic chip should be active")
	}
}

// --- form parsing helpers ---

func TestNilIfEmpty(t *testing.T) {
	if p := nilIfEmpty("  "); p != nil {
		t.Errorf("nilIfEmpty(blank) = %v, want nil", p)
	}
	if p := nilIfEmpty("Nike"); p == nil || *p != "Nike" {
		t.Errorf("nilIfEmpty(Nike) = %v, want Nike", p)
	}
}

func TestParsePrice(t *testing.T) {
	cases := []struct {
		in      string
		want    float64
		wantErr bool
	}{
		{"4500", 4500, false},
		{"4 500", 4500, false},
		{"99.5", 99.5, false},
		{"99,5", 99.5, false},
		{"", 0, true},
		{"not a number", 0, true},
		{"-1", 0, true},
	}
	for _, c := range cases {
		got, err := parsePrice(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parsePrice(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if got != c.want {
			t.Errorf("parsePrice(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// An invalid quantity must be a form error — it used to be clamped to 0
// silently, zeroing the stock of whatever cell had a typo.
func TestParseStockQty(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"12", 12, false},
		{" 0 ", 0, false},
		{"", 0, true},
		{"-5", 0, true},
		{"abc", 0, true},
		{"1.5", 0, true},
		{"100001", 0, true},
	}
	for _, c := range cases {
		got, err := parseStockQty(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parseStockQty(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if got != c.want {
			t.Errorf("parseStockQty(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestFormAt(t *testing.T) {
	values := []string{"a", "b"}
	if got := formAt(values, 0); got != "a" {
		t.Errorf("formAt(0) = %q", got)
	}
	if got := formAt(values, 5); got != "" {
		t.Errorf("formAt(5) = %q, want empty", got)
	}
	if got := formAt(values, -1); got != "" {
		t.Errorf("formAt(-1) = %q, want empty", got)
	}
}

func TestCategoryOptionsJSONRoundTrips(t *testing.T) {
	options := buildCategoryOptions(sampleTree())
	js := categoryOptionsJSON(options)
	if js == "" || js == "[]" {
		t.Fatalf("categoryOptionsJSON = %q, want non-empty JSON", js)
	}
	for _, want := range []string{`"ID":"men"`, `"Slug":"men"`, `"Children"`, `"men-classic"`} {
		if !strings.Contains(string(js), want) {
			t.Errorf("categoryOptionsJSON = %s, missing %q", js, want)
		}
	}
}

func TestBuildBrandOptionsDedupesCaseInsensitively(t *testing.T) {
	got := buildBrandOptions([]string{"Reebok", "nike", "Salomon"})

	want := map[string]bool{"Nike": true, "Reebok": true, "Salomon": true, "Adidas": true}
	if len(got) != len(defaultBrandOptions)+1 { // +1 for "Salomon", the only genuinely new brand
		t.Fatalf("buildBrandOptions returned %d entries, want %d: %v", len(got), len(defaultBrandOptions)+1, got)
	}
	for _, b := range got {
		if !want[b] && b != "Puma" && b != "New Balance" && b != "Asics" && b != "Converse" && b != "Vans" {
			t.Errorf("unexpected brand %q in result: %v", b, got)
		}
	}
	// "nike" (existing, lowercase) must not sit alongside the default "Nike".
	count := 0
	for _, b := range got {
		if strings.EqualFold(b, "nike") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("buildBrandOptions kept %d case-variants of \"nike\", want 1: %v", count, got)
	}
}

func TestBuildBrandOptionsSorted(t *testing.T) {
	got := buildBrandOptions([]string{"Salomon", "  "})
	for i := 1; i < len(got); i++ {
		if strings.ToLower(got[i-1]) > strings.ToLower(got[i]) {
			t.Errorf("buildBrandOptions not sorted: %v", got)
		}
	}
	for _, b := range got {
		if strings.TrimSpace(b) == "" {
			t.Errorf("buildBrandOptions kept a blank entry: %v", got)
		}
	}
}
