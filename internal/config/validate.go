package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Deployment environments accepted in APP_ENV.
//
//   - dev: local development. Relaxed: mock SMS/payments allowed, cookies
//     without Secure, no required external credentials.
//   - staging: publicly reachable test stand. Same required settings as
//     prod (real JWT secret, MinIO keys, PUBLIC_BASE_URL, ...), but the
//     mock payment page and the mock OTP provider are still allowed (with a
//     loud warning) so the shop can be demoed without a bank contract.
//   - prod: the live shop. Everything required, no mocks.
const (
	EnvDev     = "dev"
	EnvStaging = "staging"
	EnvProd    = "prod"
)

// minJWTSecretLen is the minimum JWT_SECRET length in bytes: HS256 keys
// shorter than the 256-bit hash output are brute-forceable offline from a
// single captured token.
const minJWTSecretLen = 32

// IsProdLike reports whether the environment is publicly reachable
// (staging or prod) and so must run with production-grade settings.
func (c *Config) IsProdLike() bool {
	return c.Env == EnvStaging || c.Env == EnvProd
}

func validateEnv(env string) error {
	switch env {
	case EnvDev, EnvStaging, EnvProd:
		return nil
	case "":
		return errors.New("APP_ENV is required: set it to dev, staging or prod")
	default:
		return fmt.Errorf("APP_ENV must be dev, staging or prod, got %q", env)
	}
}

// validate enforces the cross-field startup rules. It runs after every
// field is loaded, so each error can name the exact variable to fix.
func (c *Config) validate() error {
	if c.DatabaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	if err := validateJWTSecret(c.JWTSecret); err != nil {
		return err
	}

	if c.Env == EnvProd {
		if c.SMSMockOTP {
			return errors.New("SMS_MOCK_OTP=true is not allowed with APP_ENV=prod — every login would accept code 0000")
		}
		if c.PaymentsProvider == PaymentsProviderMock {
			return errors.New("PAYMENTS_PROVIDER=mock is not allowed with APP_ENV=prod — anyone could mark online orders paid on the mock checkout page (use APP_ENV=staging for a test stand)")
		}
	}

	if !c.IsProdLike() {
		return nil
	}

	var missing []string
	if c.PublicBaseURL == "" {
		missing = append(missing, "PUBLIC_BASE_URL")
	}
	if c.MinIOAccessKey == "" {
		missing = append(missing, "MINIO_ACCESS_KEY")
	}
	if c.MinIOSecretKey == "" {
		missing = append(missing, "MINIO_SECRET_KEY")
	}
	if !c.SMSMockOTP && isPlaceholder(c.NikitaAPIKey) {
		missing = append(missing, "NIKITA_API_KEY")
	}
	if c.PaymentsProvider == PaymentsProviderBakai {
		if isPlaceholder(c.BakaiWebhookToken) {
			missing = append(missing, "BAKAI_WEBHOOK_TOKEN")
		}
		if isPlaceholder(c.BakaiAPIToken) {
			missing = append(missing, "BAKAI_API_TOKEN")
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("APP_ENV=%s requires real values for: %s", c.Env, strings.Join(missing, ", "))
	}

	u, err := url.Parse(c.PublicBaseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("PUBLIC_BASE_URL must be an https:// origin with APP_ENV=%s, got %q", c.Env, c.PublicBaseURL)
	}
	return nil
}

func validateJWTSecret(secret string) error {
	if isPlaceholder(secret) {
		return errors.New("JWT_SECRET is required and must not be a change-me placeholder — generate one with `openssl rand -hex 32`")
	}
	if len(secret) < minJWTSecretLen {
		return fmt.Errorf("JWT_SECRET must be at least %d bytes, got %d — generate one with `openssl rand -hex 32`", minJWTSecretLen, len(secret))
	}
	return nil
}

// isPlaceholder reports whether v is empty or one of the "change-me"
// placeholders from .env.example.
func isPlaceholder(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "" || strings.HasPrefix(v, "change-me") || strings.HasPrefix(v, "changeme")
}
