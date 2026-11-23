package points

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// Location mirrors a row of storage_locations (migration
// 000049_storage_locations): a named place inside a warehouse, such as
// "Стеллаж A, полка 3". A stock row may reference one (stock.location_id).
type Location struct {
	ID        string
	PointID   string
	Name      string
	SortOrder int
	IsActive  bool
	CreatedAt time.Time
	// StockQty is the total quantity of goods lying at this location (sum
	// of stock.quantity) — filled by ListByPoint only.
	StockQty int
}

// MaxLocationNameLen mirrors the CHECK on storage_locations.name.
const MaxLocationNameLen = 100

// pgUniqueViolation is the SQLSTATE of the (point_id, lower(name)) index.
const pgUniqueViolation = "23505"

// LocationsRepo manages the storage locations of warehouses.
type LocationsRepo struct {
	db *sql.DB
}

func NewLocationsRepo(db *sql.DB) *LocationsRepo {
	return &LocationsRepo{db: db}
}

// LocationInput is the writable part of a location.
type LocationInput struct {
	Name      string
	SortOrder int
}

func (in LocationInput) normalized() (LocationInput, error) {
	out := in
	out.Name = strings.Join(strings.Fields(in.Name), " ")
	if out.Name == "" || utf8.RuneCountInString(out.Name) > MaxLocationNameLen {
		return LocationInput{}, apperr.BadRequest("invalid_location_name", "укажите название места хранения (до 100 символов)")
	}
	return out, nil
}

const locationColumns = `l.id, l.point_id, l.name, l.sort_order, l.is_active, l.created_at`

func scanLocation(row rowScanner, extra ...any) (*Location, error) {
	var l Location
	dest := append([]any{&l.ID, &l.PointID, &l.Name, &l.SortOrder, &l.IsActive, &l.CreatedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	return &l, nil
}

// ListByPoint returns every location of one warehouse (inactive ones too),
// in display order, each with the quantity of goods lying there.
func (r *LocationsRepo) ListByPoint(ctx context.Context, pointID string) ([]*Location, error) {
	const q = `
		SELECT ` + locationColumns + `, COALESCE(SUM(s.quantity), 0)
		FROM storage_locations l
		LEFT JOIN stock s ON s.location_id = l.id
		WHERE l.point_id::text = $1
		GROUP BY l.id
		ORDER BY l.sort_order, lower(l.name), l.id`
	rows, err := r.db.QueryContext(ctx, q, pointID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := []*Location{}
	for rows.Next() {
		var l *Location
		var qty int
		if l, err = scanLocation(rows, &qty); err != nil {
			return nil, err
		}
		l.StockQty = qty
		out = append(out, l)
	}
	return out, rows.Err()
}

// ListAll returns every location of every warehouse (inactive ones too),
// grouped by point id, in display order — for the stock selects of the
// product form.
func (r *LocationsRepo) ListAll(ctx context.Context) (map[string][]*Location, error) {
	const q = `SELECT ` + locationColumns + ` FROM storage_locations l ORDER BY l.sort_order, lower(l.name), l.id`
	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := map[string][]*Location{}
	for rows.Next() {
		l, err := scanLocation(rows)
		if err != nil {
			return nil, err
		}
		out[l.PointID] = append(out[l.PointID], l)
	}
	return out, rows.Err()
}

// Get returns one location of pointID; apperr.NotFound when it doesn't
// exist or belongs to another point.
func (r *LocationsRepo) Get(ctx context.Context, pointID, id string) (*Location, error) {
	const q = `SELECT ` + locationColumns + ` FROM storage_locations l WHERE l.id::text = $1 AND l.point_id::text = $2`
	l, err := scanLocation(r.db.QueryRowContext(ctx, q, id, pointID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, locationNotFound()
	}
	return l, err
}

// Create adds an active location to a warehouse, after the existing ones
// unless in.SortOrder says otherwise (> 0).
func (r *LocationsRepo) Create(ctx context.Context, pointID string, raw LocationInput) (*Location, error) {
	in, err := raw.normalized()
	if err != nil {
		return nil, err
	}
	if err := r.requireWarehouse(ctx, pointID); err != nil {
		return nil, err
	}
	const q = `
		INSERT INTO storage_locations AS l (point_id, name, sort_order)
		VALUES ($1, $2, CASE WHEN $3 > 0 THEN $3
		        ELSE (SELECT COALESCE(MAX(sort_order), 0) + 1 FROM storage_locations WHERE point_id = $1) END)
		RETURNING ` + locationColumns
	l, err := scanLocation(r.db.QueryRowContext(ctx, q, pointID, in.Name, in.SortOrder))
	if err != nil {
		return nil, translateLocationErr(err)
	}
	return l, nil
}

// Update renames a location and sets its sort order.
func (r *LocationsRepo) Update(ctx context.Context, pointID, id string, raw LocationInput) (*Location, error) {
	in, err := raw.normalized()
	if err != nil {
		return nil, err
	}
	const q = `
		UPDATE storage_locations AS l SET name = $3, sort_order = $4
		WHERE l.id::text = $1 AND l.point_id::text = $2
		RETURNING ` + locationColumns
	l, err := scanLocation(r.db.QueryRowContext(ctx, q, id, pointID, in.Name, in.SortOrder))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, locationNotFound()
	}
	if err != nil {
		return nil, translateLocationErr(err)
	}
	return l, nil
}

// SetActive activates or deactivates a location. A deactivated location
// keeps its stock rows; it just can't be picked for new goods.
func (r *LocationsRepo) SetActive(ctx context.Context, pointID, id string, active bool) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE storage_locations SET is_active = $3 WHERE id::text = $1 AND point_id::text = $2`, id, pointID, active)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// Delete removes a location that holds no goods. With goods on it
// (stock.quantity > 0) it is refused with location_in_use — the location
// can only be deactivated then. Empty stock rows just lose their
// location_id (ON DELETE SET NULL).
func (r *LocationsRepo) Delete(ctx context.Context, pointID, id string) error {
	const q = `
		DELETE FROM storage_locations l
		WHERE l.id::text = $1 AND l.point_id::text = $2
		  AND NOT EXISTS (SELECT 1 FROM stock s WHERE s.location_id = l.id AND s.quantity > 0)`
	res, err := r.db.ExecContext(ctx, q, id, pointID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if _, err := r.Get(ctx, pointID, id); err != nil {
		return err
	}
	return apperr.Conflict("location_in_use", "на этом месте хранения есть товар — его можно только деактивировать")
}

func (r *LocationsRepo) requireWarehouse(ctx context.Context, pointID string) error {
	var kind string
	err := r.db.QueryRowContext(ctx, `SELECT kind FROM points_of_sale WHERE id::text = $1`, pointID).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.NotFound("point_not_found", "точка продаж не найдена")
	}
	if err != nil {
		return fmt.Errorf("point kind of %s: %w", pointID, err)
	}
	if kind != KindWarehouse {
		return apperr.BadRequest("not_a_warehouse", "места хранения есть только у склада")
	}
	return nil
}

func requireOneRow(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return locationNotFound()
	}
	return nil
}

func translateLocationErr(err error) error {
	if pgErrCode(err) == pgUniqueViolation {
		return apperr.Conflict("location_name_taken", "такое место хранения на этом складе уже есть")
	}
	return err
}

func locationNotFound() error {
	return apperr.NotFound("location_not_found", "место хранения не найдено")
}

// StockKey identifies one stock row: a variant at a point.
type StockKey struct{ VariantID, PointID string }

// AssignedByVariants returns the storage location of every stock row of
// the given variants that has one.
func (r *LocationsRepo) AssignedByVariants(ctx context.Context, variantIDs []string) (map[StockKey]string, error) {
	out := map[StockKey]string{}
	if len(variantIDs) == 0 {
		return out, nil
	}
	const q = `
		SELECT variant_id::text, point_id::text, location_id::text FROM stock
		WHERE location_id IS NOT NULL AND variant_id::text = ANY($1)`
	rows, err := r.db.QueryContext(ctx, q, variantIDs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k StockKey
		var loc string
		if err := rows.Scan(&k.VariantID, &k.PointID, &loc); err != nil {
			return nil, err
		}
		out[k] = loc
	}
	return out, rows.Err()
}
