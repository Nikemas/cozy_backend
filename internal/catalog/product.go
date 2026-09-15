package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// Product mirrors the `products` table.
type Product struct {
	ID            string    `json:"id"`
	CategoryID    string    `json:"category_id"`
	NameRu        string    `json:"name_ru"`
	NameKy        string    `json:"name_ky"`
	DescriptionRu *string   `json:"description_ru,omitempty"`
	DescriptionKy *string   `json:"description_ky,omitempty"`
	Brand         *string   `json:"brand,omitempty"`
	BasePrice     float64   `json:"base_price"`
	IsActive      bool      `json:"is_active"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Sort values accepted by ListFilter.Sort.
const (
	SortNewest    = "newest"
	SortPriceAsc  = "price_asc"
	SortPriceDesc = "price_desc"
)

const DefaultPageSize = 20

// ListFilter carries the query params accepted by GET /api/v1/products
// (§6/§7 of the ТЗ). CategoryID must already be resolved to a UUID — see
// CategoryRepo.ResolveID for the id-or-slug `category` param.
type ListFilter struct {
	CategoryID string
	Size       string
	Color      string
	PriceMin   *float64
	PriceMax   *float64
	Query      string
	Sort       string
	Page       int // 1-based
	PageSize   int
}

type ProductRepo struct {
	db *sql.DB
}

func NewProductRepo(db *sql.DB) *ProductRepo {
	return &ProductRepo{db: db}
}

// List returns active products matching filter, plus the total count of
// matching rows (ignoring pagination) for building pagination info.
func (r *ProductRepo) List(ctx context.Context, filter ListFilter) ([]Product, int, error) {
	page := filter.Page
	if page < 1 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}

	conditions := []string{"is_active = true"}
	var args []any

	if filter.CategoryID != "" {
		args = append(args, filter.CategoryID)
		conditions = append(conditions, fmt.Sprintf("category_id = $%d", len(args)))
	}
	if filter.PriceMin != nil {
		args = append(args, *filter.PriceMin)
		conditions = append(conditions, fmt.Sprintf("base_price >= $%d", len(args)))
	}
	if filter.PriceMax != nil {
		args = append(args, *filter.PriceMax)
		conditions = append(conditions, fmt.Sprintf("base_price <= $%d", len(args)))
	}
	if filter.Query != "" {
		args = append(args, "%"+filter.Query+"%")
		idx := len(args)
		conditions = append(conditions, fmt.Sprintf("(name_ru ILIKE $%d OR name_ky ILIKE $%d)", idx, idx))
	}
	if filter.Size != "" || filter.Color != "" {
		var variantConds []string
		variantConds = append(variantConds, "product_variants.product_id = products.id")
		if filter.Size != "" {
			args = append(args, filter.Size)
			variantConds = append(variantConds, fmt.Sprintf("size = $%d", len(args)))
		}
		if filter.Color != "" {
			args = append(args, filter.Color)
			variantConds = append(variantConds, fmt.Sprintf("color = $%d", len(args)))
		}
		conditions = append(conditions, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM product_variants WHERE %s)", strings.Join(variantConds, " AND "),
		))
	}

	where := "WHERE " + strings.Join(conditions, " AND ")

	var total int
	countQuery := "SELECT COUNT(*) FROM products " + where
	if err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	orderBy := "created_at DESC"
	switch filter.Sort {
	case SortPriceAsc:
		orderBy = "base_price ASC"
	case SortPriceDesc:
		orderBy = "base_price DESC"
	case SortNewest, "":
		orderBy = "created_at DESC"
	}

	limitArgs := append(append([]any{}, args...), pageSize, safeOffset(page, pageSize))
	listQuery := fmt.Sprintf(`
		SELECT id, category_id, name_ru, name_ky, description_ru, description_ky,
		       brand, base_price, is_active, created_at, updated_at
		FROM products
		%s
		ORDER BY %s
		LIMIT $%d OFFSET $%d`, where, orderBy, len(args)+1, len(args)+2)

	rows, err := r.db.QueryContext(ctx, listQuery, limitArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	products := []Product{}
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.CategoryID, &p.NameRu, &p.NameKy, &p.DescriptionRu, &p.DescriptionKy,
			&p.Brand, &p.BasePrice, &p.IsActive, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, 0, err
		}
		products = append(products, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	return products, total, nil
}

// AdminListFilter carries the query params accepted by the admin products
// list (internal/admin) — unlike ListFilter/List, ListForAdmin never
// filters on is_active, so a deactivated product stays visible/manageable
// for staff. CategoryIDs is a pre-resolved set of category ids (a leaf
// subcategory id, or a top-level category id plus every one of its
// children) — internal/admin resolves the slug-based chip filter into ids
// via CategoryRepo.Tree before calling this, so this package doesn't need
// to know about the tree shape itself.
type AdminListFilter struct {
	CategoryIDs []string
	Query       string
	Page        int // 1-based
	PageSize    int
}

// buildAdminListConditions turns filter into SQL WHERE fragments and their
// positional args — split out from ListForAdmin as a pure function so the
// query-building logic can be unit tested without a database.
func buildAdminListConditions(filter AdminListFilter) ([]string, []any) {
	var conditions []string
	var args []any

	if len(filter.CategoryIDs) > 0 {
		args = append(args, filter.CategoryIDs)
		conditions = append(conditions, fmt.Sprintf("category_id = ANY($%d)", len(args)))
	}
	if filter.Query != "" {
		args = append(args, "%"+filter.Query+"%")
		idx := len(args)
		conditions = append(conditions, fmt.Sprintf("(name_ru ILIKE $%d OR name_ky ILIKE $%d)", idx, idx))
	}

	return conditions, args
}

// ListForAdmin returns every product matching filter (active or not) plus
// the total count of matching rows (ignoring pagination), for the admin
// products screen (internal/admin) — the admin-facing sibling of List,
// which is public-read-only and always restricted to is_active = true.
func (r *ProductRepo) ListForAdmin(ctx context.Context, filter AdminListFilter) ([]Product, int, error) {
	page := filter.Page
	if page < 1 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}

	conditions, args := buildAdminListConditions(filter)
	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	var total int
	countQuery := "SELECT COUNT(*) FROM products " + where
	if err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limitArgs := append(append([]any{}, args...), pageSize, safeOffset(page, pageSize))
	listQuery := fmt.Sprintf(`
		SELECT id, category_id, name_ru, name_ky, description_ru, description_ky,
		       brand, base_price, is_active, created_at, updated_at
		FROM products
		%s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d`, where, len(args)+1, len(args)+2)

	rows, err := r.db.QueryContext(ctx, listQuery, limitArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	products := []Product{}
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.CategoryID, &p.NameRu, &p.NameKy, &p.DescriptionRu, &p.DescriptionKy,
			&p.Brand, &p.BasePrice, &p.IsActive, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, 0, err
		}
		products = append(products, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	return products, total, nil
}

// safeOffset computes the SQL OFFSET for a page/pageSize pair without
// overflowing when page is adversarially large (e.g. a `?page=` query
// param near math.MaxInt): (page-1)*pageSize would otherwise wrap around to
// an arbitrary, possibly negative, int that Postgres rejects with "OFFSET
// must not be negative". No real catalog has anywhere near
// math.MaxInt32/pageSize pages, so clamping page at that ceiling still
// yields the same practical result (zero rows) as a literal huge offset
// would, without risking overflow.
func safeOffset(page, pageSize int) int {
	if page <= 1 {
		return 0
	}
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}
	if maxPage := math.MaxInt32 / pageSize; page > maxPage {
		page = maxPage
	}
	return (page - 1) * pageSize
}

// GetByID returns a single active product. Returns apperr.NotFound if it
// doesn't exist or is inactive — an inactive product looks the same as a
// missing one to public read endpoints.
func (r *ProductRepo) GetByID(ctx context.Context, id string) (*Product, error) {
	const q = `
		SELECT id, category_id, name_ru, name_ky, description_ru, description_ky,
		       brand, base_price, is_active, created_at, updated_at
		FROM products
		WHERE id = $1 AND is_active = true`

	var p Product
	err := r.db.QueryRowContext(ctx, q, id).Scan(&p.ID, &p.CategoryID, &p.NameRu, &p.NameKy, &p.DescriptionRu,
		&p.DescriptionKy, &p.Brand, &p.BasePrice, &p.IsActive, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.NotFound("product_not_found", "товар не найден")
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetByIDAny returns a product by id regardless of is_active, for admin use
// — unlike GetByID, a soft-deleted (is_active=false) product must still be
// resolvable so it can be edited, reactivated, or have its variants/images
// managed. Returns apperr.NotFound if no such product exists at all.
func (r *ProductRepo) GetByIDAny(ctx context.Context, id string) (*Product, error) {
	const q = `
		SELECT id, category_id, name_ru, name_ky, description_ru, description_ky,
		       brand, base_price, is_active, created_at, updated_at
		FROM products
		WHERE id = $1`

	var p Product
	err := r.db.QueryRowContext(ctx, q, id).Scan(&p.ID, &p.CategoryID, &p.NameRu, &p.NameKy, &p.DescriptionRu,
		&p.DescriptionKy, &p.Brand, &p.BasePrice, &p.IsActive, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.NotFound("product_not_found", "товар не найден")
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ProductInput carries the writable fields of a product, shared by Create
// and Update.
type ProductInput struct {
	CategoryID    string
	NameRu        string
	NameKy        string
	DescriptionRu *string
	DescriptionKy *string
	Brand         *string
	BasePrice     float64
	IsActive      bool
}

func (in ProductInput) validate() error {
	if strings.TrimSpace(in.CategoryID) == "" {
		return apperr.BadRequest("invalid_category_id", "category_id обязателен")
	}
	if strings.TrimSpace(in.NameRu) == "" {
		return apperr.BadRequest("invalid_name_ru", "name_ru обязателен")
	}
	if strings.TrimSpace(in.NameKy) == "" {
		return apperr.BadRequest("invalid_name_ky", "name_ky обязателен")
	}
	if in.BasePrice < 0 {
		return apperr.BadRequest("invalid_base_price", "base_price не может быть отрицательным")
	}
	return nil
}

// Create inserts a new product and returns the row as stored.
func (r *ProductRepo) Create(ctx context.Context, in ProductInput) (*Product, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}

	const q = `
		INSERT INTO products (category_id, name_ru, name_ky, description_ru, description_ky, brand, base_price, is_active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, category_id, name_ru, name_ky, description_ru, description_ky,
		          brand, base_price, is_active, created_at, updated_at`

	var p Product
	err := r.db.QueryRowContext(ctx, q, in.CategoryID, in.NameRu, in.NameKy, in.DescriptionRu, in.DescriptionKy,
		in.Brand, in.BasePrice, in.IsActive).
		Scan(&p.ID, &p.CategoryID, &p.NameRu, &p.NameKy, &p.DescriptionRu, &p.DescriptionKy,
			&p.Brand, &p.BasePrice, &p.IsActive, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, translateProductWriteErr(err)
	}
	return &p, nil
}

// Update replaces every writable field of the product with the given id,
// including is_active — so Update can also be used to reactivate a
// previously soft-deleted product. Returns apperr.NotFound if no such
// product exists.
func (r *ProductRepo) Update(ctx context.Context, id string, in ProductInput) (*Product, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}

	const q = `
		UPDATE products
		SET category_id = $2, name_ru = $3, name_ky = $4, description_ru = $5, description_ky = $6,
		    brand = $7, base_price = $8, is_active = $9, updated_at = now()
		WHERE id = $1
		RETURNING id, category_id, name_ru, name_ky, description_ru, description_ky,
		          brand, base_price, is_active, created_at, updated_at`

	var p Product
	err := r.db.QueryRowContext(ctx, q, id, in.CategoryID, in.NameRu, in.NameKy, in.DescriptionRu, in.DescriptionKy,
		in.Brand, in.BasePrice, in.IsActive).
		Scan(&p.ID, &p.CategoryID, &p.NameRu, &p.NameKy, &p.DescriptionRu, &p.DescriptionKy,
			&p.Brand, &p.BasePrice, &p.IsActive, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.NotFound("product_not_found", "товар не найден")
	}
	if err != nil {
		return nil, translateProductWriteErr(err)
	}
	return &p, nil
}

// Delete soft-deletes a product by setting is_active = false, rather than a
// real DELETE FROM: order_items.product_name_snapshot (and similar
// snapshot columns) denormalize the product's details at order time, but
// order_items.variant_id still has a live foreign key into
// product_variants -> products, so a hard delete of a previously-ordered
// product would either be blocked by that FK or (if variants were also
// deleted) destroy history a past order still needs to reference. Setting
// is_active = false reuses the column public reads already filter on, so a
// soft-deleted product simply stops appearing in the public catalog and
// admin can restore it later via Update. Idempotent: deleting an
// already-inactive product still succeeds.
func (r *ProductRepo) Delete(ctx context.Context, id string) error {
	const q = `UPDATE products SET is_active = false, updated_at = now() WHERE id = $1`

	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return apperr.NotFound("product_not_found", "товар не найден")
	}
	return nil
}

// translateProductWriteErr maps Postgres constraint violations from
// Create/Update into apperr responses. category_id is the only foreign key
// on products, so a violation there means the given category doesn't exist.
func translateProductWriteErr(err error) error {
	if pgErrCode(err) == pgForeignKeyViolation {
		return apperr.BadRequest("invalid_category_id", "категория не найдена")
	}
	return err
}
