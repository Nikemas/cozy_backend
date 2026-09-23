package payments

import (
	"context"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// BakaiProviderName is BakaiProvider's Name() and callback path segment.
const BakaiProviderName = "bakai"

// BakaiProvider is a placeholder for Bakai Bank acquiring. The bank hasn't
// given API access yet, so its request/response/signature formats are
// unknown: every operation returns ErrNotConfigured. Replace the bodies
// once the contract arrives — the rest of the flow (Service, webhook
// route, order state) doesn't change.
type BakaiProvider struct {
	webhookToken string
}

func NewBakaiProvider(webhookToken string) *BakaiProvider {
	return &BakaiProvider{webhookToken: webhookToken}
}

func (b *BakaiProvider) Name() string { return BakaiProviderName }

func (b *BakaiProvider) Ready() error { return ErrNotConfigured }

func (b *BakaiProvider) CreatePayment(context.Context, CreateRequest) (*Session, error) {
	return nil, ErrNotConfigured
}

// ParseCallback already enforces the header token (ТЗ §10) so an
// unauthenticated caller learns nothing; an authenticated one still gets
// ErrNotConfigured because the payload format is unknown.
func (b *BakaiProvider) ParseCallback(header http.Header, _ []byte) (*CallbackEvent, error) {
	if !tokenEqual(header.Get(WebhookTokenHeader), b.webhookToken) {
		return nil, apperr.Unauthorized("invalid_webhook_token", "неверный токен вебхука")
	}
	return nil, ErrNotConfigured
}

func (b *BakaiProvider) QueryStatus(context.Context, string) (Status, error) {
	return "", ErrNotConfigured
}
