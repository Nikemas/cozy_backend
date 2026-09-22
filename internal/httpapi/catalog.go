package httpapi

import (
	"database/sql"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/config"
)

// RegisterCatalogRoutes mounts the public, read-only catalog endpoints
// under /api/v1/*. There is no auth/RBAC here on purpose — write endpoints
// (admin CRUD) are a later wave gated by staff RBAC. cfg is needed only to
// build photo URLs (internal/web's catalog_view.go photoURL and
// internal/admin's products.go photoURL do the same MinIO endpoint+bucket
// construction) — without it the app had no way to show product photos at
// all, since object_key alone isn't a fetchable URL client-side.
func RegisterCatalogRoutes(mux *http.ServeMux, db *sql.DB, cfg *config.Config) {
	categories := catalog.NewCategoryRepo(db)
	products := catalog.NewProductRepo(db)
	variants := catalog.NewVariantRepo(db)
	stock := catalog.NewStockRepo(db)
	images := catalog.NewImageRepo(db)

	mux.Handle("GET /api/v1/categories", apperr.Wrap(func(w http.ResponseWriter, r *http.Request) error {
		tree, err := categories.Tree(r.Context())
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, tree)
	}))

	mux.Handle("GET /api/v1/products", apperr.Wrap(func(w http.ResponseWriter, r *http.Request) error {
		filter, categoryParam, err := parseListFilter(r.URL.Query())
		if err != nil {
			return err
		}

		if categoryParam != "" {
			id, err := categories.ResolveID(r.Context(), categoryParam)
			if err != nil {
				return err
			}
			filter.CategoryID = id
		}

		items, total, err := products.List(r.Context(), filter)
		if err != nil {
			return err
		}

		productIDs := make([]string, len(items))
		for i, p := range items {
			productIDs[i] = p.ID
		}
		primaryImages, err := images.PrimaryForProducts(r.Context(), productIDs)
		if err != nil {
			return err
		}

		out := make([]productOut, len(items))
		for i, p := range items {
			po := productOut{Product: p}
			if img, ok := primaryImages[p.ID]; ok {
				po.PhotoURL = photoURL(cfg, img.ObjectKey)
			}
			out[i] = po
		}

		return writeJSON(w, http.StatusOK, productListResponse{
			Items:    out,
			Page:     filter.Page,
			PageSize: filter.PageSize,
			Total:    total,
		})
	}))

	mux.Handle("GET /api/v1/products/{id}", apperr.Wrap(func(w http.ResponseWriter, r *http.Request) error {
		id := r.PathValue("id")

		product, err := products.GetByID(r.Context(), id)
		if err != nil {
			return err
		}

		productVariants, err := variants.ListByProduct(r.Context(), product.ID)
		if err != nil {
			return err
		}

		variantIDs := make([]string, len(productVariants))
		for i, v := range productVariants {
			variantIDs[i] = v.ID
		}

		stockRows, err := stock.ByVariantIDs(r.Context(), variantIDs)
		if err != nil {
			return err
		}

		stockByVariant := make(map[string][]stockPoint, len(productVariants))
		for _, s := range stockRows {
			stockByVariant[s.VariantID] = append(stockByVariant[s.VariantID], stockPoint{
				PointID:  s.PointID,
				Quantity: s.Quantity,
			})
		}

		productImages, err := images.ListByProduct(r.Context(), product.ID)
		if err != nil {
			return err
		}
		imageOuts := make([]imageOut, len(productImages))
		for i, img := range productImages {
			imageOuts[i] = imageOut{URL: photoURL(cfg, img.ObjectKey), SortOrder: img.SortOrder, Color: img.Color}
		}

		resp := productDetailResponse{Product: *product, Images: imageOuts, Variants: make([]variantDetail, 0, len(productVariants))}
		for _, v := range productVariants {
			stockForVariant := stockByVariant[v.ID]
			if stockForVariant == nil {
				stockForVariant = []stockPoint{}
			}
			resp.Variants = append(resp.Variants, variantDetail{
				Variant: v,
				Stock:   stockForVariant,
			})
		}

		return writeJSON(w, http.StatusOK, resp)
	}))
}

// photoURL builds a direct (non-presigned) URL to objectKey in the
// cozy-media bucket — the same scheme+endpoint+bucket construction
// internal/web's and internal/admin's own photoURL helpers use, so the app
// resolves to the same public-read bucket URL the site and admin panel do.
// Empty objectKey (no photo) returns "".
func photoURL(cfg *config.Config, objectKey string) string {
	return cfg.PublicObjectURL(objectKey)
}

// productOut is catalog.Product plus its primary photo URL (empty string
// if the product has none) — used by the list endpoint, which only ever
// shows one thumbnail per product.
type productOut struct {
	catalog.Product
	PhotoURL string `json:"photo_url"`
}

// imageOut's Color is nil for a general product photo, or the exact
// product_variants.color string the photo is tied to — the client filters
// this list against the shopper's selected variant color (falling back to
// the nil-color photos when that color has none of its own, per
// catalog.ForColor) so picking "black" swaps in the black pair's photos.
type imageOut struct {
	URL       string  `json:"url"`
	SortOrder int     `json:"sort_order"`
	Color     *string `json:"color,omitempty"`
}

type productListResponse struct {
	Items    []productOut `json:"items"`
	Page     int          `json:"page"`
	PageSize int          `json:"page_size"`
	Total    int          `json:"total"`
}

type productDetailResponse struct {
	catalog.Product
	Images   []imageOut      `json:"images"`
	Variants []variantDetail `json:"variants"`
}

type variantDetail struct {
	catalog.Variant
	Stock []stockPoint `json:"stock"`
}

type stockPoint struct {
	PointID  string `json:"point_id"`
	Quantity int    `json:"quantity"`
}

// parseListFilter turns the query params of GET /api/v1/products into a
// catalog.ListFilter. The `category` param is returned separately since
// resolving an id-or-slug into a category_id needs the database — see
// catalog.CategoryRepo.ResolveID — while everything else here is pure and
// unit-testable without one.
func parseListFilter(q url.Values) (filter catalog.ListFilter, categoryParam string, err error) {
	categoryParam = q.Get("category")
	filter.Size = q.Get("size")
	filter.Color = q.Get("color")
	filter.Query = q.Get("q")

	if v := q.Get("price_min"); v != "" {
		min, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return filter, "", apperr.BadRequest("invalid_price_min", "некорректный price_min")
		}
		filter.PriceMin = &min
	}

	if v := q.Get("price_max"); v != "" {
		max, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return filter, "", apperr.BadRequest("invalid_price_max", "некорректный price_max")
		}
		filter.PriceMax = &max
	}

	switch sort := q.Get("sort"); sort {
	case "", catalog.SortNewest, catalog.SortPriceAsc, catalog.SortPriceDesc:
		filter.Sort = sort
	default:
		return filter, "", apperr.BadRequest("invalid_sort", "некорректный sort")
	}

	filter.Page = 1
	if v := q.Get("page"); v != "" {
		page, err := strconv.Atoi(v)
		if err != nil || page < 1 {
			return filter, "", apperr.BadRequest("invalid_page", "некорректный page")
		}
		filter.Page = page
	}

	filter.PageSize = catalog.DefaultPageSize

	return filter, categoryParam, nil
}
