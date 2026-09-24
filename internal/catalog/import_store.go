package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/dbtx"
)

// SQLImportStore is the Postgres ImportStore.
type SQLImportStore struct {
	db *sql.DB
}

// NewSQLImportStore returns an ImportStore backed by db.
func NewSQLImportStore(db *sql.DB) *SQLImportStore {
	return &SQLImportStore{db: db}
}

var _ ImportStore = (*SQLImportStore)(nil)

// ResolveCategory accepts a category UUID, slug, or RU/KY name (case-
// insensitive) — a customer's price list names categories, it doesn't
// know slugs. A name shared by several categories is ambiguous.
func (s *SQLImportStore) ResolveCategory(ctx context.Context, raw string) (string, error) {
	if _, err := uuid.Parse(raw); err == nil {
		var id string
		err := s.db.QueryRowContext(ctx, `SELECT id FROM categories WHERE id = $1`, raw).Scan(&id)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, slug = $1 FROM categories
		WHERE slug = $1 OR lower(name_ru) = lower($1) OR lower(name_ky) = lower($1)
		ORDER BY (slug = $1) DESC
		LIMIT 2`, raw)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	var exactSlug bool
	for rows.Next() {
		var id string
		var slugHit bool
		if err := rows.Scan(&id, &slugHit); err != nil {
			return "", err
		}
		if len(ids) == 0 {
			exactSlug = slugHit
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	switch {
	case len(ids) == 0:
		return "", apperr.BadRequest("category_not_found", fmt.Sprintf("категория %q не найдена", raw))
	case len(ids) == 1 || exactSlug:
		return ids[0], nil
	}
	return "", apperr.BadRequest("category_ambiguous",
		fmt.Sprintf("категория %q неоднозначна (несколько категорий с таким названием) — укажите slug", raw))
}

// ActivePoint reports whether id is an active point of sale.
func (s *SQLImportStore) ActivePoint(ctx context.Context, id string) (bool, error) {
	var ok bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM points_of_sale WHERE id = $1 AND is_active)`, id).Scan(&ok)
	return ok, err
}

// DefaultPoint returns the oldest active point of sale, "" if none.
func (s *SQLImportStore) DefaultPoint(ctx context.Context) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM points_of_sale WHERE is_active ORDER BY created_at, id LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// ImportPoint is an active point of sale offered as the stock target.
type ImportPoint struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
}

// ActivePoints lists active points of sale, default (oldest) first.
func (s *SQLImportStore) ActivePoints(ctx context.Context) ([]ImportPoint, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, address FROM points_of_sale WHERE is_active ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []ImportPoint{}
	for rows.Next() {
		var p ImportPoint
		if err := rows.Scan(&p.ID, &p.Name, &p.Address); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// TemplateCategories lists categories for the template's reference sheet.
func (s *SQLImportStore) TemplateCategories(ctx context.Context) ([]TemplateCategory, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT slug, name_ru, name_ky FROM categories ORDER BY sort_order, name_ru`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []TemplateCategory
	for rows.Next() {
		var c TemplateCategory
		if err := rows.Scan(&c.Slug, &c.NameRu, &c.NameKy); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Begin opens an import session. A real run gives every model its own
// transaction (dbtx.WithTx); a dry run holds one transaction that is
// always rolled back, with a savepoint per model so a failing model
// doesn't poison the ones after it.
func (s *SQLImportStore) Begin(ctx context.Context, dryRun bool) (ImportSession, error) {
	if !dryRun {
		return &sqlImportSession{db: s.db}, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &sqlImportSession{db: s.db, dry: tx}, nil
}

type sqlImportSession struct {
	db  *sql.DB
	dry *sql.Tx
}

func (s *sqlImportSession) Model(ctx context.Context, fn func(tx ImportTx) error) error {
	if s.dry == nil {
		return dbtx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
			return fn(sqlImportTx{q: tx})
		})
	}
	if _, err := s.dry.ExecContext(ctx, `SAVEPOINT import_model`); err != nil {
		return err
	}
	if err := fn(sqlImportTx{q: s.dry}); err != nil {
		if _, rbErr := s.dry.ExecContext(ctx, `ROLLBACK TO SAVEPOINT import_model`); rbErr != nil {
			return errors.Join(err, rbErr)
		}
		return err
	}
	_, err := s.dry.ExecContext(ctx, `RELEASE SAVEPOINT import_model`)
	return err
}

func (s *sqlImportSession) Close() error {
	if s.dry != nil {
		return s.dry.Rollback()
	}
	return nil
}

type sqlImportTx struct {
	q *sql.Tx
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (t sqlImportTx) ProductByModelCode(ctx context.Context, code string) (string, error) {
	var id string
	err := t.q.QueryRowContext(ctx,
		`SELECT id FROM products WHERE lower(model_code) = lower($1)`, code).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func (t sqlImportTx) ProductsByName(ctx context.Context, brand, nameRu, categoryID, modelCode string) ([]string, error) {
	rows, err := t.q.QueryContext(ctx, `
		SELECT id FROM products
		WHERE category_id = $1
		  AND lower(name_ru) = lower($2)
		  AND lower(COALESCE(brand, '')) = lower($3)
		  AND ($4 = '' OR model_code IS NULL)
		ORDER BY created_at
		LIMIT 2`, categoryID, nameRu, brand, modelCode)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (t sqlImportTx) CreateProduct(ctx context.Context, p ImportProduct) (string, error) {
	var id string
	err := t.q.QueryRowContext(ctx, `
		INSERT INTO products (category_id, name_ru, name_ky, description_ru, description_ky,
		                      brand, base_price, model_code, is_active)
		VALUES ($1, $2, COALESCE($3, $2), $4, $5, $6, $7, $8, true)
		RETURNING id`,
		p.CategoryID, p.NameRu, nullIfEmpty(p.NameKy), nullIfEmpty(p.DescriptionRu), nullIfEmpty(p.DescriptionKy),
		nullIfEmpty(p.Brand), p.BasePrice, nullIfEmpty(p.ModelCode)).Scan(&id)
	return id, err
}

// UpdateProduct overwrites what the file carries. Empty optional cells
// (KY name, brand, descriptions, article) keep the stored value rather than wiping
// text entered in the admin form; is_active is never touched.
func (t sqlImportTx) UpdateProduct(ctx context.Context, id string, p ImportProduct) error {
	_, err := t.q.ExecContext(ctx, `
		UPDATE products SET
		  category_id    = $2,
		  name_ru        = $3,
		  name_ky        = COALESCE($4, name_ky),
		  description_ru = COALESCE($5, description_ru),
		  description_ky = COALESCE($6, description_ky),
		  brand          = COALESCE($7, brand),
		  base_price     = $8,
		  model_code     = COALESCE($9, model_code),
		  updated_at     = now()
		WHERE id = $1`,
		id, p.CategoryID, p.NameRu, nullIfEmpty(p.NameKy), nullIfEmpty(p.DescriptionRu), nullIfEmpty(p.DescriptionKy),
		nullIfEmpty(p.Brand), p.BasePrice, nullIfEmpty(p.ModelCode))
	return err
}

func (t sqlImportTx) VariantBySKU(ctx context.Context, sku string) (*ImportVariantRef, error) {
	var ref ImportVariantRef
	err := t.q.QueryRowContext(ctx, `
		SELECT v.id, v.product_id, COALESCE(p.model_code, '')
		FROM product_variants v JOIN products p ON p.id = v.product_id
		WHERE v.sku = $1`, sku).Scan(&ref.ID, &ref.ProductID, &ref.ProductModelCode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ref, nil
}

// VariantBySizeColor matches size exactly and color case-insensitively
// ("Черный" and "черный" are the same variant).
func (t sqlImportTx) VariantBySizeColor(ctx context.Context, productID, size, color string) (*ImportVariantRef, error) {
	var ref ImportVariantRef
	err := t.q.QueryRowContext(ctx, `
		SELECT id, product_id FROM product_variants
		WHERE product_id = $1 AND lower(size) = lower($2) AND lower(color) = lower($3)
		ORDER BY (size = $2 AND color = $3) DESC
		LIMIT 1`, productID, size, color).Scan(&ref.ID, &ref.ProductID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ref, nil
}

func (t sqlImportTx) CreateVariant(ctx context.Context, productID string, v ImportVariant) (string, error) {
	var id string
	err := t.q.QueryRowContext(ctx, `
		INSERT INTO product_variants (product_id, size, color, sku, price_override)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id`,
		productID, v.Size, v.Color, nullIfEmpty(v.SKU), v.PriceOverride).Scan(&id)
	return id, err
}

// UpdateVariant sets size/color as written in the file and the price
// override derived from it (NULL = the product's base price). An empty SKU
// cell keeps the stored SKU.
func (t sqlImportTx) UpdateVariant(ctx context.Context, id string, v ImportVariant) error {
	_, err := t.q.ExecContext(ctx, `
		UPDATE product_variants SET
		  size           = $2,
		  color          = $3,
		  sku            = COALESCE($4, sku),
		  price_override = $5
		WHERE id = $1`,
		id, v.Size, v.Color, nullIfEmpty(v.SKU), v.PriceOverride)
	return err
}

func (t sqlImportTx) SetStock(ctx context.Context, variantID, pointID string, qty int) error {
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO stock (variant_id, point_id, quantity, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (variant_id, point_id)
		DO UPDATE SET quantity = EXCLUDED.quantity, updated_at = now()`,
		variantID, pointID, qty)
	return err
}
