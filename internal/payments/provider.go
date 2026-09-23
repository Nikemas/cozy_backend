// Package payments implements online card payment for orders (ТЗ §10:
// Bakai Business Banking acquiring + payment webhook). The bank-specific
// part sits behind Provider; until Bakai grants API access the server runs
// with MockProvider (PAYMENTS_PROVIDER=mock), which issues a local checkout
// page that can simulate success/failure, so the whole flow — order with
// payment_method=online_card → payment_url → callback → order paid — is
// exercisable end to end. BakaiProvider is a stub that reports "not
// configured" everywhere until the bank's API contract is known.
package payments

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/orders"
)

// Status is a payment's state (the payment_status enum). An alias of
// orders.PaymentStatus because Order carries the same value.
type Status = orders.PaymentStatus

const (
	StatusPending   = orders.PaymentPending
	StatusPaid      = orders.PaymentPaid
	StatusFailed    = orders.PaymentFailed
	StatusCancelled = orders.PaymentCancelled
	StatusRefunded  = orders.PaymentRefunded
)

// Currency is the only currency the store charges in (payments.currency
// has a CHECK for it, migration 000021).
const Currency = "KGS"

// WebhookTokenHeader carries the shared secret on provider callbacks. The
// ТЗ (§10) calls for a header rather than a ?token= query parameter
// because Bakai is known to strip query strings from callback URLs; the
// reverse proxy can inject it if the bank itself can't.
const WebhookTokenHeader = "X-Webhook-Token"

// CreateRequest is what a provider needs to open a checkout session.
type CreateRequest struct {
	PaymentID   string // payments.id — our own idempotency key for the session
	OrderID     string
	OrderNumber string
	Amount      float64
	Currency    string
	ReturnURL   string // where the customer lands after paying
}

// Session is an opened checkout session: the provider's id for it (stored
// as payments.provider_tx_id) and the URL to send the customer to.
type Session struct {
	ExternalID  string
	RedirectURL string
}

// CallbackEvent is a verified, parsed provider callback.
type CallbackEvent struct {
	ExternalID string
	Status     Status
	Amount     float64
	Raw        json.RawMessage // stored as payments.raw_webhook
}

// Provider is one payment gateway.
type Provider interface {
	// Name is the provider's id: the payments.provider value and the
	// {provider} path segment of its callback URL.
	Name() string
	// Ready reports whether the provider can take payments at all (e.g.
	// credentials configured). Checked before an online order is created,
	// so an unusable provider never reserves stock.
	Ready() error
	// CreatePayment opens a checkout session for one payment.
	CreatePayment(ctx context.Context, req CreateRequest) (*Session, error)
	// ParseCallback authenticates a callback (signature/token) and parses
	// it. It must return an *apperr.AppError for a rejected callback.
	ParseCallback(header http.Header, body []byte) (*CallbackEvent, error)
	// QueryStatus asks the provider for a payment's current state — for a
	// future reconciliation job; nothing calls it on the request path yet.
	QueryStatus(ctx context.Context, externalID string) (Status, error)
}

// ErrNotConfigured is returned by a provider that can't take payments yet.
var ErrNotConfigured = apperr.New(http.StatusServiceUnavailable, "payments_not_configured",
	"онлайн-оплата пока недоступна, выберите оплату при получении")

// NewProvider builds the provider selected by cfg.PaymentsProvider.
func NewProvider(cfg *config.Config) (Provider, error) {
	switch cfg.PaymentsProvider {
	case config.PaymentsProviderMock:
		return NewMockProvider(cfg.PaymentsBaseURL(), cfg.BakaiWebhookToken), nil
	case config.PaymentsProviderBakai:
		return NewBakaiProvider(cfg.BakaiWebhookToken), nil
	default:
		return nil, fmt.Errorf("payments: unknown provider %q", cfg.PaymentsProvider)
	}
}
