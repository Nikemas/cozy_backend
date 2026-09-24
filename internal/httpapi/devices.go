package httpapi

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/auth"
	"github.com/Nikemas/cozy_backend/internal/storefront"
)

// deviceTokenRegisterer is the subset of *storefront.DeviceTokenRepo this
// handler depends on, so handler-level tests can inject a fake instead of a
// live database — mirrors stockUpserter in admin_catalog.go.
type deviceTokenRegisterer interface {
	Register(ctx context.Context, customerID, fcmToken, platform string) error
}

// registerDevicesRoutes mounts the customer device-token registration
// endpoint under /api/v1/devices, gated by authSvc.RequireCustomer — there
// is no anonymous access. Called from RegisterCustomerRoutes
// (customer.go), the one exported entry point for this whole file group.
func registerDevicesRoutes(mux *http.ServeMux, db *sql.DB, authSvc *auth.Service) {
	devices := storefront.NewDeviceTokenRepo(db)

	mux.Handle("POST /api/v1/devices", authSvc.RequireCustomer(apperr.Wrap(registerDeviceHandler(devices))))
	mux.Handle("DELETE /api/v1/devices", authSvc.RequireCustomer(apperr.Wrap(unregisterDeviceHandler(sqlDeviceTokenDeleter{db: db}))))
}

// deviceTokenDeleter removes one FCM token, scoped to its owner.
type deviceTokenDeleter interface {
	DeleteForCustomer(ctx context.Context, customerID, fcmToken string) error
}

// sqlDeviceTokenDeleter is the Postgres deviceTokenDeleter. Scoped by
// customer_id so a customer can only unregister their own devices.
type sqlDeviceTokenDeleter struct{ db *sql.DB }

func (d sqlDeviceTokenDeleter) DeleteForCustomer(ctx context.Context, customerID, fcmToken string) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM device_tokens WHERE fcm_token = $1 AND customer_id = $2`, fcmToken, customerID)
	return err
}

// unregisterDeviceHandler handles DELETE /api/v1/devices {"token": "..."}
// — called by the app on logout so the device stops receiving the
// customer's pushes. Always 204 (idempotent): a token that is unknown,
// already gone or empty is not an error. "fcm_token" (POST's field name)
// is accepted as an alias.
func unregisterDeviceHandler(devices deviceTokenDeleter) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, ok := auth.CustomerIDFromContext(r.Context())
		if !ok {
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}

		var req struct {
			Token    string `json:"token"`
			FCMToken string `json:"fcm_token"`
		}
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		token := req.Token
		if token == "" {
			token = req.FCMToken
		}

		if token != "" {
			if err := devices.DeleteForCustomer(r.Context(), customerID, token); err != nil {
				return err
			}
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
}

type deviceRequest struct {
	FCMToken string `json:"fcm_token"`
	Platform string `json:"platform"`
}

// registerDeviceHandler upserts an FCM device token for the authenticated
// customer — see storefront.DeviceTokenRepo.Register for the upsert-by-
// fcm_token semantics (a token re-registered under a different customer
// moves to the new owner).
func registerDeviceHandler(devices deviceTokenRegisterer) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, ok := auth.CustomerIDFromContext(r.Context())
		if !ok {
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}

		var req deviceRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		if err := devices.Register(r.Context(), customerID, req.FCMToken, req.Platform); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
}
