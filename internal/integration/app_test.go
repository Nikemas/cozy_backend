//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/admin"
	"github.com/Nikemas/cozy_backend/internal/auth"
	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/httpapi"
	"github.com/Nikemas/cozy_backend/internal/importguard"
	"github.com/Nikemas/cozy_backend/internal/notify"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/payments"
	"github.com/Nikemas/cozy_backend/internal/points"
	"github.com/Nikemas/cozy_backend/internal/reports"
	"github.com/Nikemas/cozy_backend/internal/staff"
	"github.com/Nikemas/cozy_backend/internal/web"
)

// repoRootFromHere is the repository root relative to this package's
// directory: the web/admin renderers and the storefront i18n bundle load
// templates/locales by repo-root-relative paths at registration time.
const repoRootFromHere = "../.."

// testJWTSecret signs storefront session cookies and API access tokens.
const testJWTSecret = "integration-secret-integration-secret-0123"

// testApp is the whole HTTP surface (JSON API, admin API + panel,
// storefront) mounted on one mux over testDB, wired the same way
// cmd/server does it — minus MinIO (photo uploads) and the CSRF/body-limit
// middleware, which have their own unit tests.
type testApp struct {
	handler http.Handler
	cfg     *config.Config
	auth    *auth.Service
	staff   *staff.Service
	reports *reports.CachedRepo
	// importGuard is the one guard both import surfaces share.
	importGuard *importguard.Guard
}

var (
	appOnce sync.Once
	appInst *testApp
	appErr  error
)

// app builds the shared testApp on first use. Building chdirs to the repo
// root while the templates are parsed; that happens once, before any test
// that needs the app runs its requests.
func app(t *testing.T) *testApp {
	t.Helper()
	appOnce.Do(func() { appInst, appErr = buildApp() })
	if appErr != nil {
		t.Fatalf("building app: %v", appErr)
	}
	return appInst
}

func buildApp() (*testApp, error) {
	cfg := &config.Config{
		Env:                 config.EnvDev,
		JWTSecret:           testJWTSecret,
		PublicBaseURL:       "https://cozy.test",
		MinIOPublicEndpoint: "media.cozy.test", MinIOPublicUseSSL: true, MinIOBucket: "cozy",
		PaymentsProvider: config.PaymentsProviderMock,
		Security: config.Security{Auth: config.AuthLimits{
			OTPPerIPPerHour: 10_000, OTPPerDay: 100_000, OTPVerifyMaxAttempts: 5,
			OTPVerifyFailsPerIPPerHour: 10_000, RefreshPerIPPerMinute: 10_000,
		}},
	}
	authSvc := auth.NewService(testDB, notify.NewMockClient(), []byte(cfg.JWTSecret), cfg.Security.Auth, config.ReviewLogin{}, nil)
	payProvider, err := payments.NewProvider(cfg)
	if err != nil {
		return nil, err
	}
	staffSvc := staff.NewService(testDB)

	orig, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	if err := os.Chdir(repoRootFromHere); err != nil {
		return nil, err
	}
	defer func() { _ = os.Chdir(orig) }()

	mux := http.NewServeMux()
	ordersSvc := orders.NewService(testDB)
	paySvc := payments.NewService(testDB, payProvider, ordersSvc, cfg.PaymentsBaseURL())
	httpapi.RegisterAuthRoutes(mux, authSvc)
	httpapi.RegisterCatalogRoutes(mux, testDB, cfg)
	httpapi.RegisterPublicPointsRoutes(mux, testDB)
	httpapi.RegisterPublicBannerRoutes(mux, testDB, cfg)
	httpapi.RegisterOrderRoutes(mux, testDB, authSvc, cfg, ordersSvc, paySvc)
	httpapi.RegisterPaymentRoutes(mux, paySvc, cfg.PaymentsBaseURL())
	httpapi.RegisterCustomerRoutes(mux, testDB, authSvc, cfg)
	httpapi.RegisterAppConfigRoutes(mux, cfg)

	staff.RegisterRoutes(mux, staffSvc)
	httpapi.RegisterAdminCatalogRoutes(mux, testDB, staffSvc, cfg.PublicObjectURL)
	points.RegisterRoutes(mux, testDB, staffSvc)
	httpapi.RegisterAdminOrdersRoutes(mux, testDB, staffSvc)
	reportsRepo := reports.NewCachedRepo(reports.NewRepo(testDB), reports.CacheConfig{})
	httpapi.RegisterAdminReportsRoutes(mux, reportsRepo, staffSvc)
	// Wired like cmd/server: one guard shared by the import page and the
	// JSON import.
	importGuard := importguard.New()
	httpapi.RegisterAdminImportRoutes(mux, testDB, staffSvc, importGuard)
	if err := admin.RegisterRoutes(mux, testDB, staffSvc, nil, cfg, reportsRepo, importGuard); err != nil {
		return nil, err
	}

	web.SetCookieSecure(false)
	if err := web.RegisterRoutes(mux, testDB, cfg, authSvc, payProvider); err != nil {
		return nil, err
	}
	return &testApp{handler: mux, cfg: cfg, auth: authSvc, staff: staffSvc, reports: reportsRepo, importGuard: importGuard}, nil
}

// req describes one request to the app.
type req struct {
	method  string
	path    string
	form    url.Values // sent as application/x-www-form-urlencoded
	json    any        // sent as application/json
	body    io.Reader  // raw body (with contentType)
	ctype   string
	cookies []*http.Cookie
	htmx    bool
}

// do runs r against the app and returns the recorded response.
func (a *testApp) do(t *testing.T, r req) *httptest.ResponseRecorder {
	t.Helper()
	body := r.body
	ctype := r.ctype
	switch {
	case r.form != nil:
		body, ctype = strings.NewReader(r.form.Encode()), "application/x-www-form-urlencoded"
	case r.json != nil:
		b, err := json.Marshal(r.json)
		if err != nil {
			t.Fatal(err)
		}
		body, ctype = strings.NewReader(string(b)), "application/json"
	}
	hr := httptest.NewRequest(r.method, r.path, body)
	if ctype != "" {
		hr.Header.Set("Content-Type", ctype)
	}
	if r.htmx {
		hr.Header.Set("HX-Request", "true")
	}
	for _, c := range r.cookies {
		hr.AddCookie(c)
	}
	w := httptest.NewRecorder()
	a.handler.ServeHTTP(w, hr)
	return w
}

// get is a shorthand for a GET with optional cookies.
func (a *testApp) get(t *testing.T, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	return a.do(t, req{method: http.MethodGet, path: path, cookies: cookies})
}

// customerSession logs customerID's phone in through the real OTP flow
// (mock SMS, code 0000) and returns the storefront session cookie and the
// API bearer token (the same JWT).
func (a *testApp) customerSession(t *testing.T, customerID string) *http.Cookie {
	t.Helper()
	ctx := ctxT(t)
	var phone string
	if err := testDB.QueryRowContext(ctx, `SELECT phone FROM customers WHERE id = $1`, customerID).Scan(&phone); err != nil {
		t.Fatal(err)
	}
	access := a.accessToken(t, ctx, phone)
	return &http.Cookie{Name: "cozy_session", Value: access}
}

func (a *testApp) accessToken(t *testing.T, ctx context.Context, phone string) string {
	t.Helper()
	if err := a.auth.RequestOTP(ctx, phone); err != nil {
		t.Fatalf("RequestOTP: %v", err)
	}
	access, _, _, err := a.auth.VerifyOTP(ctx, phone, "0000")
	if err != nil {
		t.Fatalf("VerifyOTP: %v", err)
	}
	return access
}

// newStaffSession creates an active staff member with role (pointID for
// point_staff) and returns its staff_session cookie and ID.
func (a *testApp) newStaffSession(t *testing.T, role staff.Role, pointID *string) (*http.Cookie, string) {
	t.Helper()
	ctx := ctxT(t)
	local, _ := uniqueKGPhone()
	const password = "staff-pass-123"
	st, err := a.staff.CreateStaff(ctx, staff.CreateStaffInput{
		Phone: local, Password: password, Name: "IT " + string(role), Role: role, PointID: pointID,
	})
	if err != nil {
		t.Fatalf("CreateStaff(%s): %v", role, err)
	}
	token, err := a.staff.Login(ctx, local, password)
	if err != nil {
		t.Fatalf("staff Login: %v", err)
	}
	return &http.Cookie{Name: staff.SessionCookieName, Value: token}, st.ID
}

// wantStatus fails the test when w's status isn't want, showing the body.
func wantStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		body := w.Body.String()
		if len(body) > 600 {
			body = body[:600] + "…"
		}
		t.Fatalf("status = %d, want %d; body: %s", w.Code, want, body)
	}
}

// wantRedirect asserts a 303 to location.
func wantRedirect(t *testing.T, w *httptest.ResponseRecorder, location string) {
	t.Helper()
	wantStatus(t, w, http.StatusSeeOther)
	if got := w.Header().Get("Location"); got != location {
		t.Fatalf("Location = %q, want %q", got, location)
	}
}

// wantRedirectPath is wantRedirect ignoring the query (a ?toast= success
// message, list filters).
func wantRedirectPath(t *testing.T, w *httptest.ResponseRecorder, path string) {
	t.Helper()
	wantStatus(t, w, http.StatusSeeOther)
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil || loc.Path != path {
		t.Fatalf("Location = %q, want path %q", w.Header().Get("Location"), path)
	}
}

// wantBody asserts every substring appears in w's body.
func wantBody(t *testing.T, w *httptest.ResponseRecorder, subs ...string) {
	t.Helper()
	body := w.Body.String()
	for _, s := range subs {
		if !strings.Contains(body, s) {
			if len(body) > 800 {
				body = body[:800] + "…"
			}
			t.Fatalf("body lacks %q; body: %s", s, body)
		}
	}
}

// decodeJSON unmarshals w's body into v.
func decodeJSON(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("decoding %q: %v", w.Body.String(), err)
	}
}

// cartQty is the quantity of variantID in customerID's cart (0 if none).
func cartQty(t *testing.T, customerID, variantID string) int {
	t.Helper()
	var q int
	err := testDB.QueryRowContext(ctxT(t),
		`SELECT COALESCE((SELECT qty FROM cart_items WHERE customer_id = $1 AND variant_id = $2), 0)`,
		customerID, variantID).Scan(&q)
	if err != nil {
		t.Fatal(err)
	}
	return q
}
