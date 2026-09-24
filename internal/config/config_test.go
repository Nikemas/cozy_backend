package config

import (
	"strings"
	"testing"
)

func TestPublicObjectURL_UsesPublicEndpoint(t *testing.T) {
	cfg := &Config{
		MinIOEndpoint: "minio:9000", MinIOUseSSL: false,
		MinIOPublicEndpoint: "media.cozy.example.com", MinIOPublicUseSSL: true,
		MinIOBucket: "cozy-media",
	}
	got := cfg.PublicObjectURL("products/a.jpg")
	want := "https://media.cozy.example.com/cozy-media/products/a.jpg"
	if got != want {
		t.Fatalf("PublicObjectURL = %q, want %q", got, want)
	}
}

func TestPublicObjectURL_FallsBackToInternalEndpoint(t *testing.T) {
	cfg := &Config{MinIOEndpoint: "localhost:9000", MinIOBucket: "cozy-media"}
	if got, want := cfg.PublicObjectURL("k.jpg"), "http://localhost:9000/cozy-media/k.jpg"; got != want {
		t.Fatalf("PublicObjectURL = %q, want %q", got, want)
	}
	if got := cfg.PublicObjectURL(""); got != "" {
		t.Fatalf("empty key should give empty URL, got %q", got)
	}
}

// testSecret is a JWT_SECRET that passes validateJWTSecret.
const testSecret = "0123456789abcdef0123456789abcdef"

// setDevEnv sets the minimum environment Load accepts for APP_ENV=dev.
func setDevEnv(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "dev")
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("JWT_SECRET", testSecret)
}

func TestLoad_PoolAndTimeoutDefaults(t *testing.T) {
	setDevEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DB.MaxOpenConns != 25 || cfg.DB.MaxIdleConns != 10 {
		t.Errorf("DB pool = %+v, want 25 open / 10 idle", cfg.DB)
	}
	if cfg.HTTP.ReadHeaderTimeout.Seconds() != 5 || cfg.HTTP.IdleTimeout.Seconds() != 120 {
		t.Errorf("HTTP timeouts = %+v, want 5s read-header / 120s idle", cfg.HTTP)
	}
}

func TestLoad_PoolAndTimeoutOverrides(t *testing.T) {
	setDevEnv(t)
	t.Setenv("DB_MAX_OPEN_CONNS", "7")
	t.Setenv("HTTP_WRITE_TIMEOUT", "90s")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DB.MaxOpenConns != 7 {
		t.Errorf("MaxOpenConns = %d, want 7", cfg.DB.MaxOpenConns)
	}
	if cfg.HTTP.WriteTimeout.Seconds() != 90 {
		t.Errorf("WriteTimeout = %v, want 90s", cfg.HTTP.WriteTimeout)
	}
}

func TestLoad_RejectsBadPoolAndTimeoutValues(t *testing.T) {
	setDevEnv(t)
	t.Setenv("DB_MAX_OPEN_CONNS", "lots")
	if _, err := Load(); err == nil {
		t.Fatal("Load: want error for non-numeric DB_MAX_OPEN_CONNS")
	}
	t.Setenv("DB_MAX_OPEN_CONNS", "")
	t.Setenv("HTTP_READ_TIMEOUT", "soon")
	if _, err := Load(); err == nil {
		t.Fatal("Load: want error for unparseable HTTP_READ_TIMEOUT")
	}
}

func TestLoadPaymentsProvider(t *testing.T) {
	cases := []struct {
		env, provider, want string
		wantErr             bool
	}{
		{env: "dev", want: PaymentsProviderMock},
		{env: "prod", want: PaymentsProviderBakai},
		{env: "prod", provider: "mock", wantErr: true},
		{env: "staging", provider: "mock", want: PaymentsProviderMock},
		{env: "staging", want: PaymentsProviderMock},
		{env: "dev", provider: " Bakai ", want: PaymentsProviderBakai},
		{env: "dev", provider: "stripe", wantErr: true},
	}
	for _, c := range cases {
		setProdEnv(t)
		t.Setenv("APP_ENV", c.env)
		t.Setenv("PAYMENTS_PROVIDER", c.provider)
		cfg, err := Load()
		if c.wantErr {
			if err == nil {
				t.Errorf("%+v: want error", c)
			}
			continue
		}
		if err != nil || cfg.PaymentsProvider != c.want {
			t.Errorf("%+v: got %+v, %v", c, cfg, err)
		}
	}
}

func TestLoadPublicBaseURLTrimsSlash(t *testing.T) {
	setDevEnv(t)
	t.Setenv("PUBLIC_BASE_URL", "https://cozy.kg/")
	cfg, err := Load()
	if err != nil || cfg.PublicBaseURL != "https://cozy.kg" {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
	if got := cfg.PaymentsBaseURL(); got != "https://cozy.kg" {
		t.Errorf("PaymentsBaseURL = %q", got)
	}
}

func TestPaymentsBaseURLFallsBackToLocalhost(t *testing.T) {
	cfg := &Config{HTTPAddr: "127.0.0.1:9090"}
	if got := cfg.PaymentsBaseURL(); got != "http://localhost:9090" {
		t.Errorf("PaymentsBaseURL = %q", got)
	}
}

// setProdEnv sets every variable APP_ENV=prod requires.
func setProdEnv(t *testing.T) {
	t.Helper()
	setDevEnv(t)
	t.Setenv("APP_ENV", "prod")
	t.Setenv("PUBLIC_BASE_URL", "https://cozy.example.com")
	t.Setenv("MINIO_ACCESS_KEY", "ak")
	t.Setenv("MINIO_SECRET_KEY", "sk")
	t.Setenv("NIKITA_API_KEY", "nikita-key")
	t.Setenv("BAKAI_WEBHOOK_TOKEN", "a-real-webhook-token")
	t.Setenv("SMS_MOCK_OTP", "")
	t.Setenv("PAYMENTS_PROVIDER", "")
}

func TestLoad_AppEnvRequiredAndValidated(t *testing.T) {
	setDevEnv(t)
	t.Setenv("APP_ENV", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "APP_ENV") {
		t.Fatalf("empty APP_ENV: err = %v, want APP_ENV error", err)
	}
	t.Setenv("APP_ENV", "production")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "APP_ENV") {
		t.Fatalf("APP_ENV=production: err = %v, want APP_ENV error", err)
	}
	t.Setenv("APP_ENV", " Staging ")
	setProdEnvKeepAppEnv(t)
	cfg, err := Load()
	if err != nil || cfg.Env != EnvStaging {
		t.Fatalf("APP_ENV=' Staging ': cfg=%v err=%v", cfg, err)
	}
}

// setProdEnvKeepAppEnv sets the prod-like requirements without touching
// APP_ENV.
func setProdEnvKeepAppEnv(t *testing.T) {
	t.Helper()
	t.Setenv("PUBLIC_BASE_URL", "https://cozy.example.com")
	t.Setenv("MINIO_ACCESS_KEY", "ak")
	t.Setenv("MINIO_SECRET_KEY", "sk")
	t.Setenv("NIKITA_API_KEY", "nikita-key")
	t.Setenv("BAKAI_WEBHOOK_TOKEN", "a-real-webhook-token")
}

func TestLoad_JWTSecretRules(t *testing.T) {
	for _, env := range []string{"dev", "staging", "prod"} {
		for _, secret := range []string{"", "change-me-in-prod", "CHANGE-ME-please-0123456789abcdef0123", "short-secret"} {
			setProdEnv(t)
			t.Setenv("APP_ENV", env)
			t.Setenv("JWT_SECRET", secret)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "JWT_SECRET") {
				t.Errorf("env=%s secret=%q: err = %v, want JWT_SECRET error", env, secret, err)
			}
		}
	}
}

func TestLoad_ProdRequiresRealSettings(t *testing.T) {
	for _, key := range []string{"PUBLIC_BASE_URL", "MINIO_ACCESS_KEY", "MINIO_SECRET_KEY", "NIKITA_API_KEY", "BAKAI_WEBHOOK_TOKEN"} {
		for _, env := range []string{"staging", "prod"} {
			setProdEnv(t)
			t.Setenv("APP_ENV", env)
			if env == "staging" {
				t.Setenv("PAYMENTS_PROVIDER", "bakai")
			}
			t.Setenv(key, "")
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), key) {
				t.Errorf("env=%s without %s: err = %v", env, key, err)
			}
		}
	}

	setProdEnv(t)
	t.Setenv("PUBLIC_BASE_URL", "http://cozy.example.com")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "https") {
		t.Errorf("http PUBLIC_BASE_URL in prod: err = %v", err)
	}

	setProdEnv(t)
	t.Setenv("NIKITA_API_KEY", "change-me")
	if _, err := Load(); err == nil {
		t.Error("placeholder NIKITA_API_KEY in prod: want error")
	}

	setProdEnv(t)
	if _, err := Load(); err != nil {
		t.Errorf("complete prod env: %v", err)
	}
}

func TestLoad_MocksInProdAndStaging(t *testing.T) {
	setProdEnv(t)
	t.Setenv("SMS_MOCK_OTP", "true")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "SMS_MOCK_OTP") {
		t.Errorf("SMS mock in prod: err = %v", err)
	}

	// Staging may demo with mock SMS + mock payments, and then needs no
	// Nikita key or webhook token.
	setProdEnv(t)
	t.Setenv("APP_ENV", "staging")
	t.Setenv("SMS_MOCK_OTP", "true")
	t.Setenv("PAYMENTS_PROVIDER", "mock")
	t.Setenv("NIKITA_API_KEY", "")
	t.Setenv("BAKAI_WEBHOOK_TOKEN", "")
	if _, err := Load(); err != nil {
		t.Errorf("staging with mocks: %v", err)
	}
}

func TestLoad_SecurityDefaults(t *testing.T) {
	setDevEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Security.CookieSecure {
		t.Error("dev: CookieSecure should default to false")
	}
	if cfg.Security.MaxBodyBytes != 1<<20 || cfg.Security.MaxUploadBytes != 25<<20 {
		t.Errorf("body limits = %d/%d", cfg.Security.MaxBodyBytes, cfg.Security.MaxUploadBytes)
	}
	a := cfg.Security.Auth
	if a.OTPPerIPPerHour != 30 || a.OTPPerDay != 1000 || a.OTPVerifyMaxAttempts != 5 ||
		a.OTPVerifyFailsPerIPPerHour != 30 || a.RefreshPerIPPerMinute != 120 || a.StaffLoginPerIP != 20 {
		t.Errorf("auth limits = %+v", a)
	}
	if len(cfg.Security.TrustedProxies) == 0 {
		t.Error("default trusted proxies should not be empty")
	}

	setProdEnv(t)
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Security.CookieSecure {
		t.Error("prod: CookieSecure should default to true")
	}
}

func TestLoad_SecurityOverrides(t *testing.T) {
	setDevEnv(t)
	t.Setenv("COOKIE_SECURE", "true")
	t.Setenv("TRUSTED_PROXY_CIDRS", "10.1.0.0/16, 192.0.2.7")
	t.Setenv("OTP_MAX_PER_DAY", "0")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Security.CookieSecure || cfg.Security.Auth.OTPPerDay != 0 {
		t.Errorf("security = %+v", cfg.Security)
	}
	if got := len(cfg.Security.TrustedProxies); got != 2 || cfg.Security.TrustedProxies[1].Bits() != 32 {
		t.Errorf("TrustedProxies = %v", cfg.Security.TrustedProxies)
	}

	t.Setenv("TRUSTED_PROXY_CIDRS", "")
	if cfg, err = Load(); err != nil || len(cfg.Security.TrustedProxies) != 0 {
		t.Errorf("empty TRUSTED_PROXY_CIDRS: %v %v", cfg.Security.TrustedProxies, err)
	}

	for key, val := range map[string]string{
		"COOKIE_SECURE":           "yes please",
		"TRUSTED_PROXY_CIDRS":     "10.0.0.0/99",
		"OTP_VERIFY_MAX_ATTEMPTS": "0",
		"MAX_BODY_BYTES":          "0",
	} {
		setDevEnv(t)
		t.Setenv(key, val)
		if _, err := Load(); err == nil {
			t.Errorf("%s=%q: want error", key, val)
		}
		t.Setenv(key, "")
	}
}
