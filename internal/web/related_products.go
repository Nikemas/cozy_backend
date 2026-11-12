package web

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Nikemas/cozy_backend/internal/catalog"
)

// relatedProductsLimit is how many cards the product page's "may like"
// block shows — the same 6 as the mobile app (relatedProductsLimit in
// cozy_mobile's catalog_providers.dart).
const relatedProductsLimit = 6

// relatedLister is the subset of the product repository relatedProducts
// needs, so tests can inject a fake.
type relatedLister interface {
	List(ctx context.Context, f catalog.ListFilter) ([]catalog.Product, int, error)
}

// pickRelatedProducts mirrors the mobile app's relatedProductsProvider:
// products of the same category first, then popular ones to fill the
// block, without the product itself or duplicates, at most
// relatedProductsLimit. Popular products are fetched only when the category
// alone can't fill the block.
func pickRelatedProducts(ctx context.Context, repo relatedLister, productID, categoryID string) ([]catalog.Product, error) {
	pageSize := relatedProductsLimit + 1 // +1: the product itself may be among them
	sameCategory, _, err := repo.List(ctx, catalog.ListFilter{CategoryID: categoryID, Page: 1, PageSize: pageSize})
	if err != nil {
		return nil, fmt.Errorf("list same-category products: %w", err)
	}
	candidates := sameCategory
	if len(sameCategory) < pageSize {
		popular, _, err := repo.List(ctx, catalog.ListFilter{Sort: catalog.SortPopular, Page: 1, PageSize: pageSize})
		if err != nil {
			return nil, fmt.Errorf("list popular products: %w", err)
		}
		candidates = append(append(make([]catalog.Product, 0, len(sameCategory)+len(popular)), sameCategory...), popular...)
	}

	seen := map[string]bool{productID: true}
	picked := make([]catalog.Product, 0, relatedProductsLimit)
	for _, p := range candidates {
		if len(picked) == relatedProductsLimit {
			break
		}
		if seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		picked = append(picked, p)
	}
	return picked, nil
}

// relatedCards builds the "may like" cards for a product page. A failure
// only hides the block (logged), never the product page — as in the app.
func (h *handlers) relatedCards(ctx context.Context, productID, categoryID, lang string) []ProductCard {
	products, err := pickRelatedProducts(ctx, h.products, productID, categoryID)
	if err == nil && len(products) > 0 {
		var cards []ProductCard
		if cards, err = h.productCards(ctx, products, lang); err == nil {
			return cards
		}
	}
	if err != nil {
		slog.Warn("web: related products failed, hiding the block", "product_id", productID, "err", err)
	}
	return nil
}
