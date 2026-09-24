package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/storefront"
)

// fakeOTPStore is an in-memory otpStore.
type fakeOTPStore struct {
	mu     sync.Mutex
	rows   map[string]*fakeOTPRow
	nextID int
	lastIP string
}

type fakeOTPRow struct {
	phone, token string
	attempts     int
	consumed     bool
}

func newFakeOTPStore() *fakeOTPStore { return &fakeOTPStore{rows: map[string]*fakeOTPRow{}} }

func (f *fakeOTPStore) reserve(_ context.Context, phone, ip, _ string, _ time.Time, _ otpSendLimits) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	id := fmt.Sprintf("otp-%d", f.nextID)
	f.rows[id] = &fakeOTPRow{phone: phone}
	f.lastIP = ip
	return id, nil
}

func (f *fakeOTPStore) setToken(_ context.Context, id, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[id].token = token
	return nil
}

func (f *fakeOTPStore) burn(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[id].consumed = true
	return nil
}

func (f *fakeOTPStore) latestActive(_ context.Context, phone string) (*otpCode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := f.nextID; i > 0; i-- {
		id := fmt.Sprintf("otp-%d", i)
		r := f.rows[id]
		if r.phone == phone && !r.consumed && r.token != "" {
			return &otpCode{ID: id, Phone: phone, Token: r.token}, nil
		}
	}
	return nil, nil
}

func (f *fakeOTPStore) takeAttempt(_ context.Context, id string, max int) (int, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.rows[id]
	if r.consumed || r.attempts >= max {
		return 0, false, nil
	}
	r.attempts++
	return r.attempts, true, nil
}

func (f *fakeOTPStore) consume(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.rows[id]
	if r.consumed {
		return false, nil
	}
	r.consumed = true
	return true, nil
}

// fakeSMS accepts code "1234"; sendErr makes SendCode fail.
type fakeSMS struct {
	sendErr   error
	verifyErr error // overrides the code check, e.g. a provider outage
}

func (f *fakeSMS) SendCode(_ context.Context, _, transactionID string) (string, error) {
	if f.sendErr != nil {
		return "", f.sendErr
	}
	return "tok-" + transactionID, nil
}

func (f *fakeSMS) VerifyCode(_ context.Context, _, code string) error {
	if f.verifyErr != nil {
		return f.verifyErr
	}
	if code != "1234" {
		return apperr.BadRequest("otp_invalid", "неверный код")
	}
	return nil
}

type fakeCustomers struct{}

func (fakeCustomers) GetOrCreateByPhone(_ context.Context, phone string) (*storefront.Customer, error) {
	return &storefront.Customer{ID: "cust-" + phone, Phone: phone}, nil
}

var errProviderDown = apperr.New(http.StatusBadGateway, "sms_provider_unreachable", "down")

func newTestService(otp otpStore, sms *fakeSMS, limits config.AuthLimits) *Service {
	return &Service{
		otp:         otp,
		refresh:     newFakeRefreshStore(),
		customers:   fakeCustomers{},
		sms:         sms,
		jwtSecret:   testJWTSecret,
		limits:      limits,
		verifyFails: newWindowLimiter(limits.OTPVerifyFailsPerIPPerHour, time.Hour),
		refreshIP:   newWindowLimiter(limits.RefreshPerIPPerMinute, time.Minute),
	}
}

// fakeRefreshStore is an in-memory refreshStore.
type fakeRefreshStore struct {
	mu     sync.Mutex
	byHash map[string]*fakeRefreshRow
	nextID int
}

type fakeRefreshRow struct {
	refreshToken
	revoked bool
}

func newFakeRefreshStore() *fakeRefreshStore {
	return &fakeRefreshStore{byHash: map[string]*fakeRefreshRow{}}
}

func (f *fakeRefreshStore) insert(customerID, familyID, hash string) {
	f.nextID++
	if familyID == "" {
		familyID = fmt.Sprintf("fam-%d", f.nextID)
	}
	f.byHash[hash] = &fakeRefreshRow{refreshToken: refreshToken{ID: fmt.Sprintf("rt-%d", f.nextID), CustomerID: customerID, FamilyID: familyID}}
}

func (f *fakeRefreshStore) create(_ context.Context, customerID, hash string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.insert(customerID, "", hash)
	return nil
}

func (f *fakeRefreshStore) rotate(_ context.Context, oldHash, newHash string, _ time.Time) (*refreshToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.byHash[oldHash]
	if !ok || r.revoked {
		return nil, nil
	}
	now := time.Now()
	r.revoked, r.RotatedAt = true, &now
	f.insert(r.CustomerID, r.FamilyID, newHash)
	t := r.refreshToken
	return &t, nil
}

func (f *fakeRefreshStore) lookup(_ context.Context, hash string) (*refreshToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.byHash[hash]
	if !ok {
		return nil, nil
	}
	t := r.refreshToken
	return &t, nil
}

func (f *fakeRefreshStore) revokeFamily(_ context.Context, familyID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.byHash {
		if r.FamilyID == familyID {
			r.revoked = true
		}
	}
	return nil
}

// active reports whether the raw token would still be accepted.
func (f *fakeRefreshStore) active(raw string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.byHash[hashToken(raw)]
	return ok && !r.revoked
}
