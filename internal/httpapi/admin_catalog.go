package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/audit"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// stockSetter is what the stock handler depends on (adminStockStore in
// admin_catalog_stock.go), so handler-level tests can inject a fake
// instead of a live database — mirrors the staffGetter/sessionStore
// interfaces in internal/staff. expected, when non-nil, is the quantity
// the client last saw: the write only happens if the stored quantity
// (0 when there is no row) still equals it, otherwise a
// *stockConflictError is returned.
type stockSetter interface {
	Set(ctx context.Context, variantID, pointID string, quantity int, expected *int) (*catalog.StockEntry, error)
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
	journal := audit.New(db)
	stock := &adminStockStore{db: db, audit: journal}

	managerOnly := staffSvc.RequireRole(staff.RoleOwner, staff.RoleManager)

	mux.Handle("POST /admin/api/categories", managerOnly(apperr.Wrap(createCategoryHandler(categories, journal))))
	mux.Handle("PUT /admin/api/categories/{id}", managerOnly(apperr.Wrap(updateCategoryHandler(categories, journal))))
	mux.Handle("DELETE /admin/api/categories/{id}", managerOnly(apperr.Wrap(deleteCategoryHandler(categories, journal))))

	mux.Handle("POST /admin/api/products", managerOnly(apperr.Wrap(createProductHandler(products, journal))))
	mux.Handle("PUT /admin/api/products/{id}", managerOnly(apperr.Wrap(updateProductHandler(products, journal))))
	mux.Handle("DELETE /admin/api/products/{id}", managerOnly(apperr.Wrap(deleteProductHandler(products, journal))))

	mux.Handle("POST /admin/api/products/{id}/variants", managerOnly(apperr.Wrap(createVariantHandler(products, variants, journal))))
	mux.Handle("PUT /admin/api/products/{id}/variants/{variantId}", managerOnly(apperr.Wrap(updateVariantHandler(variants, journal))))
	mux.Handle("DELETE /admin/api/products/{id}/variants/{variantId}", managerOnly(apperr.Wrap(deleteVariantHandler(variants, journal))))

	mux.Handle("PUT /admin/api/products/{id}/images", managerOnly(apperr.Wrap(replaceImagesHandler(products, images, journal))))

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

func createCategoryHandler(repo *catalog.CategoryRepo, journal *audit.Log) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		var req categoryRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		c, err := repo.Create(r.Context(), req.toInput())
		if err != nil {
			return err
		}
		journal.Record(r.Context(), categoryEntry(audit.ActionCategoryCreate, c.ID, "Создана категория «"+req.NameRu+"» (API)", &req))
		return writeJSON(w, http.StatusCreated, c)
	}
}

func updateCategoryHandler(repo *catalog.CategoryRepo, journal *audit.Log) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		var req categoryRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		c, err := repo.Update(r.Context(), r.PathValue("id"), req.toInput())
		if err != nil {
			return err
		}
		journal.Record(r.Context(), categoryEntry(audit.ActionCategoryUpdate, c.ID, "Изменена категория «"+req.NameRu+"» (API)", &req))
		return writeJSON(w, http.StatusOK, c)
	}
}

func deleteCategoryHandler(repo *catalog.CategoryRepo, journal *audit.Log) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		if err := repo.Delete(r.Context(), r.PathValue("id")); err != nil {
			return err
		}
		journal.Record(r.Context(), categoryEntry(audit.ActionCategoryDelete, r.PathValue("id"), "Удалена категория (API)", nil))
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

func createProductHandler(repo *catalog.ProductRepo, journal *audit.Log) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		var req productRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		p, err := repo.Create(r.Context(), req.toInput())
		if err != nil {
			return err
		}
		journal.Record(r.Context(), productEntry(audit.ActionProductCreate, p, "Создан товар «"+p.NameRu+"» (API)"))
		return writeJSON(w, http.StatusCreated, p)
	}
}

func updateProductHandler(repo *catalog.ProductRepo, journal *audit.Log) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		var req productRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		p, err := repo.Update(r.Context(), r.PathValue("id"), req.toInput())
		if err != nil {
			return err
		}
		journal.Record(r.Context(), productEntry(audit.ActionProductUpdate, p, "Изменён товар «"+p.NameRu+"» (API)"))
		return writeJSON(w, http.StatusOK, p)
	}
}

// deleteProductHandler soft-deletes (is_active = false) — see the reasoning
// on catalog.(*ProductRepo).Delete.
func deleteProductHandler(repo *catalog.ProductRepo, journal *audit.Log) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		if err := repo.Delete(r.Context(), r.PathValue("id")); err != nil {
			return err
		}
		journal.Record(r.Context(), audit.Entry{Action: audit.ActionProductDelete, EntityType: audit.EntityProduct,
			EntityID: r.PathValue("id"), Summary: "Товар удалён (скрыт из каталога) (API)"})
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

func createVariantHandler(products *catalog.ProductRepo, variants *catalog.VariantRepo, journal *audit.Log) apperr.HandlerFunc {
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
		journal.Record(r.Context(), variantEntry(audit.ActionVariantCreate, productID, v.ID, "Добавлена вариация "+v.Size+" / "+v.Color+" (API)", &req))
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

func updateVariantHandler(variants *catalog.VariantRepo, journal *audit.Log) apperr.HandlerFunc {
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
		journal.Record(r.Context(), variantEntry(audit.ActionVariantUpdate, productID, v.ID, "Изменена вариация "+v.Size+" / "+v.Color+" (API)", &req))
		return writeJSON(w, http.StatusOK, v)
	}
}

func deleteVariantHandler(variants *catalog.VariantRepo, journal *audit.Log) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		productID := r.PathValue("id")
		variantID := r.PathValue("variantId")
		v, err := variantForProduct(r.Context(), variants, productID, variantID)
		if err != nil {
			return err
		}

		if err := variants.Delete(r.Context(), variantID); err != nil {
			return err
		}
		journal.Record(r.Context(), variantEntry(audit.ActionVariantDelete, productID, variantID, "Удалена вариация "+v.Size+" / "+v.Color+" (API)", nil))
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
}

// --- images ---

type imageRequest struct {
	ObjectKey string  `json:"object_key"`
	SortOrder int     `json:"sort_order"`
	Color     *string `json:"color,omitempty"`
}

// replaceImagesHandler writes {object_key, sort_order} rows into
// product_images — it never talks to MinIO. A separate, independent upload
// flow hands the admin frontend an object_key before it calls this
// endpoint.
func replaceImagesHandler(products *catalog.ProductRepo, images *catalog.ImageRepo, journal *audit.Log) apperr.HandlerFunc {
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
			inputs[i] = catalog.ImageInput{ObjectKey: it.ObjectKey, SortOrder: it.SortOrder, Color: it.Color}
		}

		result, err := images.ReplaceForProduct(r.Context(), productID, inputs)
		if err != nil {
			return err
		}
		journal.Record(r.Context(), audit.Entry{Action: audit.ActionProductImages, EntityType: audit.EntityProduct, EntityID: productID,
			Summary: fmt.Sprintf("Фото товара заменены: %d шт. (API)", len(inputs)), Details: map[string]any{"count": len(inputs)}})
		return writeJSON(w, http.StatusOK, result)
	}
}

// --- stock ---

type stockRequest struct {
	Quantity int `json:"quantity"`
	// ExpectedQuantity (optional) is the quantity the client last saw —
	// the same optimistic check the admin HTML forms do. On mismatch the
	// write is refused with 409 stock_conflict and current_quantity.
	ExpectedQuantity *int `json:"expected_quantity"`
}

// stockConflictResponse is the 409 body: the standard error fields plus
// the quantity currently stored, so a client can refresh and retry.
type stockConflictResponse struct {
	Code            string `json:"code"`
	Message         string `json:"message"`
	CurrentQuantity int    `json:"current_quantity"`
}

// updateStockHandler is the one RBAC nuance in this wave: RequireRole above
// already let owner/manager/point_staff all through, but a point_staff
// member may only set stock at their own point (staff.PointID) — checked
// here against the {pointId} path value.
func updateStockHandler(stock stockSetter) apperr.HandlerFunc {
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

		entry, err := stock.Set(r.Context(), r.PathValue("variantId"), pointID, req.Quantity, req.ExpectedQuantity)
		var conflict *stockConflictError
		if errors.As(err, &conflict) {
			return writeJSON(w, http.StatusConflict, stockConflictResponse{
				Code:            "stock_conflict",
				Message:         fmt.Sprintf("остаток изменился: сейчас %d шт. — обновите данные и повторите", conflict.Current),
				CurrentQuantity: conflict.Current,
			})
		}
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, entry)
	}
}
