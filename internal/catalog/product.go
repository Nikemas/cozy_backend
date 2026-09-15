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
