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
	defer rows.Close()

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

	var roots []*Category
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
