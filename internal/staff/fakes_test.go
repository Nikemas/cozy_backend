package staff

import (
	"context"
	"fmt"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// fakeStaffGetter is an in-memory staffGetter for tests, keyed by phone and
// id so Login/RequireRole logic can be exercised without a database.
type fakeStaffGetter struct {
	byPhone map[string]*Staff
	byID    map[string]*Staff
}

func newFakeStaffGetter(staffers ...*Staff) *fakeStaffGetter {
	f := &fakeStaffGetter{byPhone: map[string]*Staff{}, byID: map[string]*Staff{}}
	for _, s := range staffers {
		f.byPhone[s.Phone] = s
		f.byID[s.ID] = s
	}
	return f
}

func (f *fakeStaffGetter) GetByPhone(_ context.Context, phone string) (*Staff, error) {
	return f.byPhone[phone], nil
}

func (f *fakeStaffGetter) GetByID(_ context.Context, id string) (*Staff, error) {
	return f.byID[id], nil
}

// fakeSessionStore is an in-memory sessionStore for tests.
type fakeSessionStore struct {
	byHash map[string]*Session
}

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{byHash: map[string]*Session{}}
}

func (f *fakeSessionStore) create(_ context.Context, staffID, tokenHash string, expiresAt time.Time) error {
	f.byHash[tokenHash] = &Session{ID: "sess-" + tokenHash, StaffID: staffID, ExpiresAt: expiresAt}
	return nil
}

func (f *fakeSessionStore) getActiveByHash(_ context.Context, tokenHash string) (*Session, error) {
	s, ok := f.byHash[tokenHash]
	if !ok {
		return nil, nil
	}
	if s.RevokedAt != nil || time.Now().After(s.ExpiresAt) {
		return nil, nil
	}
	return s, nil
}

func (f *fakeSessionStore) revokeAllForStaff(_ context.Context, staffID string) error {
	now := time.Now()
	for _, s := range f.byHash {
		if s.StaffID == staffID && s.RevokedAt == nil {
			s.RevokedAt = &now
		}
	}
	return nil
}

func (f *fakeSessionStore) revokeByHash(_ context.Context, tokenHash string) error {
	s, ok := f.byHash[tokenHash]
	if !ok {
		return nil
	}
	now := time.Now()
	s.RevokedAt = &now
	return nil
}

// fakeStaffAdmin is an in-memory staffAdmin for tests. Its Update method
// replicates the same "at least one active owner" invariant that the real
// Repo.Update enforces inside a Postgres transaction (see admin.go) — there
// being only one goroutine in these tests, no locking is needed to get the
// same check-then-write behavior, just the same decision logic. This is
// what lets Service.UpdateStaff's validation and error propagation be
// tested without a live database.
type fakeStaffAdmin struct {
	byID map[string]*Staff
}

func newFakeStaffAdmin(staffers ...*Staff) *fakeStaffAdmin {
	f := &fakeStaffAdmin{byID: map[string]*Staff{}}
	for _, s := range staffers {
		cp := *s
		f.byID[s.ID] = &cp
	}
	return f
}

func (f *fakeStaffAdmin) List(_ context.Context) ([]Staff, error) {
	list := make([]Staff, 0, len(f.byID))
	for _, s := range f.byID {
		list = append(list, *s)
	}
	return list, nil
}

func (f *fakeStaffAdmin) Create(_ context.Context, in StaffCreateInput) (*Staff, error) {
	for _, s := range f.byID {
		if s.Phone == in.Phone {
			return nil, apperr.Conflict("phone_taken", "этот номер телефона уже используется")
		}
	}
	s := &Staff{
		ID:           fmt.Sprintf("new-%d", len(f.byID)+1),
		Phone:        in.Phone,
		PasswordHash: in.PasswordHash,
		Name:         in.Name,
		Role:         in.Role,
		PointID:      in.PointID,
		IsActive:     true,
	}
	f.byID[s.ID] = s
	cp := *s
	return &cp, nil
}

func (f *fakeStaffAdmin) Update(_ context.Context, id string, in StaffUpdateInput) (*Staff, error) {
	current, ok := f.byID[id]
	if !ok {
		return nil, apperr.NotFound("staff_not_found", "сотрудник не найден")
	}

	losesOwnerStatus := current.Role == RoleOwner && current.IsActive && (in.Role != RoleOwner || !in.IsActive)
	if losesOwnerStatus {
		others := 0
		for oid, s := range f.byID {
			if oid != id && s.Role == RoleOwner && s.IsActive {
				others++
			}
		}
		if others == 0 {
			return nil, apperr.Conflict("last_owner", "нельзя понизить или деактивировать последнего владельца")
		}
	}

	passwordHash := current.PasswordHash
	if in.PasswordHash != nil {
		passwordHash = *in.PasswordHash
	}

	updated := &Staff{
		ID:           current.ID,
		Phone:        current.Phone,
		PasswordHash: passwordHash,
		Name:         in.Name,
		Role:         in.Role,
		PointID:      in.PointID,
		IsActive:     in.IsActive,
		CreatedAt:    current.CreatedAt,
	}
	f.byID[id] = updated
	cp := *updated
	return &cp, nil
}

func (f *fakeStaffAdmin) SetPassword(_ context.Context, id, passwordHash string) error {
	s, ok := f.byID[id]
	if !ok {
		return apperr.NotFound("staff_not_found", "сотрудник не найден")
	}
	s.PasswordHash = passwordHash
	return nil
}
