package staff

import (
	"context"
	"time"
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

func (f *fakeSessionStore) revokeByHash(_ context.Context, tokenHash string) error {
	s, ok := f.byHash[tokenHash]
	if !ok {
		return nil
	}
	now := time.Now()
	s.RevokedAt = &now
	return nil
}
