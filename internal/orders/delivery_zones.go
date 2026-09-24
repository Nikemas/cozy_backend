package orders

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// DeliveryZone mirrors one row of delivery_zones (migration 000035). A
// delivery order's fee is its zone's Fee, or 0 once the items total
// reaches FreeFrom. While no zone is active, Settings.DeliveryFee
// (DELIVERY_FEE_SOM) applies to every delivery order instead.
type DeliveryZone struct {
	ID     string  `json:"id"`
	NameRu string  `json:"name_ru"`
	NameKy string  `json:"name_ky"`
	Fee    float64 `json:"fee"`
	// FreeFrom is the items total (som, delivery excluded) from which
	// delivery to this zone is free; nil = never free.
	FreeFrom  *float64 `json:"free_from"`
	IsActive  bool     `json:"-"`
	SortOrder int      `json:"-"`
}

// FeeFor is the delivery charge to this zone for an order whose items
// cost itemsTotal.
func (z DeliveryZone) FeeFor(itemsTotal float64) float64 {
	if z.FreeFrom != nil && toTyiyn(itemsTotal) >= toTyiyn(*z.FreeFrom) {
		return 0
	}
	return z.Fee
}

// Name is the zone's name in lang ("ky" or anything else → Russian).
func (z DeliveryZone) Name(lang string) string {
	if lang == "ky" && z.NameKy != "" {
		return z.NameKy
	}
	return z.NameRu
}

// OrderDeliveryZone is the zone reference carried by Order JSON
// (`delivery_zone`, null for pickup / orders without a zone).
type OrderDeliveryZone struct {
	ID     string `json:"id"`
	NameRu string `json:"name_ru"`
	NameKy string `json:"name_ky"`
}

// DeliveryZoneInput is what the admin form submits.
type DeliveryZoneInput struct {
	NameRu    string
	NameKy    string
	Fee       float64
	FreeFrom  *float64
	IsActive  bool
	SortOrder int
}

// Limits mirroring the delivery_zones CHECK constraints.
const (
	maxZoneNameLen  = 100
	maxZoneFee      = 100000
	maxZoneFreeFrom = 10000000
)

// ErrDeliveryZoneRequired: a delivery order without a zone while zones exist.
var ErrDeliveryZoneRequired = apperr.BadRequest("delivery_zone_required", "выберите зону доставки")

// ErrInvalidDeliveryZone: unknown or deactivated zone.
var ErrInvalidDeliveryZone = apperr.BadRequest("invalid_delivery_zone",
	"зона доставки не найдена или больше не обслуживается — выберите другую")

// Normalize trims and validates the input (400 invalid_delivery_zone_input).
func (in *DeliveryZoneInput) Normalize() error {
	in.NameRu = strings.TrimSpace(in.NameRu)
	in.NameKy = strings.TrimSpace(in.NameKy)
	bad := func(msg string) error { return apperr.BadRequest("invalid_delivery_zone_input", msg) }
	if in.NameRu == "" || utf8.RuneCountInString(in.NameRu) > maxZoneNameLen {
		return bad(fmt.Sprintf("название (RU) обязательно, не длиннее %d символов", maxZoneNameLen))
	}
	if in.NameKy == "" || utf8.RuneCountInString(in.NameKy) > maxZoneNameLen {
		return bad(fmt.Sprintf("название (KY) обязательно, не длиннее %d символов", maxZoneNameLen))
	}
	if math.IsNaN(in.Fee) || math.IsInf(in.Fee, 0) || in.Fee < 0 || in.Fee > maxZoneFee {
		return bad(fmt.Sprintf("стоимость доставки — число от 0 до %d сом", maxZoneFee))
	}
	in.Fee = roundSom(in.Fee)
	if in.FreeFrom != nil {
		f := *in.FreeFrom
		if math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 || f > maxZoneFreeFrom {
			return bad("«бесплатно от» — положительная сумма или пусто")
		}
		f = roundSom(f)
		in.FreeFrom = &f
	}
	return nil
}

// DeliveryZoneRepo reads and writes delivery_zones.
type DeliveryZoneRepo struct {
	db *sql.DB
}

func NewDeliveryZoneRepo(db *sql.DB) *DeliveryZoneRepo {
	return &DeliveryZoneRepo{db: db}
}

const zoneColumns = `id, name_ru, name_ky, fee, free_from, is_active, sort_order`

func scanZone(row rowScanner, z *DeliveryZone) error {
	return row.Scan(&z.ID, &z.NameRu, &z.NameKy, &z.Fee, &z.FreeFrom, &z.IsActive, &z.SortOrder)
}

func (r *DeliveryZoneRepo) list(ctx context.Context, where string) ([]DeliveryZone, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+zoneColumns+` FROM delivery_zones `+where+` ORDER BY sort_order, name_ru, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []DeliveryZone{}
	for rows.Next() {
		var z DeliveryZone
		if err := scanZone(rows, &z); err != nil {
			return nil, err
		}
		out = append(out, z)
	}
	return out, rows.Err()
}

// ListActive returns the active zones in display order (sort_order, name).
func (r *DeliveryZoneRepo) ListActive(ctx context.Context) ([]DeliveryZone, error) {
	return r.list(ctx, `WHERE is_active`)
}

// ListAll returns every zone (admin), in display order.
func (r *DeliveryZoneRepo) ListAll(ctx context.Context) ([]DeliveryZone, error) {
	return r.list(ctx, ``)
}

// Get returns one zone (404 delivery_zone_not_found).
func (r *DeliveryZoneRepo) Get(ctx context.Context, id string) (*DeliveryZone, error) {
	if uuid.Validate(id) != nil {
		return nil, apperr.NotFound("delivery_zone_not_found", "зона доставки не найдена")
	}
	var z DeliveryZone
	err := scanZone(r.db.QueryRowContext(ctx, `SELECT `+zoneColumns+` FROM delivery_zones WHERE id = $1`, id), &z)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.NotFound("delivery_zone_not_found", "зона доставки не найдена")
	}
	if err != nil {
		return nil, err
	}
	return &z, nil
}

// Create inserts a zone.
func (r *DeliveryZoneRepo) Create(ctx context.Context, in DeliveryZoneInput) (*DeliveryZone, error) {
	if err := in.Normalize(); err != nil {
		return nil, err
	}
	const q = `
		INSERT INTO delivery_zones (name_ru, name_ky, fee, free_from, is_active, sort_order)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING ` + zoneColumns
	var z DeliveryZone
	if err := scanZone(r.db.QueryRowContext(ctx, q, in.NameRu, in.NameKy, in.Fee, in.FreeFrom, in.IsActive, in.SortOrder), &z); err != nil {
		return nil, err
	}
	return &z, nil
}

// Update overwrites a zone's fields (is_active included).
func (r *DeliveryZoneRepo) Update(ctx context.Context, id string, in DeliveryZoneInput) (*DeliveryZone, error) {
	if err := in.Normalize(); err != nil {
		return nil, err
	}
	if uuid.Validate(id) != nil {
		return nil, apperr.NotFound("delivery_zone_not_found", "зона доставки не найдена")
	}
	const q = `
		UPDATE delivery_zones
		SET name_ru = $2, name_ky = $3, fee = $4, free_from = $5, is_active = $6, sort_order = $7, updated_at = now()
		WHERE id = $1
		RETURNING ` + zoneColumns
	var z DeliveryZone
	err := scanZone(r.db.QueryRowContext(ctx, q, id, in.NameRu, in.NameKy, in.Fee, in.FreeFrom, in.IsActive, in.SortOrder), &z)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.NotFound("delivery_zone_not_found", "зона доставки не найдена")
	}
	if err != nil {
		return nil, err
	}
	return &z, nil
}

// SetActive switches a zone on or off.
func (r *DeliveryZoneRepo) SetActive(ctx context.Context, id string, active bool) error {
	if uuid.Validate(id) != nil {
		return apperr.NotFound("delivery_zone_not_found", "зона доставки не найдена")
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE delivery_zones SET is_active = $2, updated_at = now() WHERE id = $1`, id, active)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperr.NotFound("delivery_zone_not_found", "зона доставки не найдена")
	}
	return nil
}

// Delete removes a zone no order refers to; a used zone is 409
// delivery_zone_in_use (deactivate it instead).
func (r *DeliveryZoneRepo) Delete(ctx context.Context, id string) error {
	if uuid.Validate(id) != nil {
		return apperr.NotFound("delivery_zone_not_found", "зона доставки не найдена")
	}
	res, err := r.db.ExecContext(ctx, `DELETE FROM delivery_zones WHERE id = $1`, id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return apperr.Conflict("delivery_zone_in_use",
				"по этой зоне уже есть заказы — её можно только деактивировать")
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperr.NotFound("delivery_zone_not_found", "зона доставки не найдена")
	}
	return nil
}

// resolveDeliveryZoneTx picks the zone a delivery order is charged for:
// the requested active zone; nil when no zone is active (the flat
// Settings.DeliveryFee applies); ErrDeliveryZoneRequired when zones exist
// but none was given; ErrInvalidDeliveryZone for an unknown/inactive one.
func resolveDeliveryZoneTx(ctx context.Context, tx *sql.Tx, zoneID *string) (*DeliveryZone, error) {
	if zoneID != nil && *zoneID != "" {
		if uuid.Validate(*zoneID) != nil {
			return nil, ErrInvalidDeliveryZone
		}
		var z DeliveryZone
		err := scanZone(tx.QueryRowContext(ctx,
			`SELECT `+zoneColumns+` FROM delivery_zones WHERE id = $1 AND is_active`, *zoneID), &z)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidDeliveryZone
		}
		if err != nil {
			return nil, err
		}
		return &z, nil
	}
	var zonesExist bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM delivery_zones WHERE is_active)`).Scan(&zonesExist); err != nil {
		return nil, err
	}
	if zonesExist {
		return nil, ErrDeliveryZoneRequired
	}
	return nil, nil
}

// deliveryFee is what a new order is charged for delivery: 0 for pickup,
// the zone's fee (0 from its free_from threshold) for a zoned delivery,
// else the flat Settings.DeliveryFee.
func deliveryFee(st Settings, isDelivery bool, zone *DeliveryZone, itemsTotal float64) float64 {
	if !isDelivery {
		return 0
	}
	if zone != nil {
		return zone.FeeFor(itemsTotal)
	}
	return st.DeliveryFee
}

// toTyiyn converts som to tyiyn (1/100) for exact comparisons.
func toTyiyn(som float64) int64 { return int64(math.Round(som * 100)) }
