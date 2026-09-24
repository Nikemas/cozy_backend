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
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Address   string    `json:"address"`
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
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
	Name     string
	Address  string
	IsActive bool
}

func (in PointInput) validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return apperr.BadRequest("invalid_name", "name обязателен")
	}
	if strings.TrimSpace(in.Address) == "" {
		return apperr.BadRequest("invalid_address", "address обязателен")
	}
	return nil
}

// List returns every point of sale, including inactive ones, ordered by
// name for a stable display order.
func (r *PointsRepo) List(ctx context.Context) ([]*Point, error) {
	const q = `
		SELECT id, name, address, is_active, created_at
		FROM points_of_sale
		ORDER BY name`

	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	points := make([]*Point, 0)
	for rows.Next() {
		var p Point
		if err := rows.Scan(&p.ID, &p.Name, &p.Address, &p.IsActive, &p.CreatedAt); err != nil {
			return nil, err
		}
		points = append(points, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return points, nil
}

// GetByID returns one point of sale (active or not). Returns
// apperr.NotFound if no such point exists.
func (r *PointsRepo) GetByID(ctx context.Context, id string) (*Point, error) {
	const q = `
		SELECT id, name, address, is_active, created_at
		FROM points_of_sale
		WHERE id::text = $1`

	var p Point
	err := r.db.QueryRowContext(ctx, q, id).Scan(&p.ID, &p.Name, &p.Address, &p.IsActive, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.NotFound("point_not_found", "точка продаж не найдена")
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Create inserts a new point of sale and returns the row as stored.
func (r *PointsRepo) Create(ctx context.Context, in PointInput) (*Point, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}

	const q = `
		INSERT INTO points_of_sale (name, address, is_active)
		VALUES ($1, $2, $3)
		RETURNING id, name, address, is_active, created_at`

	var p Point
	err := r.db.QueryRowContext(ctx, q, in.Name, in.Address, in.IsActive).
		Scan(&p.ID, &p.Name, &p.Address, &p.IsActive, &p.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Update replaces every writable field of the point of sale with the given
// id. Returns apperr.NotFound if no such point exists.
func (r *PointsRepo) Update(ctx context.Context, id string, in PointInput) (*Point, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}

	const q = `
		UPDATE points_of_sale
		SET name = $2, address = $3, is_active = $4
		WHERE id = $1
		RETURNING id, name, address, is_active, created_at`

	var p Point
	err := r.db.QueryRowContext(ctx, q, id, in.Name, in.Address, in.IsActive).
		Scan(&p.ID, &p.Name, &p.Address, &p.IsActive, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.NotFound("point_not_found", "точка продаж не найдена")
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
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
