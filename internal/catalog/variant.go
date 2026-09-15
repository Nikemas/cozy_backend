package catalog

import (
	"context"
	"database/sql"
)

// Variant mirrors the `product_variants` table.
type Variant struct {
	ID            string   `json:"id"`
	ProductID     string   `json:"product_id"`
	Size          string   `json:"size"`
	Color         string   `json:"color"`
	SKU           *string  `json:"sku,omitempty"`
	PriceOverride *float64 `json:"price_override,omitempty"`
}

type VariantRepo struct {
	db *sql.DB
}

func NewVariantRepo(db *sql.DB) *VariantRepo {
	return &VariantRepo{db: db}
}

// ListByProduct returns every variant of a product, ordered for stable
// display (size, then color).
func (r *VariantRepo) ListByProduct(ctx context.Context, productID string) ([]Variant, error) {
	const q = `
		SELECT id, product_id, size, color, sku, price_override
		FROM product_variants
		WHERE product_id = $1
		ORDER BY size, color`

	rows, err := r.db.QueryContext(ctx, q, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var variants []Variant
	for rows.Next() {
		var v Variant
		if err := rows.Scan(&v.ID, &v.ProductID, &v.Size, &v.Color, &v.SKU, &v.PriceOverride); err != nil {
			return nil, err
		}
		variants = append(variants, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return variants, nil
}
