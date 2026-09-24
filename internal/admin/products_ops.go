// products_ops.go (fix/admin-ops, W5): the products list's bulk actions
// (activate/deactivate, move to a category) and its per-point stock
// column. Bulk writes run in one transaction and journal one audit entry
// per product that actually changed (internal/audit).
package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/audit"
	"github.com/Nikemas/cozy_backend/internal/dbtx"
)

// maxBulkItems caps one bulk request (a page holds at most 50 rows).
const maxBulkItems = 200

// productOpsBackend is productOpsStore as an interface, so the handlers
// can be tested with a fake.
type productOpsBackend interface {
	BulkSetActive(ctx context.Context, ids []string, active bool) (int, error)
	BulkSetCategory(ctx context.Context, ids []string, categoryID string) (int, error)
	StockAtPoint(ctx context.Context, pointID string, productIDs []string) (map[string]int, error)
}

type productOpsStore struct {
	db    *sql.DB
	audit *audit.Log // nil = no journal
}

type bulkProductRow struct {
	ID         string
	Name       string
	IsActive   bool
	CategoryID string
}

func lockProductsTx(ctx context.Context, tx *sql.Tx, ids []string) ([]bulkProductRow, error) {
	const q = `SELECT id, name_ru, is_active, category_id FROM products WHERE id = ANY($1) ORDER BY id FOR UPDATE`
	rows, err := tx.QueryContext(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []bulkProductRow
	for rows.Next() {
		var p bulkProductRow
		if err := rows.Scan(&p.ID, &p.Name, &p.IsActive, &p.CategoryID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// BulkSetActive sets is_active on every product in ids and returns how
// many actually changed (already-matching products are left alone).
func (s *productOpsStore) BulkSetActive(ctx context.Context, ids []string, active bool) (int, error) {
	var changed []bulkProductRow
	err := dbtx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		locked, err := lockProductsTx(ctx, tx, ids)
		if err != nil {
			return err
		}
		var changedIDs []string
		for _, p := range locked {
			if p.IsActive != active {
				changed = append(changed, p)
				changedIDs = append(changedIDs, p.ID)
			}
		}
		if len(changedIDs) == 0 {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE products SET is_active = $2, updated_at = now() WHERE id = ANY($1)`, changedIDs, active); err != nil {
			return err
		}

		action, verb := audit.ActionProductDeactivate, "деактивирован"
		if active {
			action, verb = audit.ActionProductActivate, "активирован"
		}
		entries := make([]audit.Entry, len(changed))
		for i, p := range changed {
			entries[i] = audit.Entry{
				Action: action, EntityType: audit.EntityProduct, EntityID: p.ID,
				Summary: fmt.Sprintf("Товар «%s» %s (массово)", p.Name, verb),
				Details: map[string]any{"is_active": audit.Change{From: !active, To: active}, "bulk": true},
			}
		}
		s.audit.RecordTx(ctx, tx, entries...)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(changed), nil
}

// BulkSetCategory moves every product in ids to categoryID and returns how
// many actually moved. An unknown category is a 400.
func (s *productOpsStore) BulkSetCategory(ctx context.Context, ids []string, categoryID string) (int, error) {
	var moved int
	err := dbtx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		var categoryName string
		err := tx.QueryRowContext(ctx, `SELECT name_ru FROM categories WHERE id = $1`, categoryID).Scan(&categoryName)
		if errors.Is(err, sql.ErrNoRows) {
			return apperr.BadRequest("invalid_category_id", "категория не найдена")
		}
		if err != nil {
			return err
		}

		locked, err := lockProductsTx(ctx, tx, ids)
		if err != nil {
			return err
		}
		var changed []bulkProductRow
		var changedIDs []string
		for _, p := range locked {
			if p.CategoryID != categoryID {
				changed = append(changed, p)
				changedIDs = append(changedIDs, p.ID)
			}
		}
		if len(changedIDs) == 0 {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE products SET category_id = $2, updated_at = now() WHERE id = ANY($1)`, changedIDs, categoryID); err != nil {
			return err
		}
		moved = len(changed)

		if s.audit.Enabled() {
			s.audit.RecordTxFunc(ctx, tx, func() ([]audit.Entry, error) {
				oldIDs := make([]string, 0, len(changed))
				for _, p := range changed {
					oldIDs = append(oldIDs, p.CategoryID)
				}
				names, err := categoryNamesTx(ctx, tx, oldIDs)
				if err != nil {
					return nil, err
				}
				entries := make([]audit.Entry, len(changed))
				for i, p := range changed {
					entries[i] = audit.Entry{
						Action: audit.ActionProductCategory, EntityType: audit.EntityProduct, EntityID: p.ID,
						Summary: fmt.Sprintf("Товар «%s»: категория «%s» → «%s» (массово)", p.Name, names[p.CategoryID], categoryName),
						Details: map[string]any{"category_id": audit.Change{From: p.CategoryID, To: categoryID}, "bulk": true},
					}
				}
				return entries, nil
			})
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return moved, nil
}

func categoryNamesTx(ctx context.Context, tx *sql.Tx, ids []string) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, name_ru FROM categories WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// StockAtPoint returns each product's total stock (all variants) at
// pointID; products with no stock rows there map to 0 or are absent.
func (s *productOpsStore) StockAtPoint(ctx context.Context, pointID string, productIDs []string) (map[string]int, error) {
	out := make(map[string]int, len(productIDs))
	if len(productIDs) == 0 {
		return out, nil
	}
	const q = `
		SELECT pv.product_id, COALESCE(SUM(s.quantity), 0)
		FROM product_variants pv
		LEFT JOIN stock s ON s.variant_id = pv.id AND s.point_id = $2
		WHERE pv.product_id = ANY($1)
		GROUP BY pv.product_id`
	rows, err := s.db.QueryContext(ctx, q, productIDs, pointID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var qty int
		if err := rows.Scan(&id, &qty); err != nil {
			return nil, err
		}
		out[id] = qty
	}
	return out, rows.Err()
}
