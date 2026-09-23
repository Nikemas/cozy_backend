package push

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// fakeGoogle serves both the OAuth2 token endpoint and the FCM send
// endpoint, so FCMSender is exercised end-to-end over real HTTP.
type fakeGoogle struct {
	t          *testing.T
	pub        *rsa.PublicKey
	srv        *httptest.Server
	tokenCalls atomic.Int32
	sendCalls  atomic.Int32
	lastSend   fcmRequest
	lastAuth   string
	lastPath   string
	sendStatus int
	sendBody   string
}

func newFakeGoogle(t *testing.T, pub *rsa.PublicKey) *fakeGoogle {
	fg := &fakeGoogle{t: t, pub: pub, sendStatus: http.StatusOK, sendBody: `{"name":"projects/p/messages/1"}`}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		fg.tokenCalls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if got := r.PostForm.Get("grant_type"); got != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			t.Errorf("grant_type = %q", got)
		}
		tok, err := jwt.Parse(r.PostForm.Get("assertion"), func(*jwt.Token) (any, error) { return fg.pub, nil },
			jwt.WithValidMethods([]string{"RS256"}))
		if err != nil {
			t.Errorf("assertion does not verify with the service-account public key: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		claims := tok.Claims.(jwt.MapClaims)
		if claims["iss"] != "svc@test.iam.gserviceaccount.com" || claims["scope"] != fcmScope || claims["aud"] != fg.srv.URL+"/token" {
			t.Errorf("unexpected assertion claims: %v", claims)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at-123", "expires_in": 3600, "token_type": "Bearer"})
	})
	mux.HandleFunc("POST /v1/projects/{project}/messages:send", func(w http.ResponseWriter, r *http.Request) {
		fg.sendCalls.Add(1)
		fg.lastAuth = r.Header.Get("Authorization")
		fg.lastPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&fg.lastSend)
		w.WriteHeader(fg.sendStatus)
		_, _ = w.Write([]byte(fg.sendBody))
	})
	fg.srv = httptest.NewServer(mux)
	t.Cleanup(fg.srv.Close)
	return fg
}

func newTestSender(t *testing.T) (*FCMSender, *fakeGoogle) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	fg := newFakeGoogle(t, &key.PublicKey)
	creds, _ := json.Marshal(map[string]string{
		"type":         "service_account",
		"project_id":   "cozy-test",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})),
		"client_email": "svc@test.iam.gserviceaccount.com",
		"token_uri":    fg.srv.URL + "/token",
	})
	path := filepath.Join(t.TempDir(), "sa.json")
	if err := os.WriteFile(path, creds, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewFCMSenderFromFile(path, "")
	if err != nil {
		t.Fatalf("NewFCMSenderFromFile: %v", err)
	}
	s.baseURL = fg.srv.URL
	s.httpClient = fg.srv.Client()
	return s, fg
}

func TestFCMSenderSendsV1MessageWithOAuthToken(t *testing.T) {
	s, fg := newTestSender(t)

	msg := Message{Title: "Заказ подтверждён", Body: "Собираем", Data: map[string]string{"order_id": "o1"}, AndroidChannelID: "orders"}
	if err := s.Send(context.Background(), "device-token-1", msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := s.Send(context.Background(), "device-token-2", msg); err != nil {
		t.Fatalf("second Send: %v", err)
	}

	if fg.tokenCalls.Load() != 1 {
		t.Errorf("token endpoint called %d times, want 1 (access token must be cached)", fg.tokenCalls.Load())
	}
	if fg.sendCalls.Load() != 2 {
		t.Errorf("send endpoint called %d times, want 2", fg.sendCalls.Load())
	}
	if fg.lastPath != "/v1/projects/cozy-test/messages:send" {
		t.Errorf("path = %q", fg.lastPath)
	}
	if fg.lastAuth != "Bearer at-123" {
		t.Errorf("Authorization = %q", fg.lastAuth)
	}
	m := fg.lastSend.Message
	if m.Token != "device-token-2" || m.Notification == nil || m.Notification.Title != "Заказ подтверждён" || m.Data["order_id"] != "o1" {
		t.Errorf("unexpected message payload: %+v", m)
	}
	if m.Android == nil || m.Android.Notification == nil || m.Android.Notification.ChannelID != "orders" {
		t.Errorf("android channel_id not set: %+v", m.Android)
	}
}

func TestFCMSenderRefreshesExpiredAccessToken(t *testing.T) {
	s, fg := newTestSender(t)
	now := time.Now()
	s.now = func() time.Time { return now }

	if err := s.Send(context.Background(), "t", Message{Title: "x"}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour) // past expires_in (minus skew)
	if err := s.Send(context.Background(), "t", Message{Title: "x"}); err != nil {
		t.Fatal(err)
	}
	if fg.tokenCalls.Load() != 2 {
		t.Errorf("token endpoint called %d times, want 2", fg.tokenCalls.Load())
	}
}

func TestFCMSenderClassifiesErrors(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		body        string
		wantInvalid bool
	}{
		{"unregistered", 404, `{"error":{"code":404,"message":"Requested entity was not found.","status":"NOT_FOUND","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"UNREGISTERED"}]}}`, true},
		{"sender id mismatch", 403, `{"error":{"code":403,"message":"SenderId mismatch","status":"PERMISSION_DENIED","details":[{"errorCode":"SENDER_ID_MISMATCH"}]}}`, true},
		{"invalid token", 400, `{"error":{"code":400,"message":"The registration token is not a valid FCM registration token","status":"INVALID_ARGUMENT","details":[{"errorCode":"INVALID_ARGUMENT"}]}}`, true},
		{"invalid payload is not a token problem", 400, `{"error":{"code":400,"message":"Invalid value at 'message.data'","status":"INVALID_ARGUMENT","details":[{"errorCode":"INVALID_ARGUMENT"}]}}`, false},
		{"quota", 429, `{"error":{"code":429,"message":"Quota exceeded","status":"RESOURCE_EXHAUSTED","details":[{"errorCode":"QUOTA_EXCEEDED"}]}}`, false},
		{"server error", 503, `not json`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, fg := newTestSender(t)
			fg.sendStatus, fg.sendBody = tc.status, tc.body
			err := s.Send(context.Background(), "t", Message{Title: "x"})
			if err == nil {
				t.Fatal("want error")
			}
			if got := errors.Is(err, ErrInvalidToken); got != tc.wantInvalid {
				t.Errorf("errors.Is(err, ErrInvalidToken) = %v, want %v (err: %v)", got, tc.wantInvalid, err)
			}
		})
	}
}

func TestNewFCMSenderValidation(t *testing.T) {
	if _, err := NewFCMSender([]byte(`{`), ""); err == nil {
		t.Error("want error for malformed JSON")
	}
	if _, err := NewFCMSender([]byte(`{"type":"authorized_user"}`), ""); err == nil {
		t.Error("want error for non service_account credentials")
	}
	if _, err := NewFCMSender([]byte(`{"type":"service_account","client_email":"a","private_key":"not pem"}`), "p"); err == nil ||
		!strings.Contains(err.Error(), "private key") {
		t.Errorf("want private key parse error, got %v", err)
	}
	if _, err := NewFCMSenderFromFile(filepath.Join(t.TempDir(), "missing.json"), ""); err == nil {
		t.Error("want error for missing file")
	}
}

func TestNopSenderNeverFails(t *testing.T) {
	if err := (NopSender{}).Send(context.Background(), "abcdefghijk", Message{Title: "x"}); err != nil {
		t.Fatal(err)
	}
}
