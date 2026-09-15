package httpapi

import (
	"database/sql"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/auth"
)

// RegisterCustomerRoutes mounts the logged-in-customer JSON API for the
// Flutter mobile app under /api/v1/*: the customer's own profile, favorites,
// delivery addresses, and FCM device-token registration (Task G / §6 of the
// ТЗ). Every route is behind authSvc.RequireCustomer — there is no anonymous
// access to any of them. Split into one register*Routes function per domain
// (customer_profile.go, favorites.go, addresses.go, devices.go) for
// file-size/readability, but this is the single exported entry point
// cmd/server/main.go needs to call.
func RegisterCustomerRoutes(mux *http.ServeMux, db *sql.DB, authSvc *auth.Service) {
	registerCustomerProfileRoutes(mux, db, authSvc)
	registerFavoritesRoutes(mux, db, authSvc)
	registerAddressesRoutes(mux, db, authSvc)
	registerDevicesRoutes(mux, db, authSvc)
}
