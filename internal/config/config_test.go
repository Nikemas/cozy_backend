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

func TestLoad_PoolAndTimeoutDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
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
	t.Setenv("DATABASE_URL", "postgres://x")
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
	t.Setenv("DATABASE_URL", "postgres://x")
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
