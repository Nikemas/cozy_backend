package catalog

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// CountByProductIDs returns the number of variants per product, keyed by
// product_id — products with zero variants are simply absent from the map.
// Used by the admin products list (internal/admin) to show a "N вариаций"
// column without an N+1 query per row, mirroring the batch-by-IDs shape of
// ImageRepo.PrimaryForProducts.
func (r *VariantRepo) CountByProductIDs(ctx context.Context, productIDs []string) (map[string]int, error) {
	if len(productIDs) == 0 {
		return map[string]int{}, nil
	}

	const q = `
		SELECT product_id, COUNT(*)
		FROM product_variants
		WHERE product_id = ANY($1)
		GROUP BY product_id`

	rows, err := r.db.QueryContext(ctx, q, productIDs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]int, len(productIDs))
	for rows.Next() {
		var productID string
		var count int
		if err := rows.Scan(&productID, &count); err != nil {
			return nil, err
		}
		out[productID] = count
	}
	return out, rows.Err()
}

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
	defer func() { _ = rows.Close() }()

	variants := []Variant{}
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

// GetByID returns a single variant. Returns apperr.NotFound if it doesn't
// exist.
func (r *VariantRepo) GetByID(ctx context.Context, id string) (*Variant, error) {
	const q = `
		SELECT id, product_id, size, color, sku, price_override
		FROM product_variants
		WHERE id = $1`

	var v Variant
	err := r.db.QueryRowContext(ctx, q, id).Scan(&v.ID, &v.ProductID, &v.Size, &v.Color, &v.SKU, &v.PriceOverride)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.NotFound("variant_not_found", "вариация не найдена")
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// VariantInput carries the writable fields of a variant, shared by Create
// and Update.
type VariantInput struct {
	Size          string
	Color         string
	SKU           *string
	PriceOverride *float64
}

func (in VariantInput) validate() error {
	if strings.TrimSpace(in.Size) == "" {
		return apperr.BadRequest("invalid_size", "size обязателен")
	}
	if strings.TrimSpace(in.Color) == "" {
		return apperr.BadRequest("invalid_color", "color обязателен")
	}
	if in.PriceOverride != nil && *in.PriceOverride < 0 {
		return apperr.BadRequest("invalid_price_override", "price_override не может быть отрицательным")
	}
	return nil
}

// Create inserts a new variant under productID and returns the row as
// stored. Returns apperr.BadRequest if productID doesn't reference an
// existing product, and apperr.Conflict if it collides with an existing
// (product_id, size, color) or a taken sku.
func (r *VariantRepo) Create(ctx context.Context, productID string, in VariantInput) (*Variant, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}

	const q = `
		INSERT INTO product_variants (product_id, size, color, sku, price_override)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, product_id, size, color, sku, price_override`

	var v Variant
	err := r.db.QueryRowContext(ctx, q, productID, in.Size, in.Color, in.SKU, in.PriceOverride).
		Scan(&v.ID, &v.ProductID, &v.Size, &v.Color, &v.SKU, &v.PriceOverride)
	if err != nil {
		return nil, translateVariantWriteErr(err)
	}
	return &v, nil
}

// Update replaces every writable field of the variant with the given id.
// Returns apperr.NotFound if no such variant exists.
func (r *VariantRepo) Update(ctx context.Context, id string, in VariantInput) (*Variant, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}

	const q = `
		UPDATE product_variants
		SET size = $2, color = $3, sku = $4, price_override = $5
		WHERE id = $1
		RETURNING id, product_id, size, color, sku, price_override`

	var v Variant
	err := r.db.QueryRowContext(ctx, q, id, in.Size, in.Color, in.SKU, in.PriceOverride).
		Scan(&v.ID, &v.ProductID, &v.Size, &v.Color, &v.SKU, &v.PriceOverride)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.NotFound("variant_not_found", "вариация не найдена")
	}
	if err != nil {
		return nil, translateVariantWriteErr(err)
	}
	return &v, nil
}

// Delete hard-deletes a variant. Unlike products, product_variants has no
// is_active column to soft-delete through, and adding one was out of scope
// for this wave's API surface — so a variant that was never ordered deletes
// cleanly (its stock rows cascade via ON DELETE CASCADE), while one
// referenced by order_items.variant_id (which has no ON DELETE clause, by
// design — that's the actual order history) is protected by Postgres's own
// foreign key and surfaces as a 409 instead of succeeding or 500ing. The
// trade-off: admin can't delete a variant that has ever been ordered, only
// stop restocking it (set its stock to 0) — acceptable for this wave; a
// proper soft-delete would need a migration adding is_active here too.
func (r *VariantRepo) Delete(ctx context.Context, id string) error {
	const q = `DELETE FROM product_variants WHERE id = $1`

	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		if pgErrCode(err) == pgForeignKeyViolation {
			return apperr.Conflict("variant_in_use", "нельзя удалить вариацию: по ней есть заказы")
		}
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return apperr.NotFound("variant_not_found", "вариация не найдена")
	}
	return nil
}

// translateVariantWriteErr maps Postgres constraint violations from
// Create/Update into apperr responses.
func translateVariantWriteErr(err error) error {
	switch pgErrCode(err) {
	case pgUniqueViolation:
		return apperr.Conflict("variant_exists", "такая вариация (размер/цвет) или SKU уже существует")
	case pgForeignKeyViolation:
		return apperr.BadRequest("invalid_product_id", "товар не найден")
	default:
		return err
	}
}
