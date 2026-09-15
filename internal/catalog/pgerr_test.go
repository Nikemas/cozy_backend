package catalog

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestPgErrCodeExtractsCodeFromPgError(t *testing.T) {
	wrapped := fmt.Errorf("insert failed: %w", &pgconn.PgError{Code: pgUniqueViolation})

	if got := pgErrCode(wrapped); got != pgUniqueViolation {
		t.Errorf("pgErrCode() = %q, want %q", got, pgUniqueViolation)
	}
}

func TestPgErrCodeEmptyForNonPgError(t *testing.T) {
	if got := pgErrCode(errors.New("boom")); got != "" {
		t.Errorf("pgErrCode() = %q, want empty", got)
	}
}
