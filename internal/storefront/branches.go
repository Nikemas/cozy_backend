package storefront

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
)

// Branch mirrors one row of points_of_sale (id/name/city/address/
// working_hours/latitude/longitude). Latitude/Longitude are nil until the
// owner fills them in the admin panel; MapURL then falls back to a 2GIS
// text search.
type Branch struct {
	ID           string
	Name         string
	City         string
	Address      string
	WorkingHours string
	Latitude     *float64
	Longitude    *float64
}

// MapURL is the 2GIS link for the branch: the exact point when coordinates
// are known (https://2gis.kg/geo/<lon>,<lat>), otherwise a search by
// "city, address".
func (b Branch) MapURL() string {
	if b.Latitude != nil && b.Longitude != nil {
		return fmt.Sprintf("https://2gis.kg/geo/%.6f,%.6f", *b.Longitude, *b.Latitude)
	}
	return "https://2gis.kg/search/" + url.PathEscape(b.City+", "+b.Address)
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

// EmbedURL is the keyless Google Maps iframe source for the branch: the
// exact point when coordinates are known, otherwise a search by
// "city, address".
func (b Branch) EmbedURL() string {
	q := b.City + ", " + b.Address
	if b.Latitude != nil && b.Longitude != nil {
		q = fmt.Sprintf("%.6f,%.6f", *b.Latitude, *b.Longitude)
	}
	return "https://maps.google.com/maps?z=16&output=embed&q=" + url.QueryEscape(q)
}

// List returns every active point of sale, ordered by name for a stable
// display order.
func (r *BranchRepo) List(ctx context.Context) ([]Branch, error) {
	const q = `
		SELECT id, name, city, address, working_hours, latitude, longitude
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
		if err := rows.Scan(&b.ID, &b.Name, &b.City, &b.Address, &b.WorkingHours, &b.Latitude, &b.Longitude); err != nil {
			return nil, err
		}
		branches = append(branches, b)
	}
	return branches, rows.Err()
}
