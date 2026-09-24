package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/httpmw"
	"github.com/Nikemas/cozy_backend/internal/notify"
	"github.com/Nikemas/cozy_backend/internal/storefront"
)

const (
	otpTTL            = 5 * time.Minute  // mirrors the TTL configured in the Nikita dashboard
	otpResendCooldown = 60 * time.Second // minimum gap between two requests for the same phone
	otpMaxPerHour     = 5                // hard cap per phone — SMS cost is a real abuse vector
	accessTokenTTL    = 15 * time.Minute
	refreshTokenTTL   = 30 * 24 * time.Hour
)

// customerStore is the subset of *storefront.CustomerRepo the login flow
// needs.
type customerStore interface {
	GetOrCreateByPhone(ctx context.Context, phone string) (*storefront.Customer, error)
}

type Service struct {
	otp       otpStore
	refresh   refreshStore
	customers customerStore
	sms       notify.OTPSender
	jwtSecret []byte

	limits      config.AuthLimits
	verifyFails *windowLimiter // wrong OTP codes per client IP per hour
	refreshIP   *windowLimiter // /auth/refresh calls per client IP per minute
}

// NewService wires the customer auth service. limits come from
// config.Security.Auth (env-configurable, see .env.example).
func NewService(db *sql.DB, sms notify.OTPSender, jwtSecret []byte, limits config.AuthLimits) *Service {
	return &Service{
		otp:         newOTPRepo(db),
		refresh:     newRefreshRepo(db),
		customers:   storefront.NewCustomerRepo(db),
		sms:         sms,
		jwtSecret:   jwtSecret,
		limits:      limits,
		verifyFails: newWindowLimiter(limits.OTPVerifyFailsPerIPPerHour, time.Hour),
		refreshIP:   newWindowLimiter(limits.RefreshPerIPPerMinute, time.Minute),
	}
}

// RequestOTP validates and rate-limits the phone, then asks the SMS
// provider to text a code. Always succeeds from the caller's point of view
// unless the phone is malformed or a rate limit is hit — never reveals
// whether the number belongs to an existing customer.
//
// Limits (all checked atomically under a per-phone lock, see
// otpRepo.reserve): 60 s cooldown and 5/hour per phone, OTP_MAX_PER_IP_PER_HOUR
// per client IP (httpmw.ClientIP in ctx), and OTP_MAX_PER_DAY across all
// phones as a hard ceiling on SMS spend.
func (s *Service) RequestOTP(ctx context.Context, rawPhone string) error {
	phone, err := NormalizePhone(rawPhone)
	if err != nil {
		return err
	}

	transactionID, err := newTransactionID()
	if err != nil {
		return err
	}

	id, err := s.otp.reserve(ctx, phone, httpmw.ClientIPFromContext(ctx), transactionID, time.Now().Add(otpTTL), otpSendLimits{
		Cooldown:  otpResendCooldown,
		PerPhone:  otpMaxPerHour,
		PerIP:     s.limits.OTPPerIPPerHour,
		GlobalDay: s.limits.OTPPerDay,
	})
	if err != nil {
		return err
	}

	token, err := s.sms.SendCode(ctx, forNikita(phone), transactionID)
	if err != nil {
		if berr := s.otp.burn(ctx, id); berr != nil {
			slog.WarnContext(ctx, "auth: failed to burn OTP row after SMS send error", "err", berr)
		}
		return err
	}

	return s.otp.setToken(ctx, id, token)
}

func errOTPAttemptsExceeded() error {
	return apperr.TooManyRequests("otp_attempts_exceeded", "слишком много неверных попыток, запросите новый код")
}

// VerifyOTP checks the code against the active OTP transaction for phone
// and, if correct, gets-or-creates the customer and issues a token pair.
// The customer is also returned (already fetched internally to get its ID
// for the token pair) so callers like the mobile JSON API can return the
// profile inline without a second round-trip right after login.
//
// Every code allows OTP_VERIFY_MAX_ATTEMPTS attempts (spent atomically
// before the provider check); the last wrong one burns the code. Wrong
// codes are also counted per client IP, so one IP can't spread guesses
// across many phones.
func (s *Service) VerifyOTP(ctx context.Context, rawPhone, code string) (accessToken, refreshTokenStr string, customer *storefront.Customer, err error) {
	phone, err := NormalizePhone(rawPhone)
	if err != nil {
		return "", "", nil, err
	}

	ip := httpmw.ClientIPFromContext(ctx)
	if s.verifyFails.blocked(ip) {
		return "", "", nil, errOTPAttemptsExceeded()
	}

	active, err := s.otp.latestActive(ctx, phone)
	if err != nil {
		return "", "", nil, err
	}
	if active == nil {
		return "", "", nil, apperr.BadRequest("otp_expired", "код устарел, запросите новый")
	}

	maxAttempts := s.limits.OTPVerifyMaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	attempt, ok, err := s.otp.takeAttempt(ctx, active.ID, maxAttempts)
	if err != nil {
		return "", "", nil, err
	}
	if !ok {
		return "", "", nil, errOTPAttemptsExceeded()
	}

	if err := s.sms.VerifyCode(ctx, active.Token, code); err != nil {
		var ae *apperr.AppError
		if errors.As(err, &ae) && ae.Status < http.StatusInternalServerError {
			// A wrong/expired code, not a provider outage.
			s.verifyFails.hit(ip)
			if attempt >= maxAttempts {
				if berr := s.otp.burn(ctx, active.ID); berr != nil {
					return "", "", nil, berr
				}
				return "", "", nil, errOTPAttemptsExceeded()
			}
		}
		return "", "", nil, err
	}
	consumed, err := s.otp.consume(ctx, active.ID)
	if err != nil {
		return "", "", nil, err
	}
	if !consumed {
		return "", "", nil, apperr.BadRequest("otp_expired", "код устарел, запросите новый")
	}

	customer, err = s.customers.GetOrCreateByPhone(ctx, phone)
	if err != nil {
		return "", "", nil, err
	}

	accessToken, refreshTokenStr, err = s.issueTokenPair(ctx, customer.ID)
	if err != nil {
		return "", "", nil, err
	}
	return accessToken, refreshTokenStr, customer, nil
}

func errRefreshInvalid() error {
	return apperr.Unauthorized("refresh_invalid", "сессия истекла, войдите снова")
}

// Refresh rotates a refresh token: the presented one is retired and a new
// pair is issued in the same token family, so a leaked refresh token has a
// limited window of usefulness.
//
// Presenting a token that was ALREADY rotated means two parties hold the
// same token — the legitimate app and whoever copied it — and we can't
// tell which is which, so the whole family is revoked and both have to
// log in again (OAuth 2.0 Security BCP, refresh token reuse detection).
// Calls are also rate-limited per client IP
// (AUTH_REFRESH_MAX_PER_IP_PER_MINUTE).
func (s *Service) Refresh(ctx context.Context, refreshTokenStr string) (accessToken, newRefreshToken string, err error) {
	if !s.refreshIP.allow(httpmw.ClientIPFromContext(ctx)) {
		return "", "", apperr.TooManyRequests("refresh_rate_limited", "слишком много запросов, попробуйте позже")
	}
	if refreshTokenStr == "" {
		return "", "", errRefreshInvalid()
	}

	oldHash := hashToken(refreshTokenStr)
	raw, newHash, err := newOpaqueToken()
	if err != nil {
		return "", "", err
	}
	rotated, err := s.refresh.rotate(ctx, oldHash, newHash, time.Now().Add(refreshTokenTTL))
	if err != nil {
		return "", "", err
	}
	if rotated == nil {
		if err := s.handleInactiveRefresh(ctx, oldHash); err != nil {
			return "", "", err
		}
		return "", "", errRefreshInvalid()
	}

	accessToken, err = issueAccessToken(s.jwtSecret, rotated.CustomerID, accessTokenTTL)
	if err != nil {
		return "", "", err
	}
	return accessToken, raw, nil
}

// handleInactiveRefresh revokes the family of an already-rotated token.
func (s *Service) handleInactiveRefresh(ctx context.Context, tokenHash string) error {
	old, err := s.refresh.lookup(ctx, tokenHash)
	if err != nil {
		return err
	}
	if old == nil || old.RotatedAt == nil {
		return nil
	}
	slog.WarnContext(ctx, "auth: refresh token reuse detected, revoking the whole token family",
		"customer_id", old.CustomerID, "family_id", old.FamilyID, "client_ip", httpmw.ClientIPFromContext(ctx))
	return s.refresh.revokeFamily(ctx, old.FamilyID)
}

// Logout revokes the session the refresh token belongs to (its whole
// family). Unknown, expired or already-revoked tokens are a no-op, so it
// is idempotent.
func (s *Service) Logout(ctx context.Context, refreshTokenStr string) error {
	if refreshTokenStr == "" {
		return nil
	}
	t, err := s.refresh.lookup(ctx, hashToken(refreshTokenStr))
	if err != nil || t == nil {
		return err
	}
	return s.refresh.revokeFamily(ctx, t.FamilyID)
}

func (s *Service) issueTokenPair(ctx context.Context, customerID string) (accessToken, refreshTokenStr string, err error) {
	accessToken, err = issueAccessToken(s.jwtSecret, customerID, accessTokenTTL)
	if err != nil {
		return "", "", err
	}

	raw, hash, err := newOpaqueToken()
	if err != nil {
		return "", "", err
	}
	if err := s.refresh.create(ctx, customerID, hash, time.Now().Add(refreshTokenTTL)); err != nil {
		return "", "", err
	}

	return accessToken, raw, nil
}

func newTransactionID() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil // 24 hex chars, well under Nikita's 32-char limit
}
