package storefront

import (
	"context"
	"database/sql"
)

// FavoriteRepo is the read/write contract for a customer's favorited
// products (migration 000013_create_favorites: a plain
// (customer_id, product_id) join table, no extra columns). It only deals
// in product IDs — internal/catalog.ProductRepo owns fetching the actual
// product rows (name/price/photo) for the fav.gohtml grid, per this
// package's "don't own catalog data" boundary.
type FavoriteRepo struct {
	db *sql.DB
}

func NewFavoriteRepo(db *sql.DB) *FavoriteRepo {
	return &FavoriteRepo{db: db}
}

// ListProductIDs returns customerID's favorited product IDs, most
// recently favorited first.
func (r *FavoriteRepo) ListProductIDs(ctx context.Context, customerID string) ([]string, error) {
	const q = `
		SELECT product_id FROM favorites
		WHERE customer_id = $1
		ORDER BY created_at DESC`

	rows, err := r.db.QueryContext(ctx, q, customerID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

// Count returns how many products customerID has favorited — backs the
// profile screen's "избранное" stat tile and the header/aside badge.
func (r *FavoriteRepo) Count(ctx context.Context, customerID string) (int, error) {
	const q = `SELECT COUNT(*) FROM favorites WHERE customer_id = $1`
	var n int
	if err := r.db.QueryRowContext(ctx, q, customerID).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// IsFavorite reports whether customerID has favorited productID — used
// by the product/shop cards' favorite toggle (Task 2) to render the
// initial filled/unfilled heart state.
func (r *FavoriteRepo) IsFavorite(ctx context.Context, customerID, productID string) (bool, error) {
	const q = `SELECT EXISTS(SELECT 1 FROM favorites WHERE customer_id = $1 AND product_id = $2)`
	var ok bool
	if err := r.db.QueryRowContext(ctx, q, customerID, productID).Scan(&ok); err != nil {
		return false, err
	}
	return ok, nil
}

// Add favorites productID for customerID. Idempotent — favoriting an
// already-favorited product is a no-op, not an error.
func (r *FavoriteRepo) Add(ctx context.Context, customerID, productID string) error {
	const q = `
		INSERT INTO favorites (customer_id, product_id) VALUES ($1, $2)
		ON CONFLICT (customer_id, product_id) DO NOTHING`
	_, err := r.db.ExecContext(ctx, q, customerID, productID)
	return err
}

// Remove un-favorites productID for customerID. Idempotent — removing a
// product that was never favorited is a no-op, not an error, so the
// fav.gohtml "убрать из избранного" button doesn't need to check first.
func (r *FavoriteRepo) Remove(ctx context.Context, customerID, productID string) error {
	const q = `DELETE FROM favorites WHERE customer_id = $1 AND product_id = $2`
	_, err := r.db.ExecContext(ctx, q, customerID, productID)
	return err
}
