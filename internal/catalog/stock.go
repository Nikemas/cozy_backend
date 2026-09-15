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

// TotalByProductIDs returns the sum of stock quantity (across every point
// and every variant) per product, keyed by product_id — products with no
// stock rows at all are simply absent from the map (treat as 0). Used by
// the admin products list (internal/admin) for the "Остаток" stock-level
// chip without an N+1 query per row, mirroring the batch-by-IDs shape of
// ImageRepo.PrimaryForProducts/VariantRepo.CountByProductIDs.
func (r *StockRepo) TotalByProductIDs(ctx context.Context, productIDs []string) (map[string]int, error) {
	if len(productIDs) == 0 {
		return map[string]int{}, nil
	}

	const q = `
		SELECT pv.product_id, COALESCE(SUM(s.quantity), 0)
		FROM product_variants pv
		LEFT JOIN stock s ON s.variant_id = pv.id
		WHERE pv.product_id = ANY($1)
		GROUP BY pv.product_id`

	rows, err := r.db.QueryContext(ctx, q, productIDs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]int, len(productIDs))
	for rows.Next() {
		var productID string
		var total int
		if err := rows.Scan(&productID, &total); err != nil {
			return nil, err
		}
		out[productID] = total
	}
	return out, rows.Err()
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
