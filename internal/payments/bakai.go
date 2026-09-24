package payments

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// BakaiProviderName is BakaiProvider's Name() and callback path segment.
const BakaiProviderName = "bakai"

// BakaiProvider talks to Bakai Bank's OpenBanking API: a payment session is
// a per-order pay link created via POST /api/PayLink/CreatePayLink, and the
// bank posts a plain-JSON webhook back when it settles. Ported from the
// Dordoi ERP project's integration, which verified the same endpoints
// against a real payment on 2026-06-25 — the request/response shapes and
// the webhook field names below are load-bearing, not guesses.
//
// Auth is a bearer token obtained once, out of band, from POST /Auth/Login
// with single-use merchant credentials; the token is an opaque JWE with no
// readable expiry and no refresh endpoint, so it's supplied via config and
// re-provisioned by hand if CreatePayLink starts returning 401.
type BakaiProvider struct {
	baseURL      string
	token        string // CreatePayLink bearer
	qrToken      string // GenerateQR bearer; empty disables QREnabled
	accountNo    string // receiving account for GenerateQR
	currencyID   int    // currency code for GenerateQR (417 = KGS)
	webhookToken string
	hc           *http.Client
}

// NewBakaiProvider builds a BakaiProvider. qrToken/accountNo/currencyID are
// only needed for GenerateQR (an alternative, scannable checkout — nothing
// in Provider calls it yet); leave qrToken empty to skip it.
func NewBakaiProvider(baseURL, token, qrToken, accountNo string, currencyID int, webhookToken string) *BakaiProvider {
	if baseURL == "" {
		baseURL = "https://openbanking-api.bakai.kg"
	}
	return &BakaiProvider{
		baseURL:      strings.TrimRight(baseURL, "/"),
		token:        token,
		qrToken:      qrToken,
		accountNo:    accountNo,
		currencyID:   currencyID,
		webhookToken: webhookToken,
		hc:           &http.Client{Timeout: 25 * time.Second},
	}
}

func (b *BakaiProvider) Name() string { return BakaiProviderName }

// Ready reports whether CreatePayLink is configured. QREnabled is a
// separate, optional capability (see GenerateQR) — Ready doesn't require it.
func (b *BakaiProvider) Ready() error {
	if b.token == "" {
		return ErrNotConfigured
	}
	return nil
}

// QREnabled reports whether GenerateQR is configured.
func (b *BakaiProvider) QREnabled() bool { return b.qrToken != "" }

type createPayLinkRequest struct {
	Amount        float64 `json:"amount"`
	TransactionID string  `json:"transactionID"`
	Comment       string  `json:"comment"`
	RedirectURL   string  `json:"redirectURL"`
}

// CreatePayment creates a Bakai pay link for req.Amount and returns its URL
// as Session.RedirectURL. req.PaymentID is sent as transactionID — Bakai
// echoes it back as the webhook's operationID, which is how ParseCallback
// matches the callback to this session, so Session.ExternalID is the same
// req.PaymentID (Bakai's CreatePayLink response carries no id of its own,
// just the URL).
func (b *BakaiProvider) CreatePayment(ctx context.Context, req CreateRequest) (*Session, error) {
	if err := b.Ready(); err != nil {
		return nil, err
	}
	if req.ReturnURL == "" {
		return nil, fmt.Errorf("bakai: ReturnURL is required")
	}
	body, err := json.Marshal(createPayLinkRequest{
		Amount:        req.Amount,
		TransactionID: req.PaymentID,
		Comment:       req.OrderNumber,
		RedirectURL:   req.ReturnURL,
	})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		b.baseURL+"/api/PayLink/CreatePayLink", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+b.token)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := b.hc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("bakai CreatePayLink: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	out := strings.TrimSpace(string(raw))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("bakai CreatePayLink: HTTP %d: %s", resp.StatusCode, out)
	}
	// Defensive: some gateways wrap a plain string in quotes.
	out = strings.Trim(out, "\"")
	if !strings.HasPrefix(out, "http") {
		return nil, fmt.Errorf("bakai CreatePayLink: unexpected response %q", out)
	}
	return &Session{ExternalID: req.PaymentID, RedirectURL: out}, nil
}

type generateQRRequest struct {
	AccountNo   string  `json:"accountNo"`
	CurrencyID  int     `json:"currencyId"`
	Amount      float64 `json:"amount"`
	OperationID string  `json:"operationID"`
	QrTtlUnits  int     `json:"qrTtlUnits"` // 5 = Years
	QrTtl       int     `json:"qrTtl"`
}

type generateQRResponse struct {
	QrImage          string `json:"qrImage"`
	QrLink           string `json:"qrLink"`
	QrImageWithFrame string `json:"qrImageWithFrame"`
}

// GenerateQR creates a scannable EMVCo payment QR via Bakai OpenBanking
// POST /api/Qr/GenerateQR, as an alternative to CreatePayment's redirect
// link. operationID should be a payments.id (same role as CreatePayment's
// transactionID) — the webhook echoes it back as operationID, which is
// how HandleCallback matches the payment either way (see payments.Service
// GetPaymentQR, which stores operationID into payments.provider_tx_id the
// same way openSession does for CreatePayment).
//
// Returns qrLink (the EMVCo payload some wallets scan directly) and
// qrImage, a ready-to-embed "data:image/png;base64,..." URL built from
// Bakai's own qrImage field so callers don't need a QR-rendering library.
func (b *BakaiProvider) GenerateQR(ctx context.Context, amount float64, operationID string) (qrLink, qrImage string, err error) {
	if !b.QREnabled() {
		return "", "", ErrNotConfigured
	}
	body, err := json.Marshal(generateQRRequest{
		AccountNo:   b.accountNo,
		CurrencyID:  b.currencyID,
		Amount:      amount,
		OperationID: operationID,
		QrTtlUnits:  5, // Years
		QrTtl:       5,
	})
	if err != nil {
		return "", "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		b.baseURL+"/api/Qr/GenerateQR", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+b.qrToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.hc.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("bakai GenerateQR: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("bakai GenerateQR: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out generateQRResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", "", fmt.Errorf("bakai GenerateQR decode: %w", err)
	}
	if out.QrLink == "" {
		return "", "", fmt.Errorf("bakai GenerateQR: empty qrLink")
	}
	img := strings.TrimSpace(out.QrImage)
	if img != "" && !strings.HasPrefix(img, "data:") {
		img = "data:image/png;base64," + img
	}
	return out.QrLink, img, nil
}

// bakaiWebhook is Bakai's payment callback body (plain JSON, no auth
// header of its own — the reverse proxy is configured to inject
// WebhookTokenHeader, see ParseCallback). Field names and the echo
// behavior (our transactionID comes back as operationID, our comment as
// comment) were verified against a real Bakai payment 2026-06-25.
type bakaiWebhook struct {
	AccountNo       string  `json:"accountNo"`
	Amount          float64 `json:"amount"`
	CurrencyID      int     `json:"currencyID"`
	OperationID     string  `json:"operationID"`     // = our transactionID (payments.id)
	Comment         string  `json:"comment"`         // = our comment (order number)
	QrTransactionID string  `json:"qrTransactionID"` // Bakai's own per-payment id
	ElqrID          string  `json:"elqrID"`          // per-payment fallback id
	OperationState  string  `json:"operationState"`  // "success" => paid
}

// ParseCallback authenticates the callback via WebhookTokenHeader (the
// bank strips query strings, so the ТЗ has the reverse proxy inject the
// header — see WebhookTokenHeader's doc comment), then parses it.
// ExternalID is operationID, which is our own payments.id (CreatePayment
// sent it as transactionID) — a dynamic pay link per payment, unlike
// Dordoi ERP's static per-branch QR, so operationID is already a unique,
// idempotent key and no fallback to qrTransactionID/elqrID is needed for
// matching (HandleCallback's own payment-row lookup handles duplicates).
func (b *BakaiProvider) ParseCallback(header http.Header, body []byte) (*CallbackEvent, error) {
	if !tokenEqual(header.Get(WebhookTokenHeader), b.webhookToken) {
		return nil, apperr.Unauthorized("invalid_webhook_token", "неверный токен вебхука")
	}
	var w bakaiWebhook
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, apperr.BadRequest("invalid_callback", "некорректное уведомление об оплате")
	}
	if w.OperationID == "" {
		return nil, apperr.BadRequest("invalid_callback", "некорректное уведомление об оплате")
	}
	state := strings.ToLower(strings.TrimSpace(w.OperationState))
	var status Status
	switch state {
	case "success", "processed", "paid", "ok":
		status = StatusPaid
	case "cancelled", "canceled", "cancel":
		status = StatusCancelled
	default:
		status = StatusFailed
	}
	return &CallbackEvent{
		ExternalID: strings.TrimSpace(w.OperationID),
		Status:     status,
		Amount:     w.Amount,
		Raw:        json.RawMessage(body),
	}, nil
}

// QueryStatus: Bakai's OpenBanking API documents no "get payment status"
// endpoint, only the webhook push — the pending-expiry job is the only
// reconciliation available until the bank adds one.
func (b *BakaiProvider) QueryStatus(context.Context, string) (Status, error) {
	return "", ErrNotConfigured
}
