package payments

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// MockProviderName is MockProvider's Name() and callback path segment.
const MockProviderName = "mock"

// MockCheckoutPath is where MockProvider sends the customer: a local page
// (served by internal/httpapi only while the mock is the active provider)
// with "pay" / "decline" buttons standing in for the bank's page.
const MockCheckoutPath = "/api/v1/payments/mock/checkout/"

// MockProvider imitates a card acquirer entirely in-process. It opens a
// "session" by minting a random external id, and accepts callbacks shaped
// as mockCallback authenticated with WebhookTokenHeader — the same header
// check the real Bakai integration is meant to use, so the webhook path is
// exercised for real.
type MockProvider struct {
	baseURL string
	token   string

	mu       sync.Mutex
	statuses map[string]Status // last callback status per external id, for QueryStatus
}

// NewMockProvider returns a mock whose checkout URLs are rooted at
// baseURL. token is the callback secret; empty means a random per-process
// one (only the in-process mock checkout page knows it then).
func NewMockProvider(baseURL, token string) *MockProvider {
	if token == "" {
		token = randomHex(16)
	}
	return &MockProvider{baseURL: baseURL, token: token, statuses: map[string]Status{}}
}

func (m *MockProvider) Name() string { return MockProviderName }

func (m *MockProvider) Ready() error { return nil }

func (m *MockProvider) CreatePayment(_ context.Context, _ CreateRequest) (*Session, error) {
	id := "mock_" + randomHex(16)
	m.mu.Lock()
	m.statuses[id] = StatusPending
	m.mu.Unlock()
	return &Session{ExternalID: id, RedirectURL: m.baseURL + MockCheckoutPath + id}, nil
}

// mockCallback is the mock's callback body.
type mockCallback struct {
	ExternalID string  `json:"external_id"`
	Status     Status  `json:"status"`
	Amount     float64 `json:"amount"`
}

func (m *MockProvider) ParseCallback(header http.Header, body []byte) (*CallbackEvent, error) {
	if !tokenEqual(header.Get(WebhookTokenHeader), m.token) {
		return nil, apperr.Unauthorized("invalid_webhook_token", "неверный токен вебхука")
	}
	var cb mockCallback
	if err := json.Unmarshal(body, &cb); err != nil || cb.ExternalID == "" || !cb.Status.Valid() {
		return nil, apperr.BadRequest("invalid_callback", "некорректное уведомление об оплате")
	}
	m.mu.Lock()
	m.statuses[cb.ExternalID] = cb.Status
	m.mu.Unlock()
	return &CallbackEvent{ExternalID: cb.ExternalID, Status: cb.Status, Amount: cb.Amount, Raw: json.RawMessage(body)}, nil
}

func (m *MockProvider) QueryStatus(_ context.Context, externalID string) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.statuses[externalID]
	if !ok {
		return "", apperr.NotFound("payment_not_found", "платёж не найден")
	}
	return st, nil
}

// SignedCallback builds the header+body the mock "bank" would send for a
// payment reaching status — used by the mock checkout page so its
// confirm button goes through the exact same ParseCallback/HandleCallback
// path as a real webhook.
func (m *MockProvider) SignedCallback(externalID string, status Status, amount float64) (http.Header, []byte, error) {
	body, err := json.Marshal(mockCallback{ExternalID: externalID, Status: status, Amount: amount})
	if err != nil {
		return nil, nil, err
	}
	h := http.Header{}
	h.Set(WebhookTokenHeader, m.token)
	h.Set("Content-Type", "application/json")
	return h, body, nil
}

func tokenEqual(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func randomHex(n int) string {
	b := make([]byte, n)
	// crypto/rand.Read never returns an error on supported platforms
	// (Go 1.24+ panics internally instead).
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
