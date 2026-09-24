// Package broadcasts implements promo push broadcasts ("Рассылки"): staff
// compose a RU/KY title+body with an optional deep link (a product or a
// category) in the admin panel, and a background Worker sends it through
// FCM to every device of every customer who kept promo_push on.
//
// Sending never happens on the admin request: Create only inserts a
// queued row (idempotent per form render via a submit token) and nudges
// the Worker, which works through device_tokens in batches, persisting
// progress after each one — so a broadcast survives the request ending,
// a graceful shutdown (it stops between batches) and a restart (it
// resumes from its cursor).
package broadcasts

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// Statuses of a broadcast row.
const (
	StatusQueued  = "queued"
	StatusSending = "sending"
	StatusDone    = "done"
	StatusFailed  = "failed"
)

// Link kinds the admin form offers.
const (
	LinkNone     = ""
	LinkProduct  = "product"
	LinkCategory = "category"
)

// PushTypePromo is data["type"] on promo pushes (contract with the app:
// {"type":"promo","link":"<path>"}).
const PushTypePromo = "promo"

// Text limits: roughly what Android/iOS show on a collapsed notification
// without cutting mid-word; longer text is rejected rather than truncated.
const (
	MaxTitleLen = 65
	MaxBodyLen  = 240
)

// Broadcast is one row of broadcasts.
type Broadcast struct {
	ID             string
	TitleRU        string
	BodyRU         string
	TitleKY        string
	BodyKY         string
	Link           string
	LinkLabel      string
	Status         string
	CreatedByName  string
	Targets        int
	Sent           int
	Failed         int
	InvalidRemoved int
	CursorTokenID  *string
	Error          *string
	CreatedAt      time.Time
	StartedAt      *time.Time
	FinishedAt     *time.Time
}

// Input is what the admin form submits.
type Input struct {
	TitleRU, BodyRU string
	TitleKY, BodyKY string
	LinkType        string // LinkNone | LinkProduct | LinkCategory
	LinkID          string // product/category UUID for LinkProduct/LinkCategory
}

// Author identifies the staff member creating a broadcast.
type Author struct {
	StaffID string
	Name    string
}

// normalize trims every text field and validates lengths and link shape.
// The link target's existence is checked by Repo.Create.
func (in *Input) normalize() error {
	in.TitleRU = strings.TrimSpace(in.TitleRU)
	in.BodyRU = strings.TrimSpace(in.BodyRU)
	in.TitleKY = strings.TrimSpace(in.TitleKY)
	in.BodyKY = strings.TrimSpace(in.BodyKY)
	in.LinkID = strings.TrimSpace(in.LinkID)

	if in.TitleRU == "" || in.BodyRU == "" {
		return apperr.BadRequest("invalid_broadcast", "заполните заголовок и текст на русском")
	}
	for _, f := range []struct {
		val string
		max int
		msg string
	}{
		{in.TitleRU, MaxTitleLen, "заголовок (RU) длиннее 65 символов"},
		{in.TitleKY, MaxTitleLen, "заголовок (KY) длиннее 65 символов"},
		{in.BodyRU, MaxBodyLen, "текст (RU) длиннее 240 символов"},
		{in.BodyKY, MaxBodyLen, "текст (KY) длиннее 240 символов"},
	} {
		if utf8.RuneCountInString(f.val) > f.max {
			return apperr.BadRequest("invalid_broadcast", f.msg)
		}
	}
	switch in.LinkType {
	case LinkNone:
		in.LinkID = ""
	case LinkProduct, LinkCategory:
		if _, err := uuid.Parse(in.LinkID); err != nil {
			return apperr.BadRequest("invalid_broadcast_link", "выберите товар или категорию для ссылки")
		}
	default:
		return apperr.BadRequest("invalid_broadcast_link", "неизвестный тип ссылки")
	}
	return nil
}

// linkPath is the app route the push opens (contract: /product/<id>,
// /catalog?category=<id> or "").
func linkPath(linkType, id string) string {
	switch linkType {
	case LinkProduct:
		return "/product/" + id
	case LinkCategory:
		return "/catalog?category=" + id
	default:
		return ""
	}
}

// Repo is the broadcasts table plus the audience queries the Worker and
// the admin page need.
type Repo struct {
	db *sql.DB
}

func NewRepo(db *sql.DB) *Repo { return &Repo{db: db} }

const broadcastColumns = `id, title_ru, body_ru, title_ky, body_ky, link, link_label, status, created_by_name,
	targets, sent, failed, invalid_removed, cursor_token_id, error, created_at, started_at, finished_at`

func scanBroadcast(row interface{ Scan(...any) error }) (*Broadcast, error) {
	var b Broadcast
	err := row.Scan(&b.ID, &b.TitleRU, &b.BodyRU, &b.TitleKY, &b.BodyKY, &b.Link, &b.LinkLabel, &b.Status,
		&b.CreatedByName, &b.Targets, &b.Sent, &b.Failed, &b.InvalidRemoved, &b.CursorTokenID, &b.Error,
		&b.CreatedAt, &b.StartedAt, &b.FinishedAt)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// Create validates in, resolves the link target and queues a broadcast.
// submitToken makes it idempotent: a second Create with the same token
// (double click, browser resubmit) returns the first broadcast's id with
// created=false and queues nothing.
func (r *Repo) Create(ctx context.Context, in Input, submitToken string, by Author) (id string, created bool, err error) {
	submitToken = strings.TrimSpace(submitToken)
	if submitToken == "" || len(submitToken) > 64 {
		return "", false, apperr.BadRequest("invalid_submit_token", "форма устарела — обновите страницу")
	}
	if err := in.normalize(); err != nil {
		return "", false, err
	}

	// A resubmit returns early, before re-validating a link whose target
	// may have changed since.
	if existing, err := r.idByToken(ctx, submitToken); err != nil || existing != "" {
		return existing, false, err
	}

	label, err := r.linkLabel(ctx, in.LinkType, in.LinkID)
	if err != nil {
		return "", false, err
	}

	var staffID *string
	if by.StaffID != "" {
		staffID = &by.StaffID
	}
	const q = `
		INSERT INTO broadcasts (title_ru, body_ru, title_ky, body_ky, link, link_label, submit_token, created_by, created_by_name)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (submit_token) DO NOTHING
		RETURNING id`
	err = r.db.QueryRowContext(ctx, q, in.TitleRU, in.BodyRU, in.TitleKY, in.BodyKY,
		linkPath(in.LinkType, in.LinkID), label, submitToken, staffID, by.Name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		// Lost a race with a concurrent submit of the same form.
		existing, err := r.idByToken(ctx, submitToken)
		return existing, false, err
	}
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

func (r *Repo) idByToken(ctx context.Context, token string) (string, error) {
	var id string
	err := r.db.QueryRowContext(ctx, `SELECT id FROM broadcasts WHERE submit_token = $1`, token).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// linkLabel checks the link target exists (an active product, any
// category) and returns its Russian name for the history list.
func (r *Repo) linkLabel(ctx context.Context, linkType, id string) (string, error) {
	var q string
	switch linkType {
	case LinkProduct:
		q = `SELECT name_ru FROM products WHERE id = $1 AND is_active = true`
	case LinkCategory:
		q = `SELECT name_ru FROM categories WHERE id = $1`
	default:
		return "", nil
	}
	var name string
	err := r.db.QueryRowContext(ctx, q, id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		if linkType == LinkProduct {
			return "", apperr.BadRequest("invalid_broadcast_link", "товар не найден или скрыт")
		}
		return "", apperr.BadRequest("invalid_broadcast_link", "категория не найдена")
	}
	return name, err
}

// Get returns one broadcast, or apperr.NotFound.
func (r *Repo) Get(ctx context.Context, id string) (*Broadcast, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, apperr.NotFound("broadcast_not_found", "рассылка не найдена")
	}
	b, err := scanBroadcast(r.db.QueryRowContext(ctx, `SELECT `+broadcastColumns+` FROM broadcasts WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.NotFound("broadcast_not_found", "рассылка не найдена")
	}
	return b, err
}

// List returns the newest broadcasts first (history).
func (r *Repo) List(ctx context.Context, limit int) ([]Broadcast, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+broadcastColumns+` FROM broadcasts ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Broadcast{}
	for rows.Next() {
		b, err := scanBroadcast(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// audienceWhere selects the device tokens a promo push goes to.
const audienceWhere = `c.promo_push = true AND c.deleted_at IS NULL`

// Audience counts the devices and customers a broadcast would reach now.
func (r *Repo) Audience(ctx context.Context) (devices, customers int, err error) {
	const q = `
		SELECT count(*), count(DISTINCT d.customer_id)
		FROM device_tokens d JOIN customers c ON c.id = d.customer_id
		WHERE ` + audienceWhere
	err = r.db.QueryRowContext(ctx, q).Scan(&devices, &customers)
	return devices, customers, err
}

// Option is one product/category choice for the admin link picker.
type Option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// CategoryOptions lists every category as "Parent / Child" labels.
func (r *Repo) CategoryOptions(ctx context.Context) ([]Option, error) {
	const q = `
		SELECT c.id, COALESCE(p.name_ru || ' / ', '') || c.name_ru AS label
		FROM categories c LEFT JOIN categories p ON p.id = c.parent_id
		ORDER BY label`
	return r.options(ctx, q)
}

// SearchProducts finds up to 20 active products whose RU/KY name or brand
// contains query (case-insensitive).
func (r *Repo) SearchProducts(ctx context.Context, query string) ([]Option, error) {
	query = strings.TrimSpace(query)
	if utf8.RuneCountInString(query) < 2 {
		return []Option{}, nil
	}
	pattern := "%" + likeEscaper.Replace(query) + "%"
	const q = `
		SELECT id, name_ru || COALESCE(' · ' || brand, '')
		FROM products
		WHERE is_active = true AND (name_ru ILIKE $1 OR name_ky ILIKE $1 OR brand ILIKE $1)
		ORDER BY name_ru
		LIMIT 20`
	return r.options(ctx, q, pattern)
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func (r *Repo) options(ctx context.Context, q string, args ...any) ([]Option, error) {
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Option{}
	for rows.Next() {
		var o Option
		if err := rows.Scan(&o.ID, &o.Label); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
