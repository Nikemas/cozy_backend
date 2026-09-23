package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestTelegram(t *testing.T, handler http.HandlerFunc) *TelegramClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewTelegramClient("123:ABC", "-100500")
	c.baseURL = srv.URL
	c.httpClient = srv.Client()
	return c
}

func TestTelegramSendStaffMessage(t *testing.T) {
	var gotPath string
	var got map[string]any
	c := newTestTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	})

	if err := c.SendStaffMessage(context.Background(), "<b>Новый заказ</b>"); err != nil {
		t.Fatalf("SendStaffMessage: %v", err)
	}
	if gotPath != "/bot123:ABC/sendMessage" {
		t.Errorf("path = %q", gotPath)
	}
	if got["chat_id"] != "-100500" || got["text"] != "<b>Новый заказ</b>" || got["parse_mode"] != "HTML" {
		t.Errorf("body = %v", got)
	}
}

func TestTelegramSendStaffMessageAPIError(t *testing.T) {
	c := newTestTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`))
	})
	err := c.SendStaffMessage(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("want chat not found error, got %v", err)
	}
}

func TestTelegramNetworkErrorDoesNotLeakToken(t *testing.T) {
	c := NewTelegramClient("123:SECRET", "1")
	c.baseURL = "http://127.0.0.1:1" // nothing listens here
	err := c.SendStaffMessage(context.Background(), "x")
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error leaks bot token: %v", err)
	}
}

func TestNopStaffMessenger(t *testing.T) {
	if err := (NopStaffMessenger{}).SendStaffMessage(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
}
