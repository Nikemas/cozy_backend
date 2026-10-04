// Package points implements admin CRUD for points of sale (the
// points_of_sale table, migration 000003_create_points_of_sale) — Task L,
// wave 3. Unlike internal/storefront's BranchRepo (a read-only, active-only
// view of the same table for the customer-facing branches screen), every
// route here is gated to staff.RoleOwner only and List returns every point,
// including inactive ones, since the admin needs to see and manage
// everything.
package points

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// Point mirrors a row of points_of_sale.
type Point struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	City         string    `json:"city"`
	Address      string    `json:"address"`
	WorkingHours string    `json:"working_hours"`
	Latitude     *float64  `json:"latitude"`
	Longitude    *float64  `json:"longitude"`
	IsActive     bool      `json:"is_active"`
	CreatedAt    time.Time `json:"created_at"`
}

// PointsRepo is the CRUD repo behind /admin/api/points. It mirrors the
// shape of catalog.CategoryRepo: a validate()-ing input type shared by
// Create/Update, and Delete translating a Postgres FK violation into
// apperr.Conflict rather than a raw 500 or a silent cascade.
type PointsRepo struct {
	db *sql.DB
}

func NewPointsRepo(db *sql.DB) *PointsRepo {
	return &PointsRepo{db: db}
}

// PointInput carries the writable fields of a point of sale, shared by
// Create and Update.
type PointInput struct {
	Name         string
	City         string
	Address      string
	WorkingHours string
	Latitude     *float64
	Longitude    *float64
	IsActive     bool
}

func (in PointInput) validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return apperr.BadRequest("invalid_name", "name обязателен")
	}
	if strings.TrimSpace(in.City) == "" {
		return apperr.BadRequest("invalid_city", "city обязателен")
	}
	if strings.TrimSpace(in.Address) == "" {
		return apperr.BadRequest("invalid_address", "address обязателен")
	}
	return validateCoords(in.Latitude, in.Longitude)
}

// validateCoords accepts either no coordinates or a valid lat/lng pair.
func validateCoords(lat, lng *float64) error {
	if lat == nil && lng == nil {
		return nil
	}
	if lat == nil || lng == nil {
		return apperr.BadRequest("invalid_coordinates", "укажите и широту, и долготу")
	}
	if *lat < -90 || *lat > 90 || *lng < -180 || *lng > 180 {
		return apperr.BadRequest("invalid_coordinates", "координаты вне допустимого диапазона")
	}
	return nil
}

const pointColumns = `id, name, city, address, working_hours, latitude, longitude, is_active, created_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanPoint(row rowScanner) (*Point, error) {
	var p Point
	err := row.Scan(&p.ID, &p.Name, &p.City, &p.Address, &p.WorkingHours, &p.Latitude, &p.Longitude, &p.IsActive, &p.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// List returns every point of sale, including inactive ones, ordered by
// name for a stable display order.
func (r *PointsRepo) List(ctx context.Context) ([]*Point, error) {
	const q = `SELECT ` + pointColumns + ` FROM points_of_sale ORDER BY name`

	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	points := make([]*Point, 0)
	for rows.Next() {
		p, err := scanPoint(rows)
		if err != nil {
			return nil, err
		}
		points = append(points, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return points, nil
}

// GetByID returns one point of sale (active or not). Returns
// apperr.NotFound if no such point exists.
func (r *PointsRepo) GetByID(ctx context.Context, id string) (*Point, error) {
	const q = `SELECT ` + pointColumns + ` FROM points_of_sale WHERE id::text = $1`

	p, err := scanPoint(r.db.QueryRowContext(ctx, q, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.NotFound("point_not_found", "точка продаж не найдена")
	}
	return p, err
}

// Create inserts a new point of sale and returns the row as stored.
func (r *PointsRepo) Create(ctx context.Context, in PointInput) (*Point, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}

	const q = `
		INSERT INTO points_of_sale (name, city, address, working_hours, latitude, longitude, is_active)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING ` + pointColumns

	return scanPoint(r.db.QueryRowContext(ctx, q, in.Name, in.City, in.Address, in.WorkingHours, in.Latitude, in.Longitude, in.IsActive))
}

// Update replaces every writable field of the point of sale with the given
// id. Returns apperr.NotFound if no such point exists.
func (r *PointsRepo) Update(ctx context.Context, id string, in PointInput) (*Point, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}

	const q = `
		UPDATE points_of_sale
		SET name = $2, city = $3, address = $4, working_hours = $5,
		    latitude = $6, longitude = $7, is_active = $8
		WHERE id = $1
		RETURNING ` + pointColumns

	p, err := scanPoint(r.db.QueryRowContext(ctx, q, id, in.Name, in.City, in.Address, in.WorkingHours, in.Latitude, in.Longitude, in.IsActive))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.NotFound("point_not_found", "точка продаж не найдена")
	}
	return p, err
}

// Delete removes a point of sale. A point still referenced by
// staff.point_id, stock.point_id or orders.point_id can't be removed
// without breaking that reference; Postgres raises a foreign key violation
// (SQLSTATE 23503) for that, which translateDeleteErr turns into
// apperr.Conflict instead of letting it leak as a raw 500 or silently
// cascade.
func (r *PointsRepo) Delete(ctx context.Context, id string) error {
	const q = `DELETE FROM points_of_sale WHERE id = $1`

	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return translateDeleteErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return apperr.NotFound("point_not_found", "точка продаж не найдена")
	}
	return nil
}

// translateDeleteErr maps a Postgres foreign key violation from Delete into
// apperr.Conflict, passing through any other error unchanged. Kept as a
// pure function (no receiver, no I/O) so it can be unit tested against a
// fake *pgconn.PgError without a database.
func translateDeleteErr(err error) error {
	if pgErrCode(err) == pgForeignKeyViolation {
		return apperr.Conflict("point_in_use", "нельзя удалить точку продаж: на неё ссылаются сотрудники, остатки или заказы")
	}
	return err
}
