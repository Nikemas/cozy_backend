package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// otpCodeLength must match the OTP field length in the mobile app and the
// site login form (4 digits, same as Nikita).
const otpCodeLength = 4

const telegramGatewayBaseURL = "https://gatewayapi.telegram.org"

// TelegramGatewayClient sends login codes as Telegram messages through the
// Telegram Gateway API (https://core.telegram.org/gateway/api). Like Nikita,
// Telegram generates, expires and checks the code; we only keep the
// request_id as the transaction token.
//
// SendCode first calls checkSendAbility (charged only if the number can get
// the message), then sendVerificationMessage with that request_id, which is
// then free. So a number without Telegram costs nothing and gets a distinct
// error the client can explain ("otp_telegram_unavailable").
type TelegramGatewayClient struct {
	httpClient *http.Client
	baseURL    string
	token      string
	ttl        time.Duration
}

// NewTelegramGatewayClient builds a client. ttl is how long a code stays
// valid; it should equal the auth package's OTP TTL. Gateway accepts
// 30..3600 seconds.
func NewTelegramGatewayClient(token string, ttl time.Duration) *TelegramGatewayClient {
	return &TelegramGatewayClient{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		baseURL:    telegramGatewayBaseURL,
		token:      token,
		ttl:        ttl,
	}
}

type gatewayResponse struct {
	OK     bool                `json:"ok"`
	Error  string              `json:"error"`
	Result gatewayRequestState `json:"result"`
}

type gatewayRequestState struct {
	RequestID          string `json:"request_id"`
	VerificationStatus *struct {
		Status string `json:"status"`
	} `json:"verification_status"`
}

// Channel reports that codes arrive as Telegram messages (see ChannelOf).
func (c *TelegramGatewayClient) Channel() string { return OTPChannelTelegram }

// Gateway accepts a ttl of 30..3600 seconds.
const (
	gatewayMinTTL = 30
	gatewayMaxTTL = 3600
)

func (c *TelegramGatewayClient) ttlSeconds() int {
	return min(max(int(c.ttl.Seconds()), gatewayMinTTL), gatewayMaxTTL)
}

// SendCode texts a code to phone (digits with or without a leading "+")
// and returns the Gateway request_id as the token.
func (c *TelegramGatewayClient) SendCode(ctx context.Context, phone, transactionID string) (string, error) {
	e164 := "+" + strings.TrimPrefix(phone, "+")

	check, err := c.call(ctx, "checkSendAbility", map[string]any{"phone_number": e164})
	if err != nil {
		return "", err
	}
	if !check.OK {
		return "", checkErrorFor(check.Error)
	}
	if check.Result.RequestID == "" {
		// Without a request_id the send would be charged separately and
		// there'd be nothing to verify against later.
		return "", providerError("empty_request_id")
	}

	sent, err := c.call(ctx, "sendVerificationMessage", map[string]any{
		"phone_number": e164,
		"request_id":   check.Result.RequestID,
		"code_length":  otpCodeLength,
		"ttl":          c.ttlSeconds(),
		"payload":      transactionID,
	})
	if err != nil {
		return "", err
	}
	if !sent.OK {
		return "", sendErrorForGateway(sent.Error)
	}
	if sent.Result.RequestID != "" {
		return sent.Result.RequestID, nil
	}
	return check.Result.RequestID, nil
}

// VerifyCode checks code against the Gateway request token.
func (c *TelegramGatewayClient) VerifyCode(ctx context.Context, token, code string) error {
	resp, err := c.call(ctx, "checkVerificationStatus", map[string]any{
		"request_id": token,
		"code":       code,
	})
	if err != nil {
		return err
	}
	if !resp.OK {
		// Our account's problems (token, balance, flood) are 5xx/429, so auth
		// doesn't count them as the user's wrong attempts.
		if err := accountErrorFor(resp.Error); err != nil {
			return err
		}
		// Otherwise the request is unknown or gone (e.g. REQUEST_ID_INVALID):
		// the user has to ask for a new code either way.
		return errOTPExpired()
	}
	if resp.Result.VerificationStatus == nil {
		return errOTPInvalid()
	}
	switch resp.Result.VerificationStatus.Status {
	case "code_valid":
		return nil
	case "code_max_attempts_exceeded":
		return apperr.TooManyRequests("otp_attempts_exceeded", "слишком много неверных попыток, запросите новый код")
	case "expired":
		return errOTPExpired()
	default:
		return errOTPInvalid()
	}
}

func (c *TelegramGatewayClient) call(ctx context.Context, method string, params map[string]any) (*gatewayResponse, error) {
	body, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("telegram gateway %s: encode request: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/"+method, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("telegram gateway %s: build request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		slog.WarnContext(ctx, "notify: telegram gateway unreachable", "method", method, "err", err)
		return nil, apperr.New(http.StatusBadGateway, "sms_provider_unreachable", "не удалось связаться с провайдером SMS")
	}
	defer func() { _ = resp.Body.Close() }()

	var out gatewayResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		slog.WarnContext(ctx, "notify: telegram gateway bad response", "method", method, "http_status", resp.StatusCode, "err", err)
		return nil, providerError(fmt.Sprintf("http_%d", resp.StatusCode))
	}
	if !out.OK {
		slog.WarnContext(ctx, "notify: telegram gateway error", "method", method, "error", out.Error)
	}
	return &out, nil
}

// checkErrorFor maps a checkSendAbility error: account problems, a bad
// number format, "no Telegram on this number", or a generic provider error.
func checkErrorFor(code string) error {
	if err := accountErrorFor(code); err != nil {
		return err
	}
	switch {
	case strings.Contains(code, "PHONE_NUMBER_INVALID"):
		return apperr.BadRequest("invalid_phone", "некорректный номер телефона").WithVariant("provider")
	case strings.Contains(code, "PHONE"), strings.Contains(code, "USER"):
		// The Gateway docs don't list the exact strings; number/user errors
		// (not found, not registered, privacy) mean "no Telegram here".
		return apperr.New(http.StatusUnprocessableEntity, "otp_telegram_unavailable",
			"на этот номер не удалось отправить код в Telegram — проверьте, что Telegram установлен на этом номере")
	default:
		return providerError(code)
	}
}

// sendErrorForGateway maps a sendVerificationMessage error after a
// successful check — a provider-side failure, not the user's fault.
func sendErrorForGateway(code string) error {
	if err := accountErrorFor(code); err != nil {
		return err
	}
	return providerError(code)
}

// accountErrorFor covers errors about our Gateway account rather than the
// phone: bad token, empty balance, flood limits. Returns nil otherwise.
func accountErrorFor(code string) error {
	switch {
	case strings.Contains(code, "ACCESS_TOKEN"):
		return providerError(code)
	case strings.Contains(code, "BALANCE"), strings.Contains(code, "FUNDS"):
		return apperr.New(http.StatusServiceUnavailable, "sms_provider_out_of_funds", "закончился баланс SMS-провайдера")
	case strings.HasPrefix(code, "FLOOD"), strings.Contains(code, "TOO_MANY"):
		return apperr.TooManyRequests("otp_rate_limited", "слишком много запросов кода, попробуйте позже")
	default:
		return nil
	}
}

func errOTPExpired() error {
	return apperr.BadRequest("otp_expired", "код устарел, запросите новый")
}
func errOTPInvalid() error { return apperr.BadRequest("otp_invalid", "неверный код") }

// providerError is the generic Gateway failure, worded like Nikita's
// (errors.*.yaml err.sms_provider_error) so clients show one message.
func providerError(status string) error {
	return apperr.New(http.StatusBadGateway, "sms_provider_error", "ошибка SMS-провайдера (status "+status+")").
		WithParams(map[string]string{"status": status})
}
