package httpapi

import (
	"context"

	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/config"
)

// productDetailSources is what building a productDetailResponse reads —
// the batch methods of catalog's VariantRepo/StockRepo/ImageRepo, so tests
// can fake them.
type productDetailSources interface {
	ListByProductIDs(ctx context.Context, productIDs []string) (map[string][]catalog.Variant, error)
	ByVariantIDs(ctx context.Context, variantIDs []string) ([]catalog.StockEntry, error)
	ImagesByProductIDs(ctx context.Context, productIDs []string) (map[string][]catalog.ProductImage, error)
}

// catalogDetailSources adapts the three catalog repos to
// productDetailSources.
type catalogDetailSources struct {
	variants *catalog.VariantRepo
	stock    *catalog.StockRepo
	images   *catalog.ImageRepo
}

func (s catalogDetailSources) ListByProductIDs(ctx context.Context, ids []string) (map[string][]catalog.Variant, error) {
	return s.variants.ListByProductIDs(ctx, ids)
}

func (s catalogDetailSources) ByVariantIDs(ctx context.Context, ids []string) ([]catalog.StockEntry, error) {
	return s.stock.ByVariantIDs(ctx, ids)
}

func (s catalogDetailSources) ImagesByProductIDs(ctx context.Context, ids []string) (map[string][]catalog.ProductImage, error) {
	return s.images.ListByProductIDs(ctx, ids)
}

// buildProductDetails turns products into the GET /api/v1/products/{id}
// shape (images + variants with per-point stock) with a constant number
// of queries — three, however many products — so the favorites list
// returns exactly the objects the product screen does without N+1.
// Output order follows products.
func buildProductDetails(ctx context.Context, src productDetailSources, cfg *config.Config, products []catalog.Product) ([]productDetailResponse, error) {
	out := make([]productDetailResponse, 0, len(products))
	if len(products) == 0 {
		return out, nil
	}
	productIDs := make([]string, len(products))
	for i, p := range products {
		productIDs[i] = p.ID
	}

	variantsByProduct, err := src.ListByProductIDs(ctx, productIDs)
	if err != nil {
		return nil, err
	}
	var variantIDs []string
	for _, id := range productIDs {
		for _, v := range variantsByProduct[id] {
			variantIDs = append(variantIDs, v.ID)
		}
	}
	stockRows, err := src.ByVariantIDs(ctx, variantIDs)
	if err != nil {
		return nil, err
	}
	stockByVariant := make(map[string][]stockPoint, len(variantIDs))
	for _, s := range stockRows {
		stockByVariant[s.VariantID] = append(stockByVariant[s.VariantID], stockPoint{PointID: s.PointID, Quantity: s.Quantity})
	}
	imagesByProduct, err := src.ImagesByProductIDs(ctx, productIDs)
	if err != nil {
		return nil, err
	}

	for _, p := range products {
		imgs := imagesByProduct[p.ID]
		imageOuts := make([]imageOut, len(imgs))
		for i, img := range imgs {
			imageOuts[i] = imageOut{URL: photoURL(cfg, img.ObjectKey), ThumbURL: thumbURL(cfg, img.ObjectKey), SortOrder: img.SortOrder, Color: img.Color}
		}
		vs := variantsByProduct[p.ID]
		resp := productDetailResponse{Product: p, Images: imageOuts, Variants: make([]variantDetail, 0, len(vs))}
		for _, v := range vs {
			st := stockByVariant[v.ID]
			if st == nil {
				st = []stockPoint{}
			}
			resp.Variants = append(resp.Variants, variantDetail{Variant: v, Stock: st})
		}
		out = append(out, resp)
	}
	return out, nil
}
