// Package dbtx provides a single shared helper for running a block of
// repository code inside one *sql.Tx. Several packages (staff, catalog,
// orders, storefront) each hand-rolled the same
// BeginTx/defer-Rollback/Commit boilerplate — see e.g. the comment on
// staff.Repo.Update — because there was no shared helper for it. This is
// that helper.
package dbtx

import (
	"context"
	"database/sql"
)

// WithTx begins a transaction on db, passes it to fn, and commits if fn
// returns nil. If fn returns a non-nil error, or Commit itself fails, the
// transaction is rolled back (a no-op after a successful Commit) and the
// error is returned unchanged — callers keep translating domain-specific
// errors (apperr, Postgres constraint codes) exactly as they did with the
// hand-rolled version.
func WithTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := fn(tx); err != nil {
		return err
	}

	return tx.Commit()
}
