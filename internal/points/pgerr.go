package points

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// pgForeignKeyViolation is the Postgres SQLSTATE code this package
// translates into an apperr.Conflict response, rather than letting it
// surface as an opaque 500. Mirrors internal/catalog/pgerr.go, which is
// unexported and private to package catalog, so this small equivalent
// lives here instead of being imported.
const pgForeignKeyViolation = "23503"

// pgErrCode returns the SQLSTATE code of err if it (or something it wraps)
// is a *pgconn.PgError, and "" otherwise.
func pgErrCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}
