// Package audit is the journal of staff actions in the admin panel: who
// changed what, when, and from which IP — products, variants, stock,
// categories, points of sale and staff accounts (table audit_log,
// migration 000037).
//
// Order status changes are deliberately not written here: every
// transition is already recorded, with the acting staff member, in
// order_status_history (internal/orders). List merges those rows in, so
// the journal shows them without a second copy that could drift.
//
// Writing to the journal must never fail the action being journaled.
// Record (outside a transaction) logs and swallows errors; RecordTx
// (inside the caller's transaction) wraps its inserts in a SAVEPOINT, so a
// failed insert is rolled back on its own instead of aborting the
// caller's transaction.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"

	"github.com/Nikemas/cozy_backend/internal/httpmw"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// Entity types stored in audit_log.entity_type. EntityOrder rows come from
// order_status_history (see List), never from Record.
const (
	EntityProduct  = "product"
	EntityVariant  = "variant"
	EntityStock    = "stock"
	EntityCategory = "category"
	EntityPoint    = "point"
	EntityStaff    = "staff"
	EntityOrder    = "order"
)

// Actions. The prefix before the dot is the entity type.
const (
	ActionProductCreate     = "product.create"
	ActionProductUpdate     = "product.update"
	ActionProductActivate   = "product.activate"
	ActionProductDeactivate = "product.deactivate"
	ActionProductDelete     = "product.delete"
	ActionProductCategory   = "product.category"
	ActionProductImages     = "product.images"
	ActionVariantCreate     = "variant.create"
	ActionVariantUpdate     = "variant.update"
	ActionVariantDelete     = "variant.delete"
	ActionStockUpdate       = "stock.update"
	ActionCategoryCreate    = "category.create"
	ActionCategoryUpdate    = "category.update"
	ActionCategoryDelete    = "category.delete"
	ActionPointCreate       = "point.create"
	ActionPointUpdate       = "point.update"
	ActionPointActivate     = "point.activate"
	ActionPointDeactivate   = "point.deactivate"
	ActionStaffCreate       = "staff.create"
	ActionStaffActivate     = "staff.activate"
	ActionStaffDeactivate   = "staff.deactivate"
	ActionStaffPassword     = "staff.password_reset"
	ActionOrderStatus       = "order.status"
)

// Entry is one journal line. Summary is the human-readable Russian line
// shown in the journal; Details holds structured data (typically
// {"field": {"from": x, "to": y}}) and is stored as JSONB.
type Entry struct {
	Action     string
	EntityType string
	EntityID   string
	Summary    string
	Details    map[string]any
}

// Change is the conventional Details value for one changed field.
type Change struct {
	From any `json:"from"`
	To   any `json:"to"`
}

// Log writes and reads the journal. A nil *Log is valid and does nothing
// (handlers and stores built in tests without a journal keep working).
type Log struct {
	db *sql.DB
}

// New returns a journal backed by db.
func New(db *sql.DB) *Log { return &Log{db: db} }

// insertSQL stamps clock_timestamp(), not now(): several entries written
// in one transaction keep their real order in the journal.
const insertSQL = `
	INSERT INTO audit_log (at, staff_id, action, entity_type, entity_id, summary, details, ip)
	VALUES (clock_timestamp(), $1, $2, $3, $4, $5, $6, $7)`

// insertArgs resolves the acting staff member (staff.FromContext, set by
// the admin auth gates) and client IP (httpmw.ClientIP) from ctx.
func insertArgs(ctx context.Context, e Entry) []any {
	var staffID, ip any
	if st, ok := staff.FromContext(ctx); ok && st != nil && st.ID != "" {
		staffID = st.ID
	}
	if v := httpmw.ClientIPFromContext(ctx); v != "" {
		ip = v
	}
	details := "{}"
	if len(e.Details) > 0 {
		if b, err := json.Marshal(e.Details); err == nil {
			details = string(b)
		}
	}
	return []any{staffID, e.Action, e.EntityType, e.EntityID, e.Summary, details, ip}
}

// Record writes entries outside any transaction — for actions that don't
// run in one (they have already succeeded by the time this is called).
// Errors are logged, never returned.
func (l *Log) Record(ctx context.Context, entries ...Entry) {
	if l == nil || l.db == nil {
		return
	}
	for _, e := range entries {
		if _, err := l.db.ExecContext(ctx, insertSQL, insertArgs(ctx, e)...); err != nil {
			slog.ErrorContext(ctx, "audit: write failed", "action", e.Action, "entity_id", e.EntityID, "err", err)
		}
	}
}

// savepoint isolates the journal inserts inside the caller's transaction.
const savepoint = "audit_log_write"

// RecordTx writes entries inside tx, so they commit (or roll back)
// together with the action itself. A failing insert is rolled back to a
// savepoint and logged; tx stays usable and the caller's action proceeds.
func (l *Log) RecordTx(ctx context.Context, tx *sql.Tx, entries ...Entry) {
	if len(entries) == 0 {
		return
	}
	l.RecordTxFunc(ctx, tx, func() ([]Entry, error) { return entries, nil })
}

// RecordTxFunc is RecordTx for entries that need extra reads to build
// (names for the summary, etc.): build runs inside the same savepoint as
// the inserts, so a failing lookup can't abort tx either.
func (l *Log) RecordTxFunc(ctx context.Context, tx *sql.Tx, build func() ([]Entry, error)) {
	l.guard(ctx, tx, func() error {
		entries, err := build()
		if err != nil {
			return err
		}
		for _, e := range entries {
			if _, err := tx.ExecContext(ctx, insertSQL, insertArgs(ctx, e)...); err != nil {
				return err
			}
		}
		return nil
	})
}

// SnapshotTx runs read (a "before" read that only feeds the journal)
// inside a savepoint of tx and reports whether it succeeded — on failure
// it is logged and rolled back without aborting tx. With a disabled
// journal read is not run at all.
func (l *Log) SnapshotTx(ctx context.Context, tx *sql.Tx, read func() error) bool {
	return l.guard(ctx, tx, read)
}

// guard runs fn between SAVEPOINT and RELEASE; an error from fn (or from
// the savepoint statements) is logged and rolled back to the savepoint.
func (l *Log) guard(ctx context.Context, tx *sql.Tx, fn func() error) bool {
	if l == nil || tx == nil {
		return false
	}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT "+savepoint); err != nil {
		slog.ErrorContext(ctx, "audit: savepoint failed", "err", err)
		return false
	}
	if err := fn(); err != nil {
		slog.ErrorContext(ctx, "audit: journal write failed", "err", err)
		if _, rbErr := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+savepoint); rbErr != nil {
			slog.ErrorContext(ctx, "audit: rollback to savepoint failed", "err", rbErr)
		}
		return false
	}
	if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT "+savepoint); err != nil {
		slog.ErrorContext(ctx, "audit: release savepoint failed", "err", err)
		return false
	}
	return true
}

// Enabled reports whether l actually writes anything — callers use it to
// skip extra "before" reads that only feed the journal.
func (l *Log) Enabled() bool { return l != nil && l.db != nil }
