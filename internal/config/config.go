// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"os"
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

	// FCMCredentialsFile is the path to a Firebase service-account JSON
	// key; empty disables push (notifications are logged instead).
	// FCMProjectID overrides the key file's project_id when set.
	FCMCredentialsFile string
	FCMProjectID       string

	// TelegramBotToken/TelegramChatID route new-order alerts to the staff
	// Telegram chat; either empty disables them (logged instead).
	TelegramBotToken string
	TelegramChatID   string

	// PublicBaseURL is the site's external origin (e.g.
	// "https://cozy.erpsystemsales.com"), used for links in staff
	// notifications. Empty omits the links.
	PublicBaseURL string

	// Mobile app version gate served by GET /api/v1/app/config.
	AppMinVersion      string
	AppLatestVersion   string
	AppStoreURLIOS     string
	AppStoreURLAndroid string
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

		FCMCredentialsFile: os.Getenv("FCM_CREDENTIALS_FILE"),
		FCMProjectID:       os.Getenv("FCM_PROJECT_ID"),
		TelegramBotToken:   os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:     os.Getenv("TELEGRAM_CHAT_ID"),
		PublicBaseURL:      os.Getenv("PUBLIC_BASE_URL"),

		AppMinVersion:      getEnv("APP_MIN_VERSION", "1.0.0"),
		AppLatestVersion:   getEnv("APP_LATEST_VERSION", "1.0.0"),
		AppStoreURLIOS:     os.Getenv("APP_STORE_URL_IOS"),
		AppStoreURLAndroid: os.Getenv("APP_STORE_URL_ANDROID"),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.JWTSecret == "" && cfg.Env != "dev" {
		return nil, fmt.Errorf("JWT_SECRET is required outside dev")
	}

	return cfg, nil
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
