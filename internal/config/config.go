// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Env         string // "dev" | "prod"
	HTTPAddr    string
	DatabaseURL string

	JWTSecret       string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration

	MinIOEndpoint  string
	MinIOAccessKey string
	MinIOSecretKey string
	MinIOBucket    string
	MinIOUseSSL    bool

	// MinIOPublicEndpoint/MinIOPublicUseSSL are the host the *browser* uses
	// to PUT/GET a presigned URL, which can differ from MinIOEndpoint (the
	// host the backend itself uses to reach MinIO, e.g. a Docker-internal
	// hostname like "minio:9000" that only resolves inside the compose
	// network). Both default to the internal values, which is correct for
	// local dev where backend and browser share "localhost".
	MinIOPublicEndpoint string
	MinIOPublicUseSSL   bool

	BakaiWebhookToken string

	NikitaAPIKey string

	// SMSMockOTP switches the OTP provider to a local mock (any phone,
	// code always "0000", no network call, no real SMS) — for local dev
	// only. Off by default and refused outright when Env == "prod" (see
	// cmd/server/main.go), because APP_ENV itself already defaults to
	// "dev" when unset — this flag being opt-in is the only thing
	// standing between "someone forgot to set APP_ENV in prod" and every
	// login on the live site accepting code 0000.
	SMSMockOTP bool

	// DB holds database/sql pool limits; HTTP holds net/http.Server
	// timeouts. Both come with production-sane defaults (see Load) and can
	// be tuned per environment without a rebuild.
	DB   DBPool
	HTTP HTTPTimeouts

	// LogFormat is "text" (default, human-readable) or "json" (one object
	// per line, for log shippers). LOG_FORMAT.
	LogFormat string
}

// DBPool mirrors the *sql.DB pool knobs (SetMaxOpenConns & co.). Postgres'
// default max_connections is 100, so MaxOpenConns stays well under it to
// leave room for migrations, pg_dump and psql sessions.
type DBPool struct {
	MaxOpenConns    int           // DB_MAX_OPEN_CONNS, default 25
	MaxIdleConns    int           // DB_MAX_IDLE_CONNS, default 10
	ConnMaxLifetime time.Duration // DB_CONN_MAX_LIFETIME, default 30m
	ConnMaxIdleTime time.Duration // DB_CONN_MAX_IDLE_TIME, default 5m
}

// HTTPTimeouts mirrors net/http.Server's timeouts. ReadTimeout covers the
// whole request body, so it must stay generous enough for the admin's
// .xlsx product import upload; WriteTimeout likewise for report pages.
type HTTPTimeouts struct {
	ReadHeaderTimeout time.Duration // HTTP_READ_HEADER_TIMEOUT, default 5s
	ReadTimeout       time.Duration // HTTP_READ_TIMEOUT, default 60s
	WriteTimeout      time.Duration // HTTP_WRITE_TIMEOUT, default 60s
	IdleTimeout       time.Duration // HTTP_IDLE_TIMEOUT, default 120s
	ShutdownTimeout   time.Duration // HTTP_SHUTDOWN_TIMEOUT, default 10s
}

func Load() (*Config, error) {
	cfg := &Config{
		Env:         getEnv("APP_ENV", "dev"),
		HTTPAddr:    getEnv("HTTP_ADDR", ":8080"),
		DatabaseURL: os.Getenv("DATABASE_URL"),

		JWTSecret:       os.Getenv("JWT_SECRET"),
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 30 * 24 * time.Hour,

		MinIOEndpoint:  getEnv("MINIO_ENDPOINT", "localhost:9000"),
		MinIOAccessKey: os.Getenv("MINIO_ACCESS_KEY"),
		MinIOSecretKey: os.Getenv("MINIO_SECRET_KEY"),
		MinIOBucket:    getEnv("MINIO_BUCKET", "cozy-media"),
		MinIOUseSSL:    getEnv("MINIO_USE_SSL", "false") == "true",

		MinIOPublicEndpoint: getEnv("MINIO_PUBLIC_ENDPOINT", getEnv("MINIO_ENDPOINT", "localhost:9000")),
		MinIOPublicUseSSL:   getEnv("MINIO_PUBLIC_USE_SSL", getEnv("MINIO_USE_SSL", "false")) == "true",

		BakaiWebhookToken: os.Getenv("BAKAI_WEBHOOK_TOKEN"),

		NikitaAPIKey: os.Getenv("NIKITA_API_KEY"),
		SMSMockOTP:   getEnv("SMS_MOCK_OTP", "false") == "true",

		LogFormat: getEnv("LOG_FORMAT", "text"),
	}

	var err error
	if cfg.DB, err = loadDBPool(); err != nil {
		return nil, err
	}
	if cfg.HTTP, err = loadHTTPTimeouts(); err != nil {
		return nil, err
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.JWTSecret == "" && cfg.Env != "dev" {
		return nil, fmt.Errorf("JWT_SECRET is required outside dev")
	}

	return cfg, nil
}

func loadDBPool() (DBPool, error) {
	var p DBPool
	var err error
	if p.MaxOpenConns, err = getEnvInt("DB_MAX_OPEN_CONNS", 25); err != nil {
		return p, err
	}
	if p.MaxIdleConns, err = getEnvInt("DB_MAX_IDLE_CONNS", 10); err != nil {
		return p, err
	}
	if p.ConnMaxLifetime, err = getEnvDuration("DB_CONN_MAX_LIFETIME", 30*time.Minute); err != nil {
		return p, err
	}
	if p.ConnMaxIdleTime, err = getEnvDuration("DB_CONN_MAX_IDLE_TIME", 5*time.Minute); err != nil {
		return p, err
	}
	return p, nil
}

func loadHTTPTimeouts() (HTTPTimeouts, error) {
	var t HTTPTimeouts
	var err error
	if t.ReadHeaderTimeout, err = getEnvDuration("HTTP_READ_HEADER_TIMEOUT", 5*time.Second); err != nil {
		return t, err
	}
	if t.ReadTimeout, err = getEnvDuration("HTTP_READ_TIMEOUT", 60*time.Second); err != nil {
		return t, err
	}
	if t.WriteTimeout, err = getEnvDuration("HTTP_WRITE_TIMEOUT", 60*time.Second); err != nil {
		return t, err
	}
	if t.IdleTimeout, err = getEnvDuration("HTTP_IDLE_TIMEOUT", 120*time.Second); err != nil {
		return t, err
	}
	if t.ShutdownTimeout, err = getEnvDuration("HTTP_SHUTDOWN_TIMEOUT", 10*time.Second); err != nil {
		return t, err
	}
	return t, nil
}

// getEnvInt parses key as a non-negative int, returning fallback when unset.
func getEnvInt(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer, got %q", key, v)
	}
	return n, nil
}

// getEnvDuration parses key as a time.Duration ("30s", "5m"), returning
// fallback when unset.
func getEnvDuration(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s must be a non-negative duration like 30s or 5m, got %q", key, v)
	}
	return d, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// PublicObjectURL builds the direct (non-presigned) URL a BROWSER or the
// mobile app can fetch objectKey from in the media bucket. It must use the
// public MinIO host (MINIO_PUBLIC_ENDPOINT — in prod a Caddy-fronted HTTPS
// domain), never MinIOEndpoint, which in Docker is the compose-internal
// "minio:9000" that nothing outside the backend container can resolve.
// Falls back to MinIOEndpoint when no public endpoint is configured (local
// dev, tests). Empty objectKey (no photo) returns "".
//
// The bucket is made anonymous-read by media.Client.EnsureBucket, which is
// what lets a plain GET on this URL succeed without a signature.
func (c *Config) PublicObjectURL(objectKey string) string {
	if objectKey == "" {
		return ""
	}
	endpoint, useSSL := c.MinIOPublicEndpoint, c.MinIOPublicUseSSL
	if endpoint == "" {
		endpoint, useSSL = c.MinIOEndpoint, c.MinIOUseSSL
	}
	scheme := "http"
	if useSSL {
		scheme = "https"
	}
	return scheme + "://" + endpoint + "/" + c.MinIOBucket + "/" + objectKey
}
