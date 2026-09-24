package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/audit"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/dbtx"
)

// stockConflictError: the stored quantity no longer equals the client's
// expected_quantity. Current is what is stored now (0 if no row).
type stockConflictError struct {
	Current int
}

func (e *stockConflictError) Error() string {
	return fmt.Sprintf("stock changed concurrently (now %d)", e.Current)
}

// adminStockStore backs PUT /admin/api/stock/{variantId}/{pointId}: one
// transaction that locks the cell, checks expected_quantity (when given),
// writes the new quantity and journals old → new (internal/audit).
type adminStockStore struct {
	db    *sql.DB
	audit *audit.Log // nil = no journal
}

func stockPgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func translateStockErr(err error) error {
	switch stockPgCode(err) {
	case "23503", "22P02": // foreign key violation, invalid uuid text
		return apperr.BadRequest("invalid_variant_or_point", "вариация или точка продаж не найдена")
	}
	return err
}

const stockReturning = ` RETURNING variant_id, point_id, quantity, updated_at`

// Set writes quantity for variantID at pointID. With expected != nil the
// write only happens if the stored quantity (0 when there is no stock row
// yet) equals *expected; otherwise *stockConflictError.
func (s *adminStockStore) Set(ctx context.Context, variantID, pointID string, quantity int, expected *int) (*catalog.StockEntry, error) {
	if quantity < 0 {
		return nil, apperr.BadRequest("invalid_quantity", "quantity не может быть отрицательным")
	}
	if expected != nil && *expected < 0 {
		return nil, apperr.BadRequest("invalid_expected_quantity", "expected_quantity не может быть отрицательным")
	}

	var entry catalog.StockEntry
	err := dbtx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		prev, exists := 0, true
		err := tx.QueryRowContext(ctx,
			`SELECT quantity FROM stock WHERE variant_id = $1 AND point_id = $2 FOR UPDATE`, variantID, pointID).Scan(&prev)
		if errors.Is(err, sql.ErrNoRows) {
			exists = false
		} else if err != nil {
			return translateStockErr(err)
		}
		if expected != nil && *expected != prev {
			return &stockConflictError{Current: prev}
		}

		scan := func(row *sql.Row) error {
			return row.Scan(&entry.VariantID, &entry.PointID, &entry.Quantity, &entry.UpdatedAt)
		}
		switch {
		case exists:
			err = scan(tx.QueryRowContext(ctx, `
				UPDATE stock SET quantity = $3, updated_at = now()
				WHERE variant_id = $1 AND point_id = $2`+stockReturning, variantID, pointID, quantity))
		case expected != nil:
			// No row yet and the client expects 0: insert only if nobody
			// else inserted one in the meantime.
			err = scan(tx.QueryRowContext(ctx, `
				INSERT INTO stock (variant_id, point_id, quantity, updated_at) VALUES ($1, $2, $3, now())
				ON CONFLICT (variant_id, point_id) DO NOTHING`+stockReturning, variantID, pointID, quantity))
			if errors.Is(err, sql.ErrNoRows) {
				var current int
				if err := tx.QueryRowContext(ctx,
					`SELECT quantity FROM stock WHERE variant_id = $1 AND point_id = $2`, variantID, pointID).Scan(&current); err != nil {
					return err
				}
				return &stockConflictError{Current: current}
			}
		default:
			err = scan(tx.QueryRowContext(ctx, `
				INSERT INTO stock (variant_id, point_id, quantity, updated_at) VALUES ($1, $2, $3, now())
				ON CONFLICT (variant_id, point_id) DO UPDATE SET quantity = EXCLUDED.quantity, updated_at = now()`+stockReturning,
				variantID, pointID, quantity))
		}
		if err != nil {
			return translateStockErr(err)
		}

		if prev != quantity || !exists {
			s.audit.RecordTxFunc(ctx, tx, func() ([]audit.Entry, error) {
				return stockJournalEntry(ctx, tx, variantID, pointID, prev, quantity)
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &entry, nil
}

// stockJournalEntry names the cell for the journal line.
func stockJournalEntry(ctx context.Context, tx *sql.Tx, variantID, pointID string, from, to int) ([]audit.Entry, error) {
	var productID, product, size, color, point string
	err := tx.QueryRowContext(ctx, `
		SELECT p.id, p.name_ru, v.size, v.color, pos.name
		FROM product_variants v
		JOIN products p ON p.id = v.product_id
		JOIN points_of_sale pos ON pos.id = $2
		WHERE v.id = $1`, variantID, pointID).Scan(&productID, &product, &size, &color, &point)
	if err != nil {
		return nil, err
	}
	return []audit.Entry{{
		Action: audit.ActionStockUpdate, EntityType: audit.EntityStock, EntityID: variantID,
		Summary: fmt.Sprintf("Остаток «%s» %s / %s, %s: %d → %d (API)", product, size, color, point, from, to),
		Details: map[string]any{"product_id": productID, "point_id": pointID, "point": point,
			"quantity": audit.Change{From: from, To: to}},
	}}, nil
}

// --- journal entries for the other catalog endpoints ---

func categoryEntry(action, id, summary string, req *categoryRequest) audit.Entry {
	e := audit.Entry{Action: action, EntityType: audit.EntityCategory, EntityID: id, Summary: summary}
	if req != nil {
		parent := ""
		if req.ParentID != nil {
			parent = *req.ParentID
		}
		e.Details = map[string]any{"name_ru": req.NameRu, "name_ky": req.NameKy, "slug": req.Slug,
			"parent_id": parent, "sort_order": req.SortOrder}
	}
	return e
}

func productEntry(action string, p *catalog.Product, summary string) audit.Entry {
	brand := ""
	if p.Brand != nil {
		brand = *p.Brand
	}
	return audit.Entry{Action: action, EntityType: audit.EntityProduct, EntityID: p.ID, Summary: summary,
		Details: map[string]any{"name_ru": p.NameRu, "category_id": p.CategoryID, "base_price": p.BasePrice,
			"brand": brand, "is_active": p.IsActive}}
}

func variantEntry(action, productID, variantID, summary string, req *variantRequest) audit.Entry {
	d := map[string]any{"product_id": productID}
	if req != nil {
		d["size"], d["color"] = req.Size, req.Color
		if req.SKU != nil {
			d["sku"] = *req.SKU
		}
		if req.PriceOverride != nil {
			d["price_override"] = *req.PriceOverride
		}
	}
	return audit.Entry{Action: action, EntityType: audit.EntityVariant, EntityID: variantID, Summary: summary, Details: d}
}
