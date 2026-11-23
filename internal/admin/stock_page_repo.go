package admin

import (
	"context"
	"database/sql"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/audit"
	"github.com/Nikemas/cozy_backend/internal/dbtx"
)

// stockPageSize is the number of variant rows per page.
const stockPageSize = 50

// stockLocationNone is the «Место» filter value for rows without a
// storage location.
const stockLocationNone = "none"

// stockFilter is what the Остатки page lists: one point, optionally
// narrowed by a search (name / model code / brand / SKU), a catalog
// category (with its subcategories), a color and — for a warehouse — a
// storage location (or stockLocationNone).
type stockFilter struct {
	PointID    string
	Query      string
	CategoryID string
	Color      string
	LocationID string
	Page       int
}

// stockPageRow is one variant with its stock at the selected point.
type stockPageRow struct {
	VariantID   string
	ProductID   string
	ProductName string
	ModelCode   string
	Brand       string
	Size        string
	Color       string
	Quantity    int
	HasStockRow bool
	LocationID  string // "" = no storage location
}

// stockCategory is one catalog category for the page's filter select.
type stockCategory struct {
	ID       string
	ParentID string
	NameRu   string
	NameKy   string
}

// stockLocation is one storage location for the «Место» selects.
type stockLocation struct {
	ID       string
	Name     string
	IsActive bool
}

// stockPageStore is the DB side of the screen (an interface so the
// handler can be tested with a fake).
type stockPageStore interface {
	List(ctx context.Context, f stockFilter) ([]stockPageRow, int, error)
	Categories(ctx context.Context) ([]stockCategory, error)
	Colors(ctx context.Context) ([]string, error)
	Locations(ctx context.Context, pointID string) ([]stockLocation, error)
	Apply(ctx context.Context, changes []stockCellChange, locations []stockLocationChange) error
}

type stockPageRepo struct {
	db    *sql.DB
	audit *audit.Log // nil = no journal
}

func newStockPageRepo(db *sql.DB) *stockPageRepo { return &stockPageRepo{db: db} }

// escapeLike escapes LIKE/ILIKE wildcards in user input.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// stockListWhere is shared by the count and the page query. $1 point,
// $2 search pattern, $3 category id, $4 color, $5 location filter.
const stockListWhere = `
		FROM product_variants v
		JOIN products p ON p.id = v.product_id
		LEFT JOIN stock s ON s.variant_id = v.id AND s.point_id = $1
		WHERE p.is_active
		  AND ($2 = '%%' OR p.name_ru ILIKE $2 OR p.name_ky ILIKE $2 OR p.brand ILIKE $2
		       OR p.model_code ILIKE $2 OR v.sku ILIKE $2)
		  AND ($3 = '' OR p.category_id IN (
		        WITH RECURSIVE sub AS (
		          SELECT id FROM categories WHERE id::text = $3
		          UNION ALL
		          SELECT c.id FROM categories c JOIN sub ON c.parent_id = sub.id)
		        SELECT id FROM sub))
		  AND ($4 = '' OR lower(v.color) = lower($4))
		  AND ($5 = '' OR ($5 = '` + stockLocationNone + `' AND s.location_id IS NULL) OR s.location_id::text = $5)`

// List returns active products' variants with their stock at f.PointID,
// filtered by f, sorted by product.
func (r *stockPageRepo) List(ctx context.Context, f stockFilter) ([]stockPageRow, int, error) {
	page := f.Page
	if page < 1 {
		page = 1
	}
	pattern := "%" + escapeLike(strings.TrimSpace(f.Query)) + "%"
	args := []any{f.PointID, pattern, f.CategoryID, f.Color, f.LocationID}

	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) `+stockListWhere, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := `SELECT v.id, p.id, p.name_ru, COALESCE(p.model_code, ''), COALESCE(p.brand, ''), v.size, v.color,
		       s.quantity, COALESCE(s.location_id::text, '') ` + stockListWhere + `
		ORDER BY p.name_ru, p.id, v.size, v.color
		LIMIT $6 OFFSET $7`
	rows, err := r.db.QueryContext(ctx, q, append(args, stockPageSize, (page-1)*stockPageSize)...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	var out []stockPageRow
	for rows.Next() {
		var row stockPageRow
		var qty sql.NullInt64
		if err := rows.Scan(&row.VariantID, &row.ProductID, &row.ProductName, &row.ModelCode, &row.Brand,
			&row.Size, &row.Color, &qty, &row.LocationID); err != nil {
			return nil, 0, err
		}
		row.Quantity = int(qty.Int64)
		row.HasStockRow = qty.Valid
		out = append(out, row)
	}
	return out, total, rows.Err()
}

// Categories returns every catalog category, parents before children
// within the sort order (the page builds the indented select from it).
func (r *stockPageRepo) Categories(ctx context.Context) ([]stockCategory, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, COALESCE(parent_id::text, ''), name_ru, name_ky FROM categories ORDER BY sort_order, name_ru`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []stockCategory
	for rows.Next() {
		var c stockCategory
		if err := rows.Scan(&c.ID, &c.ParentID, &c.NameRu, &c.NameKy); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Colors returns the distinct variant colors of active products,
// case-insensitively deduplicated.
func (r *stockPageRepo) Colors(ctx context.Context) ([]string, error) {
	const q = `
		SELECT MIN(v.color) FROM product_variants v JOIN products p ON p.id = v.product_id
		WHERE p.is_active AND btrim(v.color) <> ''
		GROUP BY lower(v.color) ORDER BY lower(v.color)`
	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Locations returns every storage location of pointID (inactive ones too).
func (r *stockPageRepo) Locations(ctx context.Context, pointID string) ([]stockLocation, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, name, is_active FROM storage_locations WHERE point_id::text = $1 ORDER BY sort_order, lower(name), id`, pointID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []stockLocation
	for rows.Next() {
		var l stockLocation
		if err := rows.Scan(&l.ID, &l.Name, &l.IsActive); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Apply writes changes and location changes (RowKey = variant id) in one
// transaction, with the same optimistic check as the product form.
func (r *stockPageRepo) Apply(ctx context.Context, changes []stockCellChange, locations []stockLocationChange) error {
	if len(changes) == 0 && len(locations) == 0 {
		return nil
	}
	idByKey := make(map[string]string, len(changes)+len(locations))
	for _, c := range changes {
		idByKey[c.RowKey] = c.RowKey
	}
	for _, c := range locations {
		idByKey[c.RowKey] = c.RowKey
	}
	return dbtx.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		if err := applyStockChangesTx(ctx, tx, idByKey, changes); err != nil {
			return err
		}
		if err := applyStockLocationsTx(ctx, tx, idByKey, locations); err != nil {
			return err
		}
		if r.audit.Enabled() {
			r.audit.RecordTxFunc(ctx, tx, func() ([]audit.Entry, error) {
				return stockEntriesTx(ctx, tx, idByKey, changes)
			})
		}
		return nil
	})
}
