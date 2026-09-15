package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// NikitaClient talks to the Nikita SMSPro OTP API
// (smspro.nikita.kg-OTP-api.pdf). Code generation, expiry and verification
// all happen on Nikita's side — we only keep the transaction token.
type NikitaClient struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
}

func NewNikitaClient(apiKey string) *NikitaClient {
	return &NikitaClient{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		baseURL:    "https://smspro.nikita.kg",
		apiKey:     apiKey,
	}
}

type nikitaResponse struct {
	Status      any    `json:"status"` // Nikita returns an int on send, a string on verify
	Description string `json:"description"`
	Token       string `json:"token"`
}

func (r nikitaResponse) statusCode() string {
	return fmt.Sprintf("%v", r.Status)
}

func (c *NikitaClient) SendCode(ctx context.Context, phone, transactionID string) (string, error) {
	body, err := json.Marshal(map[string]string{
		"transaction_id": transactionID,
		"phone":          phone,
	})
	if err != nil {
		return "", err
	}

	var resp nikitaResponse
	if err := c.call(ctx, "/api/otp/send", body, &resp); err != nil {
		return "", err
	}

	if err := sendErrorFor(resp.statusCode()); err != nil {
		return "", err
	}
	return resp.Token, nil
}

func (c *NikitaClient) VerifyCode(ctx context.Context, token, code string) error {
	body, err := json.Marshal(map[string]string{
		"token": token,
		"code":  code,
	})
	if err != nil {
		return err
	}

	var resp nikitaResponse
	if err := c.call(ctx, "/api/otp/verify", body, &resp); err != nil {
		return err
	}

	return verifyErrorFor(resp.statusCode())
}

func (c *NikitaClient) call(ctx context.Context, path string, body []byte, out *nikitaResponse) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-KEY", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return apperr.New(http.StatusBadGateway, "sms_provider_unreachable", "не удалось связаться с провайдером SMS")
	}
	defer resp.Body.Close()

	return json.NewDecoder(resp.Body).Decode(out)
}

// sendErrorFor maps Nikita's otp/send status codes to AppError.
func sendErrorFor(status string) error {
	switch status {
	case "0":
		return nil
	case "7":
		return apperr.BadRequest("invalid_phone", "некорректный номер телефона")
	case "4":
		return apperr.New(http.StatusServiceUnavailable, "sms_provider_out_of_funds", "закончился баланс SMS-провайдера")
	case "10":
		return apperr.New(http.StatusTooManyRequests, "otp_duplicate_request", "код уже запрошен, подождите")
	default:
		return apperr.New(http.StatusBadGateway, "sms_provider_error", "ошибка SMS-провайдера (status "+status+")")
	}
}

// verifyErrorFor maps Nikita's otp/verify status codes to AppError.
func verifyErrorFor(status string) error {
	switch status {
	case "0":
		return nil
	case "13":
		return apperr.BadRequest("otp_expired", "код устарел, запросите новый")
	case "14", "12":
		return apperr.BadRequest("otp_invalid", "неверный код")
	default:
		return apperr.New(http.StatusBadGateway, "sms_provider_error", "ошибка SMS-провайдера (status "+status+")")
	}
}
