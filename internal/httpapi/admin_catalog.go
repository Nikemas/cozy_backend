package httpapi

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// stockUpserter is the subset of *catalog.StockRepo the stock handler
// depends on, so handler-level tests can inject a fake instead of a live
// database — mirrors the staffGetter/sessionStore interfaces in
// internal/staff.
type stockUpserter interface {
	Upsert(ctx context.Context, variantID, pointID string, quantity int) (*catalog.StockEntry, error)
}

// RegisterAdminCatalogRoutes mounts the admin (write) catalog endpoints
// under /admin/api/*, per Task D / §5 and §8 of the ТЗ: categories,
// products, variants and images are owner/manager only, while stock is
// additionally open to point_staff — but only at their own point
// (staff.PointID), enforced inside updateStockHandler after RequireRole
// lets the role itself through.
func RegisterAdminCatalogRoutes(mux *http.ServeMux, db *sql.DB, staffSvc *staff.Service) {
	categories := catalog.NewCategoryRepo(db)
	products := catalog.NewProductRepo(db)
	variants := catalog.NewVariantRepo(db)
	images := catalog.NewImageRepo(db)
	stock := catalog.NewStockRepo(db)

	managerOnly := staffSvc.RequireRole(staff.RoleOwner, staff.RoleManager)

	mux.Handle("POST /admin/api/categories", managerOnly(apperr.Wrap(createCategoryHandler(categories))))
	mux.Handle("PUT /admin/api/categories/{id}", managerOnly(apperr.Wrap(updateCategoryHandler(categories))))
	mux.Handle("DELETE /admin/api/categories/{id}", managerOnly(apperr.Wrap(deleteCategoryHandler(categories))))

	mux.Handle("POST /admin/api/products", managerOnly(apperr.Wrap(createProductHandler(products))))
	mux.Handle("PUT /admin/api/products/{id}", managerOnly(apperr.Wrap(updateProductHandler(products))))
	mux.Handle("DELETE /admin/api/products/{id}", managerOnly(apperr.Wrap(deleteProductHandler(products))))

	mux.Handle("POST /admin/api/products/{id}/variants", managerOnly(apperr.Wrap(createVariantHandler(products, variants))))
	mux.Handle("PUT /admin/api/products/{id}/variants/{variantId}", managerOnly(apperr.Wrap(updateVariantHandler(variants))))
	mux.Handle("DELETE /admin/api/products/{id}/variants/{variantId}", managerOnly(apperr.Wrap(deleteVariantHandler(variants))))

	mux.Handle("PUT /admin/api/products/{id}/images", managerOnly(apperr.Wrap(replaceImagesHandler(products, images))))

	mux.Handle("PUT /admin/api/stock/{variantId}/{pointId}",
		staffSvc.RequireRole(staff.RoleOwner, staff.RoleManager, staff.RolePointStaff)(
			apperr.Wrap(updateStockHandler(stock)),
		),
	)
}

// --- categories ---

type categoryRequest struct {
	ParentID  *string `json:"parent_id"`
	NameRu    string  `json:"name_ru"`
	NameKy    string  `json:"name_ky"`
	Slug      string  `json:"slug"`
	SortOrder int     `json:"sort_order"`
}

func (req categoryRequest) toInput() catalog.CategoryInput {
	return catalog.CategoryInput{
		ParentID:  req.ParentID,
		NameRu:    req.NameRu,
		NameKy:    req.NameKy,
		Slug:      req.Slug,
		SortOrder: req.SortOrder,
	}
}

func createCategoryHandler(repo *catalog.CategoryRepo) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		var req categoryRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		c, err := repo.Create(r.Context(), req.toInput())
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusCreated, c)
	}
}

func updateCategoryHandler(repo *catalog.CategoryRepo) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		var req categoryRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		c, err := repo.Update(r.Context(), r.PathValue("id"), req.toInput())
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, c)
	}
}

func deleteCategoryHandler(repo *catalog.CategoryRepo) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		if err := repo.Delete(r.Context(), r.PathValue("id")); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
}

// --- products ---

type productRequest struct {
	CategoryID    string  `json:"category_id"`
	NameRu        string  `json:"name_ru"`
	NameKy        string  `json:"name_ky"`
	DescriptionRu *string `json:"description_ru"`
	DescriptionKy *string `json:"description_ky"`
	Brand         *string `json:"brand"`
	BasePrice     float64 `json:"base_price"`
	// IsActive is a pointer so a request that omits it defaults to true
	// (a newly created/edited product is active unless explicitly hidden)
	// rather than silently defaulting to Go's bool zero value (false).
	IsActive *bool `json:"is_active"`
}

func (req productRequest) toInput() catalog.ProductInput {
	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}
	return catalog.ProductInput{
		CategoryID:    req.CategoryID,
		NameRu:        req.NameRu,
		NameKy:        req.NameKy,
		DescriptionRu: req.DescriptionRu,
		DescriptionKy: req.DescriptionKy,
		Brand:         req.Brand,
		BasePrice:     req.BasePrice,
		IsActive:      isActive,
	}
}

func createProductHandler(repo *catalog.ProductRepo) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		var req productRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		p, err := repo.Create(r.Context(), req.toInput())
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusCreated, p)
	}
}

func updateProductHandler(repo *catalog.ProductRepo) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		var req productRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		p, err := repo.Update(r.Context(), r.PathValue("id"), req.toInput())
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, p)
	}
}

// deleteProductHandler soft-deletes (is_active = false) — see the reasoning
// on catalog.(*ProductRepo).Delete.
func deleteProductHandler(repo *catalog.ProductRepo) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		if err := repo.Delete(r.Context(), r.PathValue("id")); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
}

// --- variants ---

type variantRequest struct {
	Size          string   `json:"size"`
	Color         string   `json:"color"`
	SKU           *string  `json:"sku"`
	PriceOverride *float64 `json:"price_override"`
}

func (req variantRequest) toInput() catalog.VariantInput {
	return catalog.VariantInput{
		Size:          req.Size,
		Color:         req.Color,
		SKU:           req.SKU,
		PriceOverride: req.PriceOverride,
	}
}

func createVariantHandler(products *catalog.ProductRepo, variants *catalog.VariantRepo) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		productID := r.PathValue("id")
		// Resolved (and its NotFound surfaced) before insert so a bad
		// product id in the URL reads as 404, not a generic 400 from a
		// foreign key violation.
		if _, err := products.GetByIDAny(r.Context(), productID); err != nil {
			return err
		}

		var req variantRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		v, err := variants.Create(r.Context(), productID, req.toInput())
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusCreated, v)
	}
}

// variantForProduct fetches variantID and confirms it belongs to
// productID, returning apperr.NotFound otherwise — so one product's admin
// route can't be used to edit or delete a variant that actually belongs to
// a different product just by guessing its id.
func variantForProduct(ctx context.Context, variants *catalog.VariantRepo, productID, variantID string) (*catalog.Variant, error) {
	v, err := variants.GetByID(ctx, variantID)
	if err != nil {
		return nil, err
	}
	if v.ProductID != productID {
		return nil, apperr.NotFound("variant_not_found", "вариация не найдена")
	}
	return v, nil
}

func updateVariantHandler(variants *catalog.VariantRepo) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		productID := r.PathValue("id")
		variantID := r.PathValue("variantId")
		if _, err := variantForProduct(r.Context(), variants, productID, variantID); err != nil {
			return err
		}

		var req variantRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		v, err := variants.Update(r.Context(), variantID, req.toInput())
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, v)
	}
}

func deleteVariantHandler(variants *catalog.VariantRepo) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		productID := r.PathValue("id")
		variantID := r.PathValue("variantId")
		if _, err := variantForProduct(r.Context(), variants, productID, variantID); err != nil {
			return err
		}

		if err := variants.Delete(r.Context(), variantID); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
}

// --- images ---

type imageRequest struct {
	ObjectKey string `json:"object_key"`
	SortOrder int    `json:"sort_order"`
}

// replaceImagesHandler writes {object_key, sort_order} rows into
// product_images — it never talks to MinIO. A separate, independent upload
// flow hands the admin frontend an object_key before it calls this
// endpoint.
func replaceImagesHandler(products *catalog.ProductRepo, images *catalog.ImageRepo) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		productID := r.PathValue("id")
		if _, err := products.GetByIDAny(r.Context(), productID); err != nil {
			return err
		}

		var req []imageRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		inputs := make([]catalog.ImageInput, len(req))
		for i, it := range req {
			inputs[i] = catalog.ImageInput{ObjectKey: it.ObjectKey, SortOrder: it.SortOrder}
		}

		result, err := images.ReplaceForProduct(r.Context(), productID, inputs)
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, result)
	}
}

// --- stock ---

type stockRequest struct {
	Quantity int `json:"quantity"`
}

// updateStockHandler is the one RBAC nuance in this wave: RequireRole above
// already let owner/manager/point_staff all through, but a point_staff
// member may only set stock at their own point (staff.PointID) — checked
// here against the {pointId} path value.
func updateStockHandler(stock stockUpserter) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		st, ok := staff.FromContext(r.Context())
		if !ok {
			// Defensive: RequireRole should always have populated this.
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}

		pointID := r.PathValue("pointId")
		if st.Role == staff.RolePointStaff && (st.PointID == nil || *st.PointID != pointID) {
			return apperr.Forbidden("forbidden", "сотрудник точки может изменять остатки только своей точки")
		}

		var req stockRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		entry, err := stock.Upsert(r.Context(), r.PathValue("variantId"), pointID, req.Quantity)
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, entry)
	}
}
