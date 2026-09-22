package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

func newTestNikitaClient(t *testing.T, handler http.HandlerFunc) *NikitaClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &NikitaClient{httpClient: srv.Client(), baseURL: srv.URL, apiKey: "test-key"}
}

func appErrStatus(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	appErr, ok := err.(*apperr.AppError)
	if !ok {
		t.Fatalf("expected *apperr.AppError, got %T: %v", err, err)
	}
	return appErr.Status
}

func TestNikitaClientSendCode(t *testing.T) {
	var gotPath, gotAPIKey string
	var gotBody map[string]string

	client := newTestNikitaClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("X-API-KEY")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)

		// Nikita returns an int status on otp/send.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":      0,
			"description": "Code Sent",
			"token":       "abc123token",
		})
	})

	token, err := client.SendCode(context.Background(), "996700123456", "txn1")
	if err != nil {
		t.Fatalf("SendCode() unexpected error: %v", err)
	}
	if token != "abc123token" {
		t.Errorf("SendCode() token = %q, want %q", token, "abc123token")
	}
	if gotPath != "/api/otp/send" {
		t.Errorf("path = %q, want %q", gotPath, "/api/otp/send")
	}
	if gotAPIKey != "test-key" {
		t.Errorf("X-API-KEY = %q, want %q", gotAPIKey, "test-key")
	}
	if gotBody["phone"] != "996700123456" || gotBody["transaction_id"] != "txn1" {
		t.Errorf("request body = %+v, want phone=996700123456 transaction_id=txn1", gotBody)
	}
}

func TestNikitaClientSendCodeErrors(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		wantHTTP   int
		wantErrNil bool
	}{
		{"invalid phone", 7, http.StatusBadRequest, false},
		{"out of funds", 4, http.StatusServiceUnavailable, false},
		{"duplicate transaction", 10, http.StatusTooManyRequests, false},
		{"unmapped provider error", 2, http.StatusBadGateway, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client := newTestNikitaClient(t, func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": c.status, "description": "x"})
			})

			_, err := client.SendCode(context.Background(), "996700123456", "txn1")
			if got := appErrStatus(t, err); got != c.wantHTTP {
				t.Errorf("SendCode() HTTP status = %d, want %d", got, c.wantHTTP)
			}
		})
	}
}

func TestNikitaClientVerifyCode(t *testing.T) {
	var gotBody map[string]string

	client := newTestNikitaClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/otp/verify" {
			t.Errorf("path = %q, want /api/otp/verify", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)

		// Nikita returns a string status on otp/verify (per its docs).
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":      "0",
			"description": "Code Valid",
		})
	})

	if err := client.VerifyCode(context.Background(), "tok1", "123456"); err != nil {
		t.Fatalf("VerifyCode() unexpected error: %v", err)
	}
	if gotBody["token"] != "tok1" || gotBody["code"] != "123456" {
		t.Errorf("request body = %+v, want token=tok1 code=123456", gotBody)
	}
}

func TestNikitaClientVerifyCodeErrors(t *testing.T) {
	cases := []struct {
		name     string
		status   string
		wantHTTP int
	}{
		{"expired", "13", http.StatusBadRequest},
		{"invalid code", "14", http.StatusBadRequest},
		{"invalid token", "12", http.StatusBadRequest},
		{"unmapped provider error", "3", http.StatusBadGateway},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client := newTestNikitaClient(t, func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": c.status, "description": "x"})
			})

			err := client.VerifyCode(context.Background(), "tok1", "000000")
			if got := appErrStatus(t, err); got != c.wantHTTP {
				t.Errorf("VerifyCode() HTTP status = %d, want %d", got, c.wantHTTP)
			}
		})
	}
}

func TestNikitaClientUnreachable(t *testing.T) {
	client := &NikitaClient{httpClient: http.DefaultClient, baseURL: "http://127.0.0.1:0", apiKey: "test-key"}

	_, err := client.SendCode(context.Background(), "996700123456", "txn1")
	if got := appErrStatus(t, err); got != http.StatusBadGateway {
		t.Errorf("SendCode() HTTP status = %d, want %d", got, http.StatusBadGateway)
	}
}
