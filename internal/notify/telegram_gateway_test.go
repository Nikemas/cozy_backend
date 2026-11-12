package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// gatewayCall records one request the fake Gateway received.
type gatewayCall struct {
	method string
	auth   string
	body   map[string]any
}

// newTestGateway starts a fake Telegram Gateway. respond maps a method
// name to the JSON envelope it answers with.
func newTestGateway(t *testing.T, respond map[string]any) (*TelegramGatewayClient, *[]gatewayCall) {
	t.Helper()
	var calls []gatewayCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		method := r.URL.Path[1:]
		calls = append(calls, gatewayCall{method: method, auth: r.Header.Get("Authorization"), body: body})
		resp, ok := respond[method]
		if !ok {
			t.Errorf("unexpected Gateway method %q", method)
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	c := NewTelegramGatewayClient("tg-token", 5*time.Minute)
	c.httpClient = srv.Client()
	c.baseURL = srv.URL
	return c, &calls
}

func okResult(result map[string]any) map[string]any {
	return map[string]any{"ok": true, "result": result}
}

func gatewayError(code string) map[string]any {
	return map[string]any{"ok": false, "error": code}
}

func appErrCode(t *testing.T, err error) string {
	t.Helper()
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected *apperr.AppError, got %T: %v", err, err)
	}
	return appErr.Code
}

func TestTelegramGatewaySendCode_ChecksAbilityThenSendsWithRequestID(t *testing.T) {
	client, calls := newTestGateway(t, map[string]any{
		"checkSendAbility":        okResult(map[string]any{"request_id": "req-1", "phone_number": "+996700123456"}),
		"sendVerificationMessage": okResult(map[string]any{"request_id": "req-1"}),
	})

	token, err := client.SendCode(context.Background(), "996700123456", "txn1")
	if err != nil {
		t.Fatalf("SendCode() error: %v", err)
	}
	if token != "req-1" {
		t.Errorf("token = %q, want req-1", token)
	}
	if len(*calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(*calls))
	}
	check, send := (*calls)[0], (*calls)[1]
	if check.method != "checkSendAbility" || check.body["phone_number"] != "+996700123456" {
		t.Errorf("check call = %+v, want checkSendAbility for +996700123456", check)
	}
	if check.auth != "Bearer tg-token" {
		t.Errorf("Authorization = %q, want Bearer tg-token", check.auth)
	}
	if send.method != "sendVerificationMessage" {
		t.Fatalf("second call = %q, want sendVerificationMessage", send.method)
	}
	if send.body["request_id"] != "req-1" || send.body["phone_number"] != "+996700123456" {
		t.Errorf("send body = %v, want request_id req-1 and the E.164 phone", send.body)
	}
	if send.body["code_length"] != float64(otpCodeLength) || send.body["ttl"] != float64(300) {
		t.Errorf("send body = %v, want code_length %d and ttl 300", send.body, otpCodeLength)
	}
	if send.body["payload"] != "txn1" {
		t.Errorf("payload = %v, want the transaction id", send.body["payload"])
	}
}

func TestTelegramGatewaySendCode_MapsErrors(t *testing.T) {
	tests := []struct {
		name     string
		check    any
		send     any
		wantCode string
	}{
		{"number without Telegram", gatewayError("PHONE_NUMBER_NOT_FOUND"), nil, "otp_telegram_unavailable"},
		{"invalid phone", gatewayError("PHONE_NUMBER_INVALID"), nil, "invalid_phone"},
		{"bad token", gatewayError("ACCESS_TOKEN_INVALID"), nil, "sms_provider_error"},
		{"no balance", gatewayError("BALANCE_NOT_ENOUGH"), nil, "sms_provider_out_of_funds"},
		{"flood", gatewayError("FLOOD_WAIT_30"), nil, "otp_rate_limited"},
		{"send fails after check", okResult(map[string]any{"request_id": "req-1"}), gatewayError("REQUEST_ID_INVALID"), "sms_provider_error"},
		{"unknown check error", gatewayError("INTERNAL_SERVER_ERROR"), nil, "sms_provider_error"},
		{"check without request_id", okResult(map[string]any{}), nil, "sms_provider_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			respond := map[string]any{"checkSendAbility": tt.check}
			if tt.send != nil {
				respond["sendVerificationMessage"] = tt.send
			}
			client, _ := newTestGateway(t, respond)
			_, err := client.SendCode(context.Background(), "996700123456", "txn1")
			if got := appErrCode(t, err); got != tt.wantCode {
				t.Errorf("code = %q, want %q (err: %v)", got, tt.wantCode, err)
			}
		})
	}
}

func TestTelegramGatewaySendCode_Unreachable(t *testing.T) {
	client := NewTelegramGatewayClient("tg-token", 5*time.Minute)
	client.baseURL = "http://127.0.0.1:1" // nothing listens here
	_, err := client.SendCode(context.Background(), "996700123456", "txn1")
	if got := appErrCode(t, err); got != "sms_provider_unreachable" {
		t.Errorf("code = %q, want sms_provider_unreachable", got)
	}
}

func TestTelegramGatewayVerifyCode(t *testing.T) {
	tests := []struct {
		name     string
		status   any // verification_status.status; nil = field absent
		wantCode string
	}{
		{"valid", "code_valid", ""},
		{"invalid", "code_invalid", "otp_invalid"},
		{"too many attempts", "code_max_attempts_exceeded", "otp_attempts_exceeded"},
		{"expired", "expired", "otp_expired"},
		{"no verification status", nil, "otp_invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := map[string]any{"request_id": "req-1"}
			if tt.status != nil {
				result["verification_status"] = map[string]any{"status": tt.status}
			}
			client, calls := newTestGateway(t, map[string]any{"checkVerificationStatus": okResult(result)})

			err := client.VerifyCode(context.Background(), "req-1", "1234")

			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("VerifyCode() error: %v", err)
				}
			} else if got := appErrCode(t, err); got != tt.wantCode {
				t.Errorf("code = %q, want %q", got, tt.wantCode)
			}
			body := (*calls)[0].body
			if body["request_id"] != "req-1" || body["code"] != "1234" {
				t.Errorf("verify body = %v, want request_id req-1 and code 1234", body)
			}
		})
	}
}

func TestTelegramGatewayVerifyCode_GatewayError(t *testing.T) {
	client, _ := newTestGateway(t, map[string]any{"checkVerificationStatus": gatewayError("REQUEST_ID_INVALID")})
	err := client.VerifyCode(context.Background(), "req-1", "1234")
	if got := appErrCode(t, err); got != "otp_expired" {
		t.Errorf("code = %q, want otp_expired for an unknown request", got)
	}
}

func TestChannelOf(t *testing.T) {
	if got := ChannelOf(NewTelegramGatewayClient("t", time.Minute)); got != OTPChannelTelegram {
		t.Errorf("telegram client channel = %q", got)
	}
	if got := ChannelOf(NewNikitaClient("k")); got != OTPChannelSMS {
		t.Errorf("nikita client channel = %q", got)
	}
	if got := ChannelOf(NewMockClient()); got != OTPChannelSMS {
		t.Errorf("mock client channel = %q", got)
	}
}

func TestTelegramGatewayVerifyCode_AccountErrorsAreNotWrongCodes(t *testing.T) {
	// A broken token or an empty balance must not look like a wrong code
	// (4xx), or auth would count it against the user's attempts.
	for code, want := range map[string]string{
		"ACCESS_TOKEN_INVALID": "sms_provider_error",
		"BALANCE_NOT_ENOUGH":   "sms_provider_out_of_funds",
		"FLOOD_WAIT_5":         "otp_rate_limited",
	} {
		client, _ := newTestGateway(t, map[string]any{"checkVerificationStatus": gatewayError(code)})
		err := client.VerifyCode(context.Background(), "req-1", "1234")
		if got := appErrCode(t, err); got != want {
			t.Errorf("%s: code = %q, want %q", code, got, want)
		}
	}
}

func TestTelegramGatewayTTLIsClamped(t *testing.T) {
	for _, tt := range []struct {
		in   time.Duration
		want int
	}{{10 * time.Second, 30}, {5 * time.Minute, 300}, {2 * time.Hour, 3600}} {
		if got := NewTelegramGatewayClient("t", tt.in).ttlSeconds(); got != tt.want {
			t.Errorf("ttl %v -> %d, want %d", tt.in, got, tt.want)
		}
	}
}
