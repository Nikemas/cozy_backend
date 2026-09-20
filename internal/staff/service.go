package staff

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
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

// staffAdmin is the subset of *Repo that Service's admin CRUD methods
// (CreateStaff/UpdateStaff/ListStaff) depend on — mirrors the
// staffGetter/sessionStore pattern above so this stays testable with a fake
// instead of a live database. The real *Repo.Update implementation owns the
// last-owner-invariant transaction; a test fake replicates that same
// business rule in memory to exercise Service's validation and error
// propagation without Postgres.
type staffAdmin interface {
	List(ctx context.Context) ([]Staff, error)
	Create(ctx context.Context, in StaffCreateInput) (*Staff, error)
	Update(ctx context.Context, id string, in StaffUpdateInput) (*Staff, error)
}

// Service implements staff login/logout, session resolution, and (for
// RoleOwner only, gated at the route level) admin CRUD over staff accounts.
type Service struct {
	staff    staffGetter
	sessions sessionStore
	admin    staffAdmin

	loginLimiter loginRateLimiter
}

func NewService(db *sql.DB) *Service {
	repo := NewRepo(db)
	return &Service{
		staff:    repo,
		sessions: newSessionRepo(db),
		admin:    repo,
	}
}

// Login checks phone+password against the staff table (bcrypt compare of
// password_hash) and, on success, creates a server-side session and
// returns its opaque token for the caller to set as an httpOnly cookie.
// Wrong password, unknown phone, or an inactive staff account all fail the
// same way — apperr.Unauthorized, and a bcrypt compare always runs — so a
// login attempt can't be used to enumerate valid phone numbers or find
// disabled accounts, whether by response content or by timing.
//
// Attempts are throttled per phone number (see loginRateLimiter) before
// any of that: once the budget for a phone is used up within the window,
// Login rejects with apperr.TooManyRequests without touching the database
// or running bcrypt, so brute-forcing one account's password can't be
// sped up by parallelizing requests.
func (s *Service) Login(ctx context.Context, phone, password string) (sessionToken string, err error) {
	if !s.loginLimiter.allow(phone) {
		return "", apperr.TooManyRequests("too_many_attempts", "слишком много попыток входа, попробуйте позже")
	}

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

	s.loginLimiter.reset(phone)

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

// StaffFromRequest resolves the staff_session cookie on r to the active
// staff member it belongs to, or (nil, nil) if there's no valid session
// (missing cookie, unknown/expired/revoked session, or a deactivated staff
// account). It's the HTML-page counterpart of RequireRole: RequireRole
// answers JSON API requests with a 401/403 body when the session is
// invalid, which doesn't make sense for a page a person is looking at in a
// browser — internal/admin's own auth-gate calls this instead and decides
// what to render/redirect to itself.
func (s *Service) StaffFromRequest(r *http.Request) (*Staff, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil, nil
	}
	return s.authenticatedStaff(r.Context(), cookie.Value)
}

func invalidCredentials() error {
	return apperr.Unauthorized("invalid_credentials", "неверный телефон или пароль")
}

// CreateStaffInput carries the create form of a staff account, as decoded
// from the request body — Password is plaintext here; Service hashes it
// before it ever reaches the repo.
type CreateStaffInput struct {
	Phone    string
	Password string
	Name     string
	Role     Role
	PointID  *string
}

// UpdateStaffInput carries the update form of a staff account. Password is
// nil to leave the stored password unchanged — the PUT endpoint's password
// field is optional for exactly that reason.
type UpdateStaffInput struct {
	Name     string
	Role     Role
	PointID  *string
	IsActive bool
	Password *string
}

// ListStaff returns every staff account for the admin staff list. Response
// DTOs are built by the route handler, not here — Staff.PasswordHash never
// leaves this package as JSON.
func (s *Service) ListStaff(ctx context.Context) ([]Staff, error) {
	return s.admin.List(ctx)
}

// CreateStaff validates in, hashes the plaintext password with bcrypt (same
// cost factor as everywhere else in this package,
// bcrypt.DefaultCost — see Login), and creates the staff account.
// point_id/role compatibility is checked here, in Go, before ever reaching
// the database: point_id is required for point_staff and forbidden for
// owner/manager. Duplicate phone and unknown point_id are caught by the
// repo's Postgres constraint translation (see translateStaffWriteErr) and
// surface here as ordinary *apperr.AppError values.
func (s *Service) CreateStaff(ctx context.Context, in CreateStaffInput) (*Staff, error) {
	if strings.TrimSpace(in.Phone) == "" {
		return nil, apperr.BadRequest("invalid_phone", "телефон обязателен")
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, apperr.BadRequest("invalid_name", "имя обязательно")
	}
	if in.Password == "" {
		return nil, apperr.BadRequest("invalid_password", "пароль обязателен")
	}
	if err := validateStaffRolePointID(in.Role, in.PointID); err != nil {
		return nil, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	return s.admin.Create(ctx, StaffCreateInput{
		Phone:        in.Phone,
		PasswordHash: string(hash),
		Name:         in.Name,
		Role:         in.Role,
		PointID:      in.PointID,
	})
}

// UpdateStaff validates in the same way CreateStaff does, hashes the new
// password when one is given, and delegates to the repo, which enforces the
// last-active-owner invariant atomically (see Repo.Update). A rejection of
// that invariant — apperr.Conflict("last_owner", ...) — surfaces here
// unchanged, and leaves the staff account untouched.
func (s *Service) UpdateStaff(ctx context.Context, id string, in UpdateStaffInput) (*Staff, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, apperr.BadRequest("invalid_name", "имя обязательно")
	}
	if err := validateStaffRolePointID(in.Role, in.PointID); err != nil {
		return nil, err
	}

	upd := StaffUpdateInput{
		Name:     in.Name,
		Role:     in.Role,
		PointID:  in.PointID,
		IsActive: in.IsActive,
	}
	if in.Password != nil {
		if *in.Password == "" {
			return nil, apperr.BadRequest("invalid_password", "пароль не может быть пустым")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(*in.Password), bcrypt.DefaultCost)
		if err != nil {
			return nil, err
		}
		h := string(hash)
		upd.PasswordHash = &h
	}

	return s.admin.Update(ctx, id, upd)
}

// validateStaffRolePointID enforces §"Business rules" 1: point_id is
// required when role is point_staff, and must be absent for owner/manager.
// Shared by CreateStaff and UpdateStaff so the rule can't drift between the
// two.
func validateStaffRolePointID(role Role, pointID *string) error {
	switch role {
	case RoleOwner, RoleManager:
		if pointID != nil && strings.TrimSpace(*pointID) != "" {
			return apperr.BadRequest("point_id_forbidden", "point_id должен быть пустым для роли owner/manager")
		}
	case RolePointStaff:
		if pointID == nil || strings.TrimSpace(*pointID) == "" {
			return apperr.BadRequest("point_id_required", "point_id обязателен для роли point_staff")
		}
	default:
		return apperr.BadRequest("invalid_role", "неизвестная роль")
	}
	return nil
}
