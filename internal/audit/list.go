package audit

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
)

// DefaultPageSize is List's page size when Filter.PageSize is unset.
const DefaultPageSize = 50

// maxPage caps Filter.Page so a hostile ?page= can't overflow the OFFSET.
const maxPage = 100000

// Filter narrows List. Zero values mean "no filter".
type Filter struct {
	StaffID    string    // a staff uuid; anything that isn't one is ignored
	EntityType string    // one of the Entity* constants
	From       time.Time // inclusive
	To         time.Time // exclusive
	// EntityQuery matches an entity id by prefix (a pasted uuid or its
	// first characters) — for variants and stock also their product's id —
	// or, for orders, an exact order number.
	EntityQuery string
	Page        int // 1-based
	PageSize    int
}

// Row is one journal line as the admin page shows it.
type Row struct {
	ID         string
	At         time.Time
	StaffID    *string
	StaffName  string
	Action     string
	EntityType string
	EntityID   string
	Summary    string
	Details    map[string]any
	IP         string
}

// entriesCTE is the journal: audit_log plus staff-made order status
// changes from order_status_history (rendered as action order.status,
// entity order, details {order_number, from, to, note}).
const entriesCTE = `
	WITH entries AS (
		SELECT a.id, a.at, a.staff_id, a.action, a.entity_type, a.entity_id, a.summary, a.details,
		       COALESCE(a.ip, '') AS ip
		FROM audit_log a
		UNION ALL
		SELECT h.id, h.created_at, h.actor_staff_id, 'order.status', 'order', h.order_id::text,
		       'Заказ ' || o.order_number,
		       jsonb_build_object('order_number', o.order_number, 'from', h.from_status,
		                          'to', h.to_status, 'note', h.note),
		       ''
		FROM order_status_history h
		JOIN orders o ON o.id = h.order_id
		WHERE h.actor_type = 'staff'
	)`

const listWhere = `
	WHERE ($1::uuid IS NULL OR e.staff_id = $1::uuid)
	  AND ($2 = '' OR e.entity_type = $2)
	  AND ($3::timestamptz IS NULL OR e.at >= $3::timestamptz)
	  AND ($4::timestamptz IS NULL OR e.at < $4::timestamptz)
	  AND ($5 = '' OR starts_with(lower(e.entity_id), lower($5))
	       OR starts_with(lower(e.details->>'product_id'), lower($5))
	       OR lower(e.details->>'order_number') = lower($5))`

// filterArgs turns f into listWhere's $1..$5.
func filterArgs(f Filter) []any {
	var staffID, from, to any
	if id, err := uuid.Parse(strings.TrimSpace(f.StaffID)); err == nil {
		staffID = id.String()
	}
	if !f.From.IsZero() {
		from = f.From
	}
	if !f.To.IsZero() {
		to = f.To
	}
	return []any{staffID, strings.TrimSpace(f.EntityType), from, to, strings.TrimSpace(f.EntityQuery)}
}

// List returns one page of the journal, newest first, plus the total
// number of matching lines.
func (l *Log) List(ctx context.Context, f Filter) ([]Row, int, error) {
	if l == nil || l.db == nil {
		return nil, 0, nil
	}
	page := f.Page
	if page < 1 {
		page = 1
	}
	if page > maxPage {
		page = maxPage
	}
	size := f.PageSize
	if size <= 0 {
		size = DefaultPageSize
	}
	args := filterArgs(f)

	var total int
	countQ := entriesCTE + ` SELECT COUNT(*) FROM entries e ` + listWhere
	if err := l.db.QueryRowContext(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	listQ := entriesCTE + `
		SELECT e.id, e.at, e.staff_id, COALESCE(s.name, ''), e.action, e.entity_type, e.entity_id,
		       e.summary, e.details, e.ip
		FROM entries e
		LEFT JOIN staff s ON s.id = e.staff_id ` + listWhere + `
		ORDER BY e.at DESC, e.id DESC
		LIMIT $6 OFFSET $7`
	rows, err := l.db.QueryContext(ctx, listQ, append(args, size, (page-1)*size)...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	out := []Row{}
	for rows.Next() {
		var r Row
		var details []byte
		if err := rows.Scan(&r.ID, &r.At, &r.StaffID, &r.StaffName, &r.Action, &r.EntityType, &r.EntityID,
			&r.Summary, &details, &r.IP); err != nil {
			return nil, 0, err
		}
		if len(details) > 0 {
			_ = json.Unmarshal(details, &r.Details)
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}
