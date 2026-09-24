package httpapi

import (
	"context"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/orders"
)

// deliveryZoneLister is the subset of *orders.DeliveryZoneRepo
// listDeliveryZonesHandler needs.
type deliveryZoneLister interface {
	ListActive(ctx context.Context) ([]orders.DeliveryZone, error)
}

// deliveryZonesMaxAge: zones change rarely (an owner edits them in the
// admin); a minute of caching keeps the checkout screen cheap.
const deliveryZonesMaxAge = "public, max-age=60"

// listDeliveryZonesHandler serves GET /api/v1/delivery-zones: the active
// zones in display order, [{id, name_ru, name_ky, fee, free_from}]
// (free_from null = never free). An empty list means zones are off and
// the flat delivery_fee of GET /api/v1/app/config applies.
func listDeliveryZonesHandler(repo deliveryZoneLister) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		zones, err := repo.ListActive(r.Context())
		if err != nil {
			return err
		}
		w.Header().Set("Cache-Control", deliveryZonesMaxAge)
		return writeJSON(w, http.StatusOK, zones)
	}
}
