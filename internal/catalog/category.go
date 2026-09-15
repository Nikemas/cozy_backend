// Package catalog holds the shoe-catalog domain: categories, products,
// variants and per-point stock. This wave only implements public,
// read-only access — admin CRUD lands later, gated by RBAC from the staff
// auth stream.
package catalog

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// Category mirrors the `categories` table. Children is populated only by
// Tree, not by row scans.
type Category struct {
	ID        string      `json:"id"`
	ParentID  *string     `json:"parent_id,omitempty"`
	NameRu    string      `json:"name_ru"`
	NameKy    string      `json:"name_ky"`
	Slug      string      `json:"slug"`
	SortOrder int         `json:"sort_order"`
	Children  []*Category `json:"children,omitempty"`
}

type CategoryRepo struct {
	db *sql.DB
}

func NewCategoryRepo(db *sql.DB) *CategoryRepo {
	return &CategoryRepo{db: db}
}

// Tree returns all categories arranged into a forest by parent_id. Built in
// Go from a flat, sort_order-ordered SELECT rather than a recursive CTE.
func (r *CategoryRepo) Tree(ctx context.Context) ([]*Category, error) {
	const q = `
		SELECT id, parent_id, name_ru, name_ky, slug, sort_order
		FROM categories
		ORDER BY sort_order`

	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var flat []*Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.ParentID, &c.NameRu, &c.NameKy, &c.Slug, &c.SortOrder); err != nil {
			return nil, err
		}
		flat = append(flat, &c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return buildTree(flat), nil
}

// buildTree arranges a flat, sort_order-ordered list of categories into a
// forest based on ParentID. A pure function so the tree-building logic can
// be unit tested without a database.
func buildTree(flat []*Category) []*Category {
	byID := make(map[string]*Category, len(flat))
	for _, c := range flat {
		byID[c.ID] = c
	}

	roots := make([]*Category, 0, len(flat))
	for _, c := range flat {
		if c.ParentID == nil {
			roots = append(roots, c)
			continue
		}
		parent, ok := byID[*c.ParentID]
		if !ok {
			// Dangling parent_id shouldn't happen given the FK constraint,
			// but surface the category rather than silently dropping it.
			roots = append(roots, c)
			continue
		}
		parent.Children = append(parent.Children, c)
	}
	return roots
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ResolveID resolves a `category` filter value that may be either the
// category's UUID or its slug into the UUID id that products.category_id
// actually stores. Returns apperr.NotFound if no such category exists.
func (r *CategoryRepo) ResolveID(ctx context.Context, idOrSlug string) (string, error) {
	if uuidPattern.MatchString(idOrSlug) {
		return idOrSlug, nil
	}

	const q = `SELECT id FROM categories WHERE slug = $1`
	var id string
	err := r.db.QueryRowContext(ctx, q, idOrSlug).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", apperr.NotFound("category_not_found", "категория не найдена")
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

// CategoryInput carries the writable fields of a category, shared by Create
// and Update. The id and slug uniqueness are enforced by the database
// (primary key / UNIQUE constraint on slug); Create/Update translate a
// violation into apperr.Conflict rather than a raw Postgres error.
type CategoryInput struct {
	ParentID  *string
	NameRu    string
	NameKy    string
	Slug      string
	SortOrder int
}

func (in CategoryInput) validate() error {
	if strings.TrimSpace(in.NameRu) == "" {
		return apperr.BadRequest("invalid_name_ru", "name_ru обязателен")
	}
	if strings.TrimSpace(in.NameKy) == "" {
		return apperr.BadRequest("invalid_name_ky", "name_ky обязателен")
	}
	if strings.TrimSpace(in.Slug) == "" {
		return apperr.BadRequest("invalid_slug", "slug обязателен")
	}
	return nil
}

// Create inserts a new category and returns the row as stored.
func (r *CategoryRepo) Create(ctx context.Context, in CategoryInput) (*Category, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}

	const q = `
		INSERT INTO categories (parent_id, name_ru, name_ky, slug, sort_order)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, parent_id, name_ru, name_ky, slug, sort_order`

	var c Category
	err := r.db.QueryRowContext(ctx, q, in.ParentID, in.NameRu, in.NameKy, in.Slug, in.SortOrder).
		Scan(&c.ID, &c.ParentID, &c.NameRu, &c.NameKy, &c.Slug, &c.SortOrder)
	if err != nil {
		return nil, translateCategoryUpsertErr(err)
	}
	return &c, nil
}

// Update replaces every writable field of the category with the given id.
// Returns apperr.NotFound if no such category exists, and apperr.BadRequest
// if parentID would make the category its own parent.
func (r *CategoryRepo) Update(ctx context.Context, id string, in CategoryInput) (*Category, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	if in.ParentID != nil && *in.ParentID == id {
		return nil, apperr.BadRequest("invalid_parent_id", "категория не может быть собственным родителем")
	}

	const q = `
		UPDATE categories
		SET parent_id = $2, name_ru = $3, name_ky = $4, slug = $5, sort_order = $6
		WHERE id = $1
		RETURNING id, parent_id, name_ru, name_ky, slug, sort_order`

	var c Category
	err := r.db.QueryRowContext(ctx, q, id, in.ParentID, in.NameRu, in.NameKy, in.Slug, in.SortOrder).
		Scan(&c.ID, &c.ParentID, &c.NameRu, &c.NameKy, &c.Slug, &c.SortOrder)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.NotFound("category_not_found", "категория не найдена")
	}
	if err != nil {
		return nil, translateCategoryUpsertErr(err)
	}
	return &c, nil
}

// Delete removes a category. Categories are hard-deleted (unlike products,
// there's no is_active column to soft-delete through, and nothing snapshots
// a category the way order_items snapshots product/variant details) — but a
// category still referenced by a product (products.category_id) or by a
// subcategory (categories.parent_id) can't be removed without breaking that
// reference, so Postgres's own FK violation is translated into a 409
// instead of leaking as a raw error.
func (r *CategoryRepo) Delete(ctx context.Context, id string) error {
	const q = `DELETE FROM categories WHERE id = $1`

	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		if pgErrCode(err) == pgForeignKeyViolation {
			return apperr.Conflict("category_in_use", "нельзя удалить категорию: на неё ссылаются товары или подкатегории")
		}
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return apperr.NotFound("category_not_found", "категория не найдена")
	}
	return nil
}

// translateCategoryUpsertErr maps Postgres constraint violations from
// Create/Update into apperr responses. On insert/update the only foreign
// key a category row has is parent_id, so a violation there unambiguously
// means "that parent doesn't exist" (as opposed to Delete, where a
// violation means the opposite: other rows still point at this one).
func translateCategoryUpsertErr(err error) error {
	switch pgErrCode(err) {
	case pgUniqueViolation:
		return apperr.Conflict("category_slug_taken", "категория с таким slug уже существует")
	case pgForeignKeyViolation:
		return apperr.BadRequest("invalid_parent_id", "родительская категория не найдена")
	default:
		return err
	}
}
