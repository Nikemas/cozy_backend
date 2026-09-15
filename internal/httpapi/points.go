package httpapi

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/storefront"
)

// branchLister is the subset of *storefront.BranchRepo this handler depends
// on, so the handler test can inject a fake instead of a live database —
// mirrors favoriteLister in favorites.go / stockUpserter in
// admin_catalog.go.
type branchLister interface {
	List(ctx context.Context) ([]storefront.Branch, error)
}

// RegisterPublicPointsRoutes mounts the public, read-only points-of-sale
// endpoint under /api/v1/points. There is no auth here on purpose — same as
// RegisterCatalogRoutes's /api/v1/categories and /api/v1/products, this is
// customer-facing catalog/reference data the checkout flow's self-pickup
// option needs before the customer has logged in. Not to be confused with
// the unrelated, owner-only internal/points package (/admin/api/points
// CRUD) — this file lives in package httpapi and only ever reads.
func RegisterPublicPointsRoutes(mux *http.ServeMux, db *sql.DB) {
	branches := storefront.NewBranchRepo(db)

	mux.Handle("GET /api/v1/points", apperr.Wrap(listPointsHandler(branches)))
}

// pointResponse is the JSON shape of one point of sale in API responses.
// storefront.Branch has no json tags of its own — internal/web only ever
// renders it through html/template — so this handler-local DTO carries the
// naming convention the rest of /api/v1/* uses without adding a
// JSON-specific dependency to the shared storefront domain type. Mirrors
// addressResponse in addresses.go.
type pointResponse struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
}

func newPointResponse(b storefront.Branch) pointResponse {
	return pointResponse{
		ID:      b.ID,
		Name:    b.Name,
		Address: b.Address,
	}
}

// pointsResponse wraps the list in an `items` field. This package answers a
// flat, unpaginated list two other ways already (favoritesResponse in
// favorites.go, addressListResponse in addresses.go), both `{items: [...]}`
// — GET /api/v1/categories is the one bare-array response, but it returns a
// nested category tree, not a flat list, so it's not a comparable
// precedent. Matching the flat-list convention here.
type pointsResponse struct {
	Items []pointResponse `json:"items"`
}

func listPointsHandler(branches branchLister) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		list, err := branches.List(r.Context())
		if err != nil {
			return err
		}

		items := make([]pointResponse, len(list))
		for i, b := range list {
			items[i] = newPointResponse(b)
		}
		return writeJSON(w, http.StatusOK, pointsResponse{Items: items})
	}
}
