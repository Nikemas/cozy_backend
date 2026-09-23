package push

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	fcmScope        = "https://www.googleapis.com/auth/firebase.messaging"
	defaultTokenURI = "https://oauth2.googleapis.com/token"
	defaultFCMBase  = "https://fcm.googleapis.com"
	// tokenRefreshSkew refreshes the cached OAuth2 access token this long
	// before Google says it expires, so an in-flight send never races
	// expiry.
	tokenRefreshSkew = time.Minute
)

// serviceAccount is the subset of a Google service-account JSON key file
// (Firebase console → Project settings → Service accounts → Generate new
// private key) that the OAuth2 JWT-bearer flow needs.
type serviceAccount struct {
	Type        string `json:"type"`
	ProjectID   string `json:"project_id"`
	PrivateKey  string `json:"private_key"`
	ClientEmail string `json:"client_email"`
	TokenURI    string `json:"token_uri"`
}

// FCMSender sends through FCM HTTP v1
// (POST /v1/projects/{project}/messages:send), authenticating with an
// OAuth2 access token minted from a service account via the JWT-bearer
// grant (RFC 7523). This is the same flow golang.org/x/oauth2/google
// implements; it's done by hand on top of golang-jwt (already a
// dependency) to avoid pulling in the oauth2/Google auth module tree.
type FCMSender struct {
	httpClient  *http.Client
	projectID   string
	clientEmail string
	tokenURI    string
	baseURL     string
	key         *rsa.PrivateKey
	now         func() time.Time

	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time
}

// NewFCMSenderFromFile loads a service-account JSON key file. projectID
// overrides the key file's project_id when non-empty (FCM_PROJECT_ID).
func NewFCMSenderFromFile(path, projectID string) (*FCMSender, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("push: reading FCM credentials: %w", err)
	}
	return NewFCMSender(raw, projectID)
}

// NewFCMSender parses service-account JSON credentials.
func NewFCMSender(credentialsJSON []byte, projectID string) (*FCMSender, error) {
	var sa serviceAccount
	if err := json.Unmarshal(credentialsJSON, &sa); err != nil {
		return nil, fmt.Errorf("push: parsing FCM credentials: %w", err)
	}
	if sa.Type != "" && sa.Type != "service_account" {
		return nil, fmt.Errorf("push: FCM credentials must be a service_account key, got %q", sa.Type)
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return nil, errors.New("push: FCM credentials missing client_email or private_key")
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(sa.PrivateKey))
	if err != nil {
		return nil, fmt.Errorf("push: parsing FCM private key: %w", err)
	}
	if projectID == "" {
		projectID = sa.ProjectID
	}
	if projectID == "" {
		return nil, errors.New("push: FCM project id unknown (set FCM_PROJECT_ID or use a key file with project_id)")
	}
	tokenURI := sa.TokenURI
	if tokenURI == "" {
		tokenURI = defaultTokenURI
	}
	return &FCMSender{
		httpClient:  &http.Client{Timeout: 10 * time.Second},
		projectID:   projectID,
		clientEmail: sa.ClientEmail,
		tokenURI:    tokenURI,
		baseURL:     defaultFCMBase,
		key:         key,
		now:         time.Now,
	}, nil
}

// ProjectID reports which Firebase project messages are sent to.
func (s *FCMSender) ProjectID() string { return s.projectID }

type fcmRequest struct {
	Message fcmMessage `json:"message"`
}

type fcmMessage struct {
	Token        string            `json:"token"`
	Notification *fcmNotification  `json:"notification,omitempty"`
	Data         map[string]string `json:"data,omitempty"`
	Android      *fcmAndroid       `json:"android,omitempty"`
	APNS         *fcmAPNS          `json:"apns,omitempty"`
}

type fcmNotification struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
}

type fcmAndroid struct {
	Priority     string                  `json:"priority,omitempty"`
	Notification *fcmAndroidNotification `json:"notification,omitempty"`
}

type fcmAndroidNotification struct {
	ChannelID string `json:"channel_id,omitempty"`
}

type fcmAPNS struct {
	Payload map[string]any `json:"payload,omitempty"`
}

// fcmErrorResponse is Google's standard error envelope; FCM puts its own
// error code (UNREGISTERED, INVALID_ARGUMENT, ...) in details[].errorCode.
type fcmErrorResponse struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
		Details []struct {
			Type      string `json:"@type"`
			ErrorCode string `json:"errorCode"`
		} `json:"details"`
	} `json:"error"`
}

// Send delivers msg to token. Returns an error wrapping ErrInvalidToken if
// FCM says the token is dead; any other failure is a plain error.
func (s *FCMSender) Send(ctx context.Context, token string, msg Message) error {
	accessToken, err := s.getAccessToken(ctx)
	if err != nil {
		return err
	}

	android := &fcmAndroid{Priority: "high"}
	if msg.AndroidChannelID != "" {
		android.Notification = &fcmAndroidNotification{ChannelID: msg.AndroidChannelID}
	}
	body, err := json.Marshal(fcmRequest{Message: fcmMessage{
		Token:        token,
		Notification: &fcmNotification{Title: msg.Title, Body: msg.Body},
		Data:         msg.Data,
		Android:      android,
		APNS:         &fcmAPNS{Payload: map[string]any{"aps": map[string]any{"sound": "default"}}},
	}})
	if err != nil {
		return err
	}

	endpoint := s.baseURL + "/v1/projects/" + url.PathEscape(s.projectID) + "/messages:send"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("push: FCM request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode == http.StatusUnauthorized {
		// Access token revoked/expired early — drop the cache so the next
		// send mints a fresh one.
		s.mu.Lock()
		s.accessToken = ""
		s.mu.Unlock()
	}

	var fe fcmErrorResponse
	_ = json.Unmarshal(respBody, &fe)
	if isInvalidTokenError(resp.StatusCode, fe) {
		return fmt.Errorf("%w (FCM %d %s: %s)", ErrInvalidToken, resp.StatusCode, fe.Error.Status, fe.Error.Message)
	}
	return fmt.Errorf("push: FCM send failed: HTTP %d %s: %s", resp.StatusCode, fe.Error.Status, fe.Error.Message)
}

// isInvalidTokenError decides whether an FCM error means "delete this
// token". UNREGISTERED (404) and SENDER_ID_MISMATCH (403) are always
// token-specific. INVALID_ARGUMENT (400) can also mean a malformed
// payload — a bug on our side that must NOT wipe every customer's tokens —
// so it only counts when FCM's message blames the registration token.
func isInvalidTokenError(status int, fe fcmErrorResponse) bool {
	codes := map[string]bool{}
	for _, d := range fe.Error.Details {
		if d.ErrorCode != "" {
			codes[d.ErrorCode] = true
		}
	}
	if codes["UNREGISTERED"] || codes["SENDER_ID_MISMATCH"] {
		return true
	}
	if status == http.StatusNotFound && fe.Error.Status == "NOT_FOUND" {
		return true
	}
	if codes["INVALID_ARGUMENT"] || fe.Error.Status == "INVALID_ARGUMENT" {
		return strings.Contains(strings.ToLower(fe.Error.Message), "registration token")
	}
	return false
}

// getAccessToken returns a cached OAuth2 access token, minting a new one
// via the JWT-bearer grant when missing or about to expire.
func (s *FCMSender) getAccessToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if s.accessToken != "" && now.Add(tokenRefreshSkew).Before(s.expiresAt) {
		return s.accessToken, nil
	}

	claims := jwt.MapClaims{
		"iss":   s.clientEmail,
		"scope": fcmScope,
		"aud":   s.tokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	assertion, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(s.key)
	if err != nil {
		return "", fmt.Errorf("push: signing OAuth2 assertion: %w", err)
	}

	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("push: OAuth2 token request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&tr); err != nil {
		return "", fmt.Errorf("push: OAuth2 token response (HTTP %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK || tr.AccessToken == "" {
		return "", fmt.Errorf("push: OAuth2 token request rejected (HTTP %d): %s %s", resp.StatusCode, tr.Error, tr.ErrorDesc)
	}
	if tr.ExpiresIn <= 0 {
		tr.ExpiresIn = 3600
	}
	s.accessToken = tr.AccessToken
	s.expiresAt = now.Add(time.Duration(tr.ExpiresIn) * time.Second)
	return s.accessToken, nil
}
