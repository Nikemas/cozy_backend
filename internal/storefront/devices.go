package storefront

import (
	"context"
	"database/sql"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// Platforms accepted by DeviceTokenRepo.Register — matches the `platform`
// comment on migration 000014_create_device_tokens ("ios|android").
const (
	PlatformIOS     = "ios"
	PlatformAndroid = "android"
)

func validPlatform(platform string) bool {
	return platform == PlatformIOS || platform == PlatformAndroid
}

// DeviceTokenRepo is the read/write contract for a customer's registered
// FCM device tokens (migration 000014_create_device_tokens), used to push
// order-status notifications to the Flutter app.
type DeviceTokenRepo struct {
	db *sql.DB
}

func NewDeviceTokenRepo(db *sql.DB) *DeviceTokenRepo {
	return &DeviceTokenRepo{db: db}
}

// Register upserts an FCM device token for customerID. fcm_token is UNIQUE,
// so re-registering a token that's already on file (e.g. the same physical
// device, reinstalled or logged in as a different customer) updates the
// owning customer_id and platform in place rather than erroring — this
// deliberately lets a fresher registration "steal" a token from whichever
// customer last owned it, since a stale FK to the previous customer would
// otherwise silently keep sending that customer's push notifications to a
// device they no longer use.
func (r *DeviceTokenRepo) Register(ctx context.Context, customerID, fcmToken, platform string) error {
	if !validPlatform(platform) {
		return apperr.BadRequest("invalid_platform", "platform должен быть ios или android")
	}

	const q = `
		INSERT INTO device_tokens (customer_id, fcm_token, platform)
		VALUES ($1, $2, $3)
		ON CONFLICT (fcm_token) DO UPDATE
		SET customer_id = EXCLUDED.customer_id, platform = EXCLUDED.platform`
	_, err := r.db.ExecContext(ctx, q, customerID, fcmToken, platform)
	return err
}

// TokensForCustomer returns every FCM token registered to customerID —
// one per device the customer is logged in on.
func (r *DeviceTokenRepo) TokensForCustomer(ctx context.Context, customerID string) ([]string, error) {
	const q = `SELECT fcm_token FROM device_tokens WHERE customer_id = $1 ORDER BY created_at`
	rows, err := r.db.QueryContext(ctx, q, customerID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var tokens []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// DeleteToken removes a token FCM reported as unregistered/invalid, so it
// isn't retried on every future notification. Deleting a token that's
// already gone is not an error.
func (r *DeviceTokenRepo) DeleteToken(ctx context.Context, fcmToken string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM device_tokens WHERE fcm_token = $1`, fcmToken)
	return err
}
