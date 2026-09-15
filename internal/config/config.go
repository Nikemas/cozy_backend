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

		BakaiWebhookToken: os.Getenv("BAKAI_WEBHOOK_TOKEN"),

		NikitaAPIKey: os.Getenv("NIKITA_API_KEY"),
		SMSMockOTP:   getEnv("SMS_MOCK_OTP", "false") == "true",
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
