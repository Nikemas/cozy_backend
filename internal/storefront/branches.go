package storefront

import (
	"context"
	"database/sql"
)

// Branch mirrors one row of points_of_sale (migration
// 000003_create_points_of_sale). That table only has id/name/address/
// is_active/created_at — no opening-hours or lat/lng columns — so the
// branches screen's pseudo-map pin positions and "hours" copy are a
// presentation-layer concern (internal/web), not something this read-only
// repo can source from the DB.
type Branch struct {
	ID      string
	Name    string
	Address string
}

// BranchRepo is a read-only view over points_of_sale for the customer-
// facing branches screen (COZY_WEB_DESIGN.md §3.7). It lives in
// internal/storefront alongside addresses/favorites because it's the same
// kind of customer-facing read as those, per web-plan Task 5.
type BranchRepo struct {
	db *sql.DB
}

func NewBranchRepo(db *sql.DB) *BranchRepo {
	return &BranchRepo{db: db}
}

// List returns every active point of sale, ordered by name for a stable
// display order (and stable pseudo-map pin assignment, since pins are
// assigned by list position — see internal/web's pinCoords).
func (r *BranchRepo) List(ctx context.Context) ([]Branch, error) {
	const q = `
		SELECT id, name, address
		FROM points_of_sale
		WHERE is_active
		ORDER BY name`

	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	branches := make([]Branch, 0)
	for rows.Next() {
		var b Branch
		if err := rows.Scan(&b.ID, &b.Name, &b.Address); err != nil {
			return nil, err
		}
		branches = append(branches, b)
	}
	return branches, rows.Err()
}
