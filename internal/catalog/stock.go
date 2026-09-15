package catalog

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// StockEntry mirrors one row of the `stock` table: the quantity of a
// specific variant available at a specific point of sale.
type StockEntry struct {
	VariantID string    `json:"variant_id"`
	PointID   string    `json:"point_id"`
	Quantity  int       `json:"quantity"`
	UpdatedAt time.Time `json:"updated_at"`
}

type StockRepo struct {
	db *sql.DB
}

func NewStockRepo(db *sql.DB) *StockRepo {
	return &StockRepo{db: db}
}

// ByVariantIDs returns every stock row (quantity per point) for the given
// variant IDs. Returns an empty result without querying when variantIDs is
// empty, rather than issuing a query that would error on an empty IN ().
func (r *StockRepo) ByVariantIDs(ctx context.Context, variantIDs []string) ([]StockEntry, error) {
	if len(variantIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(variantIDs))
	args := make([]any, len(variantIDs))
	for i, id := range variantIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}

	q := fmt.Sprintf(`
		SELECT variant_id, point_id, quantity, updated_at
		FROM stock
		WHERE variant_id IN (%s)`, strings.Join(placeholders, ", "))

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	entries := []StockEntry{}
	for rows.Next() {
		var e StockEntry
		if err := rows.Scan(&e.VariantID, &e.PointID, &e.Quantity, &e.UpdatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// Upsert sets the quantity of variantID at pointID, inserting the row if
// one doesn't exist yet (a variant has no stock row at a point until
// someone sets one there) or updating it in place otherwise. Returns
// apperr.BadRequest for a negative quantity, or if variantID/pointID
// doesn't reference an existing variant/point of sale.
func (r *StockRepo) Upsert(ctx context.Context, variantID, pointID string, quantity int) (*StockEntry, error) {
	if quantity < 0 {
		return nil, apperr.BadRequest("invalid_quantity", "quantity не может быть отрицательным")
	}

	const q = `
		INSERT INTO stock (variant_id, point_id, quantity, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (variant_id, point_id) DO UPDATE SET quantity = EXCLUDED.quantity, updated_at = now()
		RETURNING variant_id, point_id, quantity, updated_at`

	var e StockEntry
	err := r.db.QueryRowContext(ctx, q, variantID, pointID, quantity).
		Scan(&e.VariantID, &e.PointID, &e.Quantity, &e.UpdatedAt)
	if pgErrCode(err) == pgForeignKeyViolation {
		return nil, apperr.BadRequest("invalid_variant_or_point", "вариация или точка продаж не найдена")
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}
