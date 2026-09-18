// products_view.go holds Task 2's (Товары) view-model types and the pure
// (no I/O) logic that shapes them — split out from products.go so the
// query-building/formatting/pluralization logic can be unit tested without
// a database or an *http.Request, mirroring how internal/catalog keeps its
// own pure helpers (e.g. safeOffset) separate from the DB-calling methods
// around them.
package admin

import (
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/catalog"
)

// --- list screen view models ---

// ChipVM is one category/subcategory filter chip (design canvas's
// cats/subChips arrays).
type ChipVM struct {
	Label  string
	URL    string
	Active bool
}

// ProductRowVM is one row of the products table/cards (design canvas's
// prodRows).
type ProductRowVM struct {
	ID              string
	Name            string
	Brand           string
	CategoryPath    string
	PriceText       string
	VariantsLabel   string
	StockLabel      string
	StockFG         string
	StockBG         string
	StatusLabel     string
	HasPhoto        bool
	PhotoURL        string
	EditURL         string
	ToggleActiveURL string
	DeactivateLabel string
	DeleteURL       string
}

// ProductsPageData backs products.gohtml (screen "products").
type ProductsPageData struct {
	CanEdit   bool
	CanDelete bool

	CategoryChips []ChipVM
	ShowSubs      bool
	SubChips      []ChipVM

	Products   []ProductRowVM
	Empty      bool
	CountLabel string

	Query     string
	PageSize  int
	Page      int
	PageCount int

	NewURL    string
	ImportURL string
}

// --- form screen view models ---

type CategoryOptionVM struct {
	ID       string
	Name     string
	Slug     string
	Children []CategoryOptionVM
}

type VariantRowVM struct {
	ID       string
	Size     string
	Color    string
	Qty      int
	BadgeLbl string
	BadgeFG  string
	BadgeBG  string
}

type ImageRowVM struct {
	ObjectKey string
	URL       string
}

// ProductFormData backs product_form.gohtml (screen "product_form").
type ProductFormData struct {
	IsEdit    bool
	ProductID string

	NameRu string
	NameKy string

	CategoryID    string // resolved leaf category id (top-level or subcategory)
	TopCategoryID string
	SubCategoryID string

	Brand         string
	BasePrice     string
	DescriptionRu string
	DescriptionKy string

	Categories     []CategoryOptionVM
	CategoriesJSON template.JS // Categories, marshaled for the form's category/subcategory-select JS

	Brands []string // datalist options for the brand field — see buildBrandOptions

	Images   []ImageRowVM
	Variants []VariantRowVM

	CanDelete bool
	Err       string
}

// --- import screen view model ---

type ImportRowError struct {
	Row     int
	Message string
}

type ImportPageData struct {
	ImportURL string
}

// --- pure helpers ---

// stockChip mirrors the design canvas's stockMeta(n) (Cozy Admin.dc.html
// ~848-852): the exact thresholds/colors for the stock-level chip shown on
// both the products list and the variant table in the form.
func stockChip(qty int) (label, fg, bg string) {
	switch {
	case qty == 0:
		return "Нет в наличии", "#C62828", "#FFEBEE"
	case qty < 5:
		return fmt.Sprintf("%d шт", qty), "#B85C00", "#FFF3E0"
	default:
		return fmt.Sprintf("%d шт", qty), "#2E7D32", "#E8F5E9"
	}
}

// countLabel renders the products-list footer's "N товаров" count.
func countLabel(total int) string {
	return fmt.Sprintf("%d %s", total, pluralRu(total, "товар", "товара", "товаров"))
}

// variantsLabel renders the table's "Вариаций" cell / card subtitle.
func variantsLabel(n int) string {
	return fmt.Sprintf("%d %s", n, pluralRu(n, "вариация", "вариации", "вариаций"))
}

// formatMoney renders a KGS amount the same way internal/web's
// handlers.formatMoney (catalog_view.go) does — thousands grouped with a
// space, " сом" suffix, no decimals — kept as its own copy rather than an
// import because internal/web doesn't export it and pulling in the whole
// web package here for one formatting helper would be a much bigger
// coupling than duplicating ~15 lines shared by design, not by code.
func formatMoney(v float64) string {
	n := int64(math.Round(v))
	neg := n < 0
	if neg {
		n = -n
	}
	digits := strconv.FormatInt(n, 10)

	var grouped []byte
	for i := 0; i < len(digits); i++ {
		if i > 0 && (len(digits)-i)%3 == 0 {
			grouped = append(grouped, ' ')
		}
		grouped = append(grouped, digits[i])
	}
	out := string(grouped)
	if neg {
		out = "-" + out
	}
	return out + " сом"
}

// statusLabel mirrors the design's {{ p.status }} binding.
func statusLabel(isActive bool) string {
	if isActive {
		return "Активен"
	}
	return "Неактивен"
}

// deactivateLabel mirrors the design's {{ p.deactivateLabel }} toggle
// button text — "Деактивировать" for an active product, "Активировать" to
// bring a previously-deactivated one back.
func deactivateLabel(isActive bool) string {
	if isActive {
		return "Деактивировать"
	}
	return "Активировать"
}

// findCategoryBySlug searches the full tree (including every level of
// nesting) for the category whose slug matches. Returns nil if none match.
func findCategoryBySlug(tree []*catalog.Category, slug string) *catalog.Category {
	for _, c := range tree {
		if c.Slug == slug {
			return c
		}
		if found := findCategoryBySlug(c.Children, slug); found != nil {
			return found
		}
	}
	return nil
}

// collectCategoryIDs returns c's own id plus every descendant's id — used
// to turn a "show this top-level category" chip filter into the full set
// of category_id values products.ListForAdmin should match, since a
// product's category_id may point at any subcategory under the chosen
// top-level category, not the top-level category itself.
func collectCategoryIDs(c *catalog.Category) []string {
	ids := []string{c.ID}
	for _, child := range c.Children {
		ids = append(ids, collectCategoryIDs(child)...)
	}
	return ids
}

// categoryPath renders "Топ / Под" (or just "Топ" with no subcategory) for
// the list table's Категория column, given the product's own category_id
// resolved against the tree.
func categoryPath(tree []*catalog.Category, categoryID string) string {
	for _, top := range tree {
		if top.ID == categoryID {
			return top.NameRu
		}
		for _, sub := range top.Children {
			if sub.ID == categoryID {
				return top.NameRu + " / " + sub.NameRu
			}
		}
	}
	return ""
}

// resolveCategoryFilter turns the list page's ?cat=&sub= slug params into
// the resolved top/sub *catalog.Category (for chip Active state) and the
// category_id set ListForAdmin should filter on. An unknown/empty cat
// param means "no filter" (categoryIDs is nil), matching the design's
// "Все" chip.
func resolveCategoryFilter(tree []*catalog.Category, catSlug, subSlug string) (categoryIDs []string, top, sub *catalog.Category) {
	if catSlug == "" || catSlug == "all" {
		return nil, nil, nil
	}
	top = findCategoryBySlug(tree, catSlug)
	if top == nil {
		return nil, nil, nil
	}
	if subSlug != "" {
		for _, c := range top.Children {
			if c.Slug == subSlug {
				sub = c
				break
			}
		}
	}
	if sub != nil {
		return []string{sub.ID}, top, sub
	}
	return collectCategoryIDs(top), top, nil
}

// buildCategoryChips renders the "Все" + top-level category chip row,
// preserving the current search query across chip clicks.
func buildCategoryChips(tree []*catalog.Category, activeTop *catalog.Category, query string) []ChipVM {
	chips := make([]ChipVM, 0, len(tree)+1)
	chips = append(chips, ChipVM{Label: "Все", URL: productsListURL("", "", query), Active: activeTop == nil})
	for _, c := range tree {
		chips = append(chips, ChipVM{
			Label:  c.NameRu,
			URL:    productsListURL(c.Slug, "", query),
			Active: activeTop != nil && activeTop.ID == c.ID,
		})
	}
	return chips
}

// buildSubChips renders the subcategory chip row for the currently active
// top-level category, including its own "Все" (== activeTop with no
// subcategory) option, mirroring the design's showSubs/subChips.
func buildSubChips(activeTop *catalog.Category, activeSub *catalog.Category, query string) []ChipVM {
	if activeTop == nil || len(activeTop.Children) == 0 {
		return nil
	}
	chips := make([]ChipVM, 0, len(activeTop.Children)+1)
	chips = append(chips, ChipVM{Label: "Все", URL: productsListURL(activeTop.Slug, "", query), Active: activeSub == nil})
	for _, c := range activeTop.Children {
		chips = append(chips, ChipVM{
			Label:  c.NameRu,
			URL:    productsListURL(activeTop.Slug, c.Slug, query),
			Active: activeSub != nil && activeSub.ID == c.ID,
		})
	}
	return chips
}

// productsListURL builds a GET /admin/products?cat=&sub=&q= URL for a chip
// link — a pure function so chip URL construction (and query-param
// preservation) can be tested without spinning up a handler.
func productsListURL(catSlug, subSlug, query string) string {
	v := url.Values{}
	if catSlug != "" {
		v.Set("cat", catSlug)
	}
	if subSlug != "" {
		v.Set("sub", subSlug)
	}
	if query != "" {
		v.Set("q", query)
	}
	if len(v) == 0 {
		return "/admin/products"
	}
	return "/admin/products?" + v.Encode()
}

// buildCategoryOptions converts the CategoryRepo.Tree() result into the
// form's <select> option view models.
func buildCategoryOptions(tree []*catalog.Category) []CategoryOptionVM {
	out := make([]CategoryOptionVM, 0, len(tree))
	for _, c := range tree {
		out = append(out, CategoryOptionVM{ID: c.ID, Name: c.NameRu, Slug: c.Slug, Children: buildCategoryOptions(c.Children)})
	}
	return out
}

// categoryOptionsJSON marshals options for the product form's category/
// subcategory <select> JS (see product_form.gohtml's script block, which
// reads tree[i].ID/.Name/.Slug/.Children — CategoryOptionVM's exported
// field names, unchanged by json.Marshal since it carries no json tags).
// Falls back to an empty array rather than failing the whole page render
// if marshaling somehow errors (it can't, for this struct, but
// json.Marshal's signature always allows for it).
func categoryOptionsJSON(options []CategoryOptionVM) template.JS {
	b, err := json.Marshal(options)
	if err != nil {
		return template.JS("[]")
	}
	return template.JS(b)
}

// defaultBrandOptions seeds the brand field's datalist before any product
// has ever been saved with a brand (a fresh install's ProductRepo.
// DistinctBrands returns nothing yet) — the common shoe brands staff are
// most likely to type first.
var defaultBrandOptions = []string{"Nike", "Adidas", "Puma", "New Balance", "Reebok", "Asics", "Converse", "Vans"}

// buildBrandOptions merges defaultBrandOptions with the brands already used
// by a real product (ProductRepo.DistinctBrands), so the datalist offers
// both the common starting set and whatever staff have actually typed
// before — deduped case-insensitively (keeping whichever spelling was seen
// first) so "Nike" typed on an early product doesn't sit next to a
// redundant default "Nike", and sorted for a stable, scannable dropdown.
func buildBrandOptions(existing []string) []string {
	seen := make(map[string]bool, len(defaultBrandOptions)+len(existing))
	out := make([]string, 0, len(defaultBrandOptions)+len(existing))
	for _, b := range append(append([]string{}, defaultBrandOptions...), existing...) {
		key := strings.ToLower(strings.TrimSpace(b))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}

// resolveTopAndSubIDs finds which top-level category (and, if categoryID
// itself is a subcategory, which subcategory) a product's stored
// category_id belongs to — the edit form needs both to preselect its two
// <select> elements.
func resolveTopAndSubIDs(tree []*catalog.Category, categoryID string) (topID, subID string) {
	for _, top := range tree {
		if top.ID == categoryID {
			return top.ID, ""
		}
		for _, sub := range top.Children {
			if sub.ID == categoryID {
				return top.ID, sub.ID
			}
		}
	}
	return "", ""
}

// nilIfEmpty turns a trimmed-empty form value into nil, matching how
// catalog.ProductInput's DescriptionRu/DescriptionKy/Brand fields (all
// nullable columns) distinguish "not set" from "set to empty string".
func nilIfEmpty(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}

// parsePrice parses the base-price form field, defaulting to 0 on a blank
// or unparsable value rather than erroring the whole save — validate() on
// catalog.ProductInput still rejects a negative price server-side.
func parsePrice(s string) float64 {
	s = strings.TrimSpace(strings.ReplaceAll(s, " ", ""))
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

// parseQty parses one variant row's "Кол-во (склад)" field, clamping a
// blank/unparsable/negative value to 0 rather than erroring the whole
// save.
func parseQty(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// formAt returns values[i] or "" if i is out of range — the parallel
// variant_size[]/variant_color[]/variant_qty[]/variant_id[] form arrays a
// submitted variants table sends aren't guaranteed equal length if a
// client ever sends malformed data, so every read goes through this
// instead of a raw index.
func formAt(values []string, i int) string {
	if i < 0 || i >= len(values) {
		return ""
	}
	return values[i]
}
