package httpapi

import (
	"database/sql"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/httpmw"
	"github.com/Nikemas/cozy_backend/internal/media"
)

// Client cache lifetimes for the public read endpoints (see
// httpmw.PublicCache). Kept short: there is no cache invalidation, so this
// is the worst-case staleness a shopper can see after an admin edit.
const (
	publicCategoryMaxAge = 60 * time.Second
	publicProductMaxAge  = 30 * time.Second
	publicPointsMaxAge   = 60 * time.Second
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

	// These responses are identical for every caller (no auth, nothing
	// per-customer), so clients may reuse them briefly. Products carry
	// stock numbers, hence the shorter window; the category tree only
	// changes when staff edit it.
	detailSources := catalogDetailSources{variants: variants, stock: stock, images: images}

	categoryCache := httpmw.PublicCache(publicCategoryMaxAge)
	productCache := httpmw.PublicCache(publicProductMaxAge)

	mux.Handle("GET /api/v1/categories", categoryCache(apperr.Wrap(func(w http.ResponseWriter, r *http.Request) error {
		tree, err := categories.Tree(r.Context())
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, tree)
	})))

	mux.Handle("GET /api/v1/products", productCache(apperr.Wrap(func(w http.ResponseWriter, r *http.Request) error {
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
				po.ThumbURL = thumbURL(cfg, img.ObjectKey)
			}
			out[i] = po
		}

		return writeJSON(w, http.StatusOK, productListResponse{
			Items:    out,
			Page:     filter.Page,
			PageSize: filter.PageSize,
			Total:    total,
		})
	})))

	mux.Handle("GET /api/v1/products/{id}", productCache(apperr.Wrap(func(w http.ResponseWriter, r *http.Request) error {
		id := r.PathValue("id")

		product, err := products.GetByID(r.Context(), id)
		if err != nil {
			return err
		}

		details, err := buildProductDetails(r.Context(), detailSources, cfg, []catalog.Product{*product})
		if err != nil {
			return err
		}
		resp := details[0]

		return writeJSON(w, http.StatusOK, resp)
	})))
}

// photoURL builds a direct (non-presigned) URL to objectKey in the
// cozy-media bucket — the same scheme+endpoint+bucket construction
// internal/web's and internal/admin's own photoURL helpers use, so the app
// resolves to the same public-read bucket URL the site and admin panel do.
// Empty objectKey (no photo) returns "".
func photoURL(cfg *config.Config, objectKey string) string {
	return cfg.PublicObjectURL(objectKey)
}

// thumbURL is photoURL for the 400px thumb variant (media.ThumbKey) —
// what grids/lists should load. For a legacy key with no variants it is
// the same URL as photoURL, so clients can always prefer thumb_url.
func thumbURL(cfg *config.Config, objectKey string) string {
	return cfg.PublicObjectURL(media.ThumbKey(objectKey))
}

// productOut is catalog.Product plus its primary photo URLs (empty
// strings if the product has none) — used by the list endpoint, which
// only ever shows one thumbnail per product. ThumbURL was added alongside
// PhotoURL (additive — older clients keep reading photo_url).
type productOut struct {
	catalog.Product
	PhotoURL string `json:"photo_url"`
	ThumbURL string `json:"thumb_url"`
}

// imageOut's Color is nil for a general product photo, or the exact
// product_variants.color string the photo is tied to — the client filters
// this list against the shopper's selected variant color (falling back to
// the nil-color photos when that color has none of its own, per
// catalog.ForColor) so picking "black" swaps in the black pair's photos.
type imageOut struct {
	URL       string  `json:"url"`
	ThumbURL  string  `json:"thumb_url"`
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
