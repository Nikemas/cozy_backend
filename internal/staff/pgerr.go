package staff

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// Postgres SQLSTATE codes this package translates into apperr responses,
// rather than letting them surface as opaque 500s. Mirrors
// internal/catalog/pgerr.go — duplicated locally since there's no shared
// pgerr helper package yet.
const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
)

// pgErrCode returns the SQLSTATE code of err if it (or something it wraps)
// is a *pgconn.PgError, and "" otherwise.
func pgErrCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}
