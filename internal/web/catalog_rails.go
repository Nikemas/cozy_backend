// Home page category rails: in browse mode (no category, search, filter or
// sort — see buildShopData) the shop shows one horizontally scrollable row
// per subcategory instead of the flat grid — up to railSize newest
// products, then a "Смотреть все" card linking to /catalog/:slug.
package web

import (
	"context"
	"fmt"

	"github.com/Nikemas/cozy_backend/internal/catalog"
)

// railSize is how many products one home-page rail shows before its
// "see all" card.
const railSize = 5

// CategoryRail is one home-page row: a category heading, its first
// products and the link to the full (category-filtered) catalog.
type CategoryRail struct {
	Name string
	// Parent is the top-level category's name ("Мужское") when Name is a
	// subcategory ("Классика") — shown so equal names stay distinguishable.
	Parent   string
	Href     string
	Products []ProductCard
}

// railSpec is a category a rail is built for: its display names, URL and
// every category ID in its subtree (products of nested children count).
type railSpec struct {
	Name, Parent, Href string
	CategoryIDs        []string
}

// railSpecs lists the rail categories in tree order: every child of a
// top-level category, or the top-level category itself when it has none.
func railSpecs(tree []*catalog.Category, lang string) []railSpec {
	var out []railSpec
	for _, top := range tree {
		topName := pickName(top.NameRu, top.NameKy, lang)
		if len(top.Children) == 0 {
			out = append(out, newRailSpec(top, topName, ""))
			continue
		}
		for _, child := range top.Children {
			out = append(out, newRailSpec(child, pickName(child.NameRu, child.NameKy, lang), topName))
		}
	}
	return out
}

func newRailSpec(c *catalog.Category, name, parent string) railSpec {
	var ids []string
	walkCategories([]*catalog.Category{c}, func(n *catalog.Category) { ids = append(ids, n.ID) })
	return railSpec{Name: name, Parent: parent, Href: "/catalog/" + c.Slug, CategoryIDs: ids}
}

// productLister is catalog.ProductRepo.List — a seam for tests.
type productLister func(ctx context.Context, filter catalog.ListFilter) ([]catalog.Product, int, error)

// railProducts fetches up to railSize newest products per spec. Specs with
// no products are dropped; the result keeps spec order, index-aligned
// with the returned specs.
func railProducts(ctx context.Context, specs []railSpec, list productLister) ([]railSpec, [][]catalog.Product, error) {
	keptSpecs := make([]railSpec, 0, len(specs))
	rows := make([][]catalog.Product, 0, len(specs))
	for _, s := range specs {
		products, _, err := list(ctx, catalog.ListFilter{
			CategoryIDs: s.CategoryIDs,
			Sort:        catalog.SortNewest,
			Page:        1,
			PageSize:    railSize,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("web: listing rail %q: %w", s.Href, err)
		}
		if len(products) == 0 {
			continue
		}
		if len(products) > railSize {
			products = products[:railSize]
		}
		keptSpecs = append(keptSpecs, s)
		rows = append(rows, products)
	}
	return keptSpecs, rows, nil
}

// categoryRails builds the home page rails, shaping every rail's products
// into cards with one batched image/price lookup.
func (h *handlers) categoryRails(ctx context.Context, tree []*catalog.Category, lang string) ([]CategoryRail, error) {
	specs, rows, err := railProducts(ctx, railSpecs(tree, lang), h.products.List)
	if err != nil {
		return nil, err
	}
	var all []catalog.Product
	for _, row := range rows {
		all = append(all, row...)
	}
	cards, err := h.productCards(ctx, all, lang)
	if err != nil {
		return nil, err
	}
	return assembleRails(specs, rows, cards), nil
}

// assembleRails splits the flat cards list back into rails (cards are in
// the same order as the concatenated rows).
func assembleRails(specs []railSpec, rows [][]catalog.Product, cards []ProductCard) []CategoryRail {
	rails := make([]CategoryRail, 0, len(specs))
	offset := 0
	for i, s := range specs {
		n := len(rows[i])
		rails = append(rails, CategoryRail{Name: s.Name, Parent: s.Parent, Href: s.Href, Products: cards[offset : offset+n]})
		offset += n
	}
	return rails
}
