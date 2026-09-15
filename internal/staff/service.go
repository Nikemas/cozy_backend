package staff

import (
	"context"
	"database/sql"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// sessionTTL is deliberately shorter than the customer refresh-token TTL —
// staff sessions guard write access to the whole shop, not just one
// customer's own data.
const sessionTTL = 12 * time.Hour

// dummyPasswordHash is a valid bcrypt hash of an arbitrary, never-used
// password. Login runs a bcrypt compare against this hash whenever the
// phone number isn't found, so an unknown phone costs the same CPU time as
// a real one with a wrong password — without it, the missing bcrypt call
// makes "phone not found" measurably faster than "wrong password" and lets
// an attacker enumerate valid staff phone numbers via response timing.
const dummyPasswordHash = "$2a$10$UnWYtHLgrH/2tNOF3VF.k.Mno4/aqJ1u8/dKRjqdLBiwUsDvBTD7W"

// staffGetter is the subset of *Repo that Service depends on. Defined as an
// interface so tests can inject a fake in-memory lookup instead of a real
// database.
type staffGetter interface {
	GetByPhone(ctx context.Context, phone string) (*Staff, error)
	GetByID(ctx context.Context, id string) (*Staff, error)
}

// sessionStore is the subset of *sessionRepo that Service depends on.
type sessionStore interface {
	create(ctx context.Context, staffID, tokenHash string, expiresAt time.Time) error
	getActiveByHash(ctx context.Context, tokenHash string) (*Session, error)
	revokeByHash(ctx context.Context, tokenHash string) error
}

// Service implements staff login/logout and session resolution.
type Service struct {
	staff    staffGetter
	sessions sessionStore
}

func NewService(db *sql.DB) *Service {
	return &Service{
		staff:    NewRepo(db),
		sessions: newSessionRepo(db),
	}
}

// Login checks phone+password against the staff table (bcrypt compare of
// password_hash) and, on success, creates a server-side session and
// returns its opaque token for the caller to set as an httpOnly cookie.
// Wrong password, unknown phone, or an inactive staff account all fail the
// same way — apperr.Unauthorized, and a bcrypt compare always runs — so a
// login attempt can't be used to enumerate valid phone numbers or find
// disabled accounts, whether by response content or by timing.
func (s *Service) Login(ctx context.Context, phone, password string) (sessionToken string, err error) {
	st, err := s.staff.GetByPhone(ctx, phone)
	if err != nil {
		return "", err
	}

	// Always compare against a bcrypt hash, even when the phone isn't
	// found, so this call takes the same amount of time either way.
	pwHash := dummyPasswordHash
	if st != nil {
		pwHash = st.PasswordHash
	}
	cmpErr := bcrypt.CompareHashAndPassword([]byte(pwHash), []byte(password))

	if st == nil || !st.IsActive || cmpErr != nil {
		return "", invalidCredentials()
	}

	raw, tokenHash, err := newSessionToken()
	if err != nil {
		return "", err
	}
	if err := s.sessions.create(ctx, st.ID, tokenHash, time.Now().Add(sessionTTL)); err != nil {
		return "", err
	}

	return raw, nil
}

// Logout revokes the session behind sessionToken. Revoking an
// already-invalid or unknown token is a no-op, so logout stays idempotent.
func (s *Service) Logout(ctx context.Context, sessionToken string) error {
	if sessionToken == "" {
		return nil
	}
	return s.sessions.revokeByHash(ctx, hashSessionToken(sessionToken))
}

// authenticatedStaff resolves a raw session token to the active staff
// member it belongs to, or (nil, nil) if the session is
// missing/expired/revoked or the staff account has since been deactivated.
func (s *Service) authenticatedStaff(ctx context.Context, sessionToken string) (*Staff, error) {
	sess, err := s.sessions.getActiveByHash(ctx, hashSessionToken(sessionToken))
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, nil
	}

	st, err := s.staff.GetByID(ctx, sess.StaffID)
	if err != nil {
		return nil, err
	}
	if st == nil || !st.IsActive {
		return nil, nil
	}
	return st, nil
}

func invalidCredentials() error {
	return apperr.Unauthorized("invalid_credentials", "неверный телефон или пароль")
}
