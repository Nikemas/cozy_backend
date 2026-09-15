package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
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

type Service struct {
	otp       *otpRepo
	refresh   *refreshRepo
	customers *storefront.CustomerRepo
	sms       notify.OTPSender
	jwtSecret []byte
}

func NewService(db *sql.DB, sms notify.OTPSender, jwtSecret []byte) *Service {
	return &Service{
		otp:       newOTPRepo(db),
		refresh:   newRefreshRepo(db),
		customers: storefront.NewCustomerRepo(db),
		sms:       sms,
		jwtSecret: jwtSecret,
	}
}

// RequestOTP validates and rate-limits the phone, then asks the SMS
// provider to text a code. Always succeeds from the caller's point of view
// unless the phone is malformed or the phone is over its rate limit —
// never reveals whether the number belongs to an existing customer.
func (s *Service) RequestOTP(ctx context.Context, rawPhone string) error {
	phone, err := NormalizePhone(rawPhone)
	if err != nil {
		return err
	}

	if err := s.checkRateLimit(ctx, phone); err != nil {
		return err
	}

	transactionID, err := newTransactionID()
	if err != nil {
		return err
	}

	token, err := s.sms.SendCode(ctx, forNikita(phone), transactionID)
	if err != nil {
		return err
	}

	return s.otp.create(ctx, phone, transactionID, token, time.Now().Add(otpTTL))
}

func (s *Service) checkRateLimit(ctx context.Context, phone string) error {
	lastAt, ok, err := s.otp.lastRequestAt(ctx, phone)
	if err != nil {
		return err
	}
	if ok && time.Since(lastAt) < otpResendCooldown {
		return apperr.New(http.StatusTooManyRequests, "otp_cooldown", "код уже отправлен, попробуйте чуть позже")
	}

	count, err := s.otp.countRecentRequests(ctx, phone, time.Now().Add(-time.Hour))
	if err != nil {
		return err
	}
	if count >= otpMaxPerHour {
		return apperr.New(http.StatusTooManyRequests, "otp_rate_limited", "слишком много запросов кода, попробуйте позже")
	}
	return nil
}

// VerifyOTP checks the code against the active OTP transaction for phone
// and, if correct, gets-or-creates the customer and issues a token pair.
// The customer is also returned (already fetched internally to get its ID
// for the token pair) so callers like the mobile JSON API can return the
// profile inline without a second round-trip right after login.
func (s *Service) VerifyOTP(ctx context.Context, rawPhone, code string) (accessToken, refreshTokenStr string, customer *storefront.Customer, err error) {
	phone, err := NormalizePhone(rawPhone)
	if err != nil {
		return "", "", nil, err
	}

	active, err := s.otp.latestActive(ctx, phone)
	if err != nil {
		return "", "", nil, err
	}
	if active == nil {
		return "", "", nil, apperr.BadRequest("otp_expired", "код устарел, запросите новый")
	}

	if err := s.sms.VerifyCode(ctx, active.Token, code); err != nil {
		return "", "", nil, err
	}
	if err := s.otp.markConsumed(ctx, active.ID); err != nil {
		return "", "", nil, err
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

// Refresh rotates a refresh token: the old one is revoked and a new pair
// is issued, so a leaked refresh token has a limited window of usefulness.
func (s *Service) Refresh(ctx context.Context, refreshTokenStr string) (accessToken, newRefreshToken string, err error) {
	existing, err := s.refresh.getActiveByHash(ctx, hashToken(refreshTokenStr))
	if err != nil {
		return "", "", err
	}
	if existing == nil {
		return "", "", apperr.Unauthorized("invalid_refresh_token", "недействительный refresh-токен")
	}

	if err := s.refresh.revoke(ctx, existing.ID); err != nil {
		return "", "", err
	}

	return s.issueTokenPair(ctx, existing.CustomerID)
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
