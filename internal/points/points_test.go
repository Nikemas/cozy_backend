package points

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

func assertAppErrStatus(t *testing.T, err error, status int) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected *apperr.AppError, got %T (%v)", err, err)
	}
	if appErr.Status != status {
		t.Errorf("Status = %d, want %d (code=%s, msg=%s)", appErr.Status, status, appErr.Code, appErr.Message)
	}
}

// Create/Update validate PointInput before ever touching the *sql.DB, so
// NewPointsRepo(nil) is safe to exercise here — same convention as
// internal/catalog/category_write_test.go.

func TestPointCreateValidatesRequiredFields(t *testing.T) {
	repo := NewPointsRepo(nil)

	cases := []struct {
		name string
		in   PointInput
	}{
		{"missing name", PointInput{Address: "ул. Чуй, 1"}},
		{"missing address", PointInput{Name: "Точка на Чуй"}},
		{"blank name", PointInput{Name: "   ", Address: "ул. Чуй, 1"}},
		{"blank address", PointInput{Name: "Точка на Чуй", Address: "   "}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := repo.Create(context.Background(), c.in)
			assertAppErrStatus(t, err, http.StatusBadRequest)
		})
	}
}

func TestPointUpdateValidatesRequiredFields(t *testing.T) {
	repo := NewPointsRepo(nil)

	_, err := repo.Update(context.Background(), "point-1", PointInput{Name: "Точка на Чуй"})

	assertAppErrStatus(t, err, http.StatusBadRequest)
}

func TestTranslateDeleteErrConflictOnForeignKeyViolation(t *testing.T) {
	err := translateDeleteErr(&pgconn.PgError{Code: pgForeignKeyViolation})

	assertAppErrStatus(t, err, http.StatusConflict)
}

func TestTranslateDeleteErrPassesThroughOtherErrors(t *testing.T) {
	original := errors.New("boom")

	if got := translateDeleteErr(original); !errors.Is(got, original) {
		t.Errorf("translateDeleteErr(boom) = %v, want passthrough of the original error", got)
	}
}

func TestTranslateDeleteErrPassesThroughOtherPgErrCodes(t *testing.T) {
	err := translateDeleteErr(&pgconn.PgError{Code: "23505"}) // unique_violation, not our FK case

	var appErr *apperr.AppError
	if errors.As(err, &appErr) {
		t.Fatalf("translateDeleteErr should not turn a non-FK pg error into an AppError, got %+v", appErr)
	}
}

func TestPgErrCodeExtractsCodeFromWrappedPgError(t *testing.T) {
	wrapped := errors.Join(errors.New("delete failed"), &pgconn.PgError{Code: pgForeignKeyViolation})

	if got := pgErrCode(wrapped); got != pgForeignKeyViolation {
		t.Errorf("pgErrCode() = %q, want %q", got, pgForeignKeyViolation)
	}
}

func TestPgErrCodeEmptyForNonPgError(t *testing.T) {
	if got := pgErrCode(errors.New("boom")); got != "" {
		t.Errorf("pgErrCode() = %q, want empty", got)
	}
}
