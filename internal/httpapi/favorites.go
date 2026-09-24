package httpapi

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/auth"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/storefront"
)

// favoriteLister/favoriteWriter are the subsets of *storefront.FavoriteRepo
// these handlers depend on, so handler-level tests can inject a fake
// instead of a live database — mirrors stockUpserter in admin_catalog.go.
type favoriteLister interface {
	ListProductIDs(ctx context.Context, customerID string) ([]string, error)
}

type favoriteWriter interface {
	Add(ctx context.Context, customerID, productID string) error
	Remove(ctx context.Context, customerID, productID string) error
}

// favoriteProductGetter is the subset of *catalog.ProductRepo used to
// resolve favorited product IDs into full product rows in one query.
type favoriteProductGetter interface {
	GetActiveByIDs(ctx context.Context, ids []string) (map[string]catalog.Product, error)
}

// registerFavoritesRoutes mounts the customer favorites endpoints under
// /api/v1/favorites, all gated by authSvc.RequireCustomer — there is no
// anonymous access to a customer's favorites. Called from
// RegisterCustomerRoutes (customer.go), the one exported entry point for
// this whole file group.
func registerFavoritesRoutes(mux *http.ServeMux, db *sql.DB, authSvc *auth.Service, cfg *config.Config) {
	favorites := storefront.NewFavoriteRepo(db)
	products := catalog.NewProductRepo(db)
	details := catalogDetailSources{
		variants: catalog.NewVariantRepo(db),
		stock:    catalog.NewStockRepo(db),
		images:   catalog.NewImageRepo(db),
	}

	mux.Handle("GET /api/v1/favorites", authSvc.RequireCustomer(apperr.Wrap(listFavoritesHandler(favorites, products, details, cfg))))
	mux.Handle("POST /api/v1/favorites/{productId}", authSvc.RequireCustomer(apperr.Wrap(addFavoriteHandler(favorites))))
	mux.Handle("DELETE /api/v1/favorites/{productId}", authSvc.RequireCustomer(apperr.Wrap(removeFavoriteHandler(favorites))))
}

// listFavoritesHandler returns the customer's favorited products as full
// product objects — the exact GET /api/v1/products/{id} shape (images,
// variants with per-point stock) — newest favorite first, so the app can
// render the list and open a product without another request per item.
//
// Query count is constant, whatever the list length: favorite ids,
// products (ProductRepo.GetActiveByIDs), then variants, stock and images
// in one batch query each (buildProductDetails). A product that has since
// gone inactive/deleted is skipped rather than failing the whole request —
// same behavior as internal/web's loadFavoriteCards.
func listFavoritesHandler(favorites favoriteLister, products favoriteProductGetter, details productDetailSources, cfg *config.Config) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, ok := auth.CustomerIDFromContext(r.Context())
		if !ok {
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}

		ids, err := favorites.ListProductIDs(r.Context(), customerID)
		if err != nil {
			return err
		}

		byID, err := products.GetActiveByIDs(r.Context(), ids)
		if err != nil {
			return err
		}
		active := make([]catalog.Product, 0, len(ids))
		for _, id := range ids {
			if p, ok := byID[id]; ok {
				active = append(active, p)
			}
		}

		items, err := buildProductDetails(r.Context(), details, cfg, active)
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, favoritesResponse{Items: items})
	}
}

type favoritesResponse struct {
	Items []productDetailResponse `json:"items"`
}

// addFavoriteHandler adds a product to the authenticated customer's
// favorites. Idempotent, matching FavoriteRepo.Add's semantics — favoriting
// an already-favorited product succeeds again rather than erroring.
func addFavoriteHandler(favorites favoriteWriter) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, ok := auth.CustomerIDFromContext(r.Context())
		if !ok {
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}

		if err := favorites.Add(r.Context(), customerID, r.PathValue("productId")); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
}

// removeFavoriteHandler removes a product from the authenticated
// customer's favorites. Idempotent, matching FavoriteRepo.Remove's
// semantics — removing a product that was never favorited succeeds too.
func removeFavoriteHandler(favorites favoriteWriter) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, ok := auth.CustomerIDFromContext(r.Context())
		if !ok {
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}

		if err := favorites.Remove(r.Context(), customerID, r.PathValue("productId")); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
}
