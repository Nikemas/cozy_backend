package config

import "testing"

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

func TestLoadPaymentsProvider(t *testing.T) {
	cases := []struct {
		env, provider, want string
		wantErr             bool
	}{
		{env: "dev", want: PaymentsProviderMock},
		{env: "prod", want: PaymentsProviderBakai},
		{env: "prod", provider: "mock", want: PaymentsProviderMock},
		{env: "dev", provider: " Bakai ", want: PaymentsProviderBakai},
		{env: "dev", provider: "stripe", wantErr: true},
	}
	for _, c := range cases {
		t.Setenv("DATABASE_URL", "postgres://x")
		t.Setenv("JWT_SECRET", "s")
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
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("APP_ENV", "dev")
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
