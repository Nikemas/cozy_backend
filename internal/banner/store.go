package banner

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// pgForeignKeyViolation: link_category_id names a category that doesn't
// exist (deleted between the form render and the save).
const pgForeignKeyViolation = "23503"

// Store reads and writes the home_banner singleton.
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// selectColumns reads a banner row b joined with its linked category c.
const selectColumns = `
	b.enabled, b.eyebrow_ru, b.eyebrow_ky, b.title_ru, b.title_ky, b.button_ru, b.button_ky,
	b.link_category_id::text, c.slug, b.bg_color, b.text_color, b.image_key, b.bg_image_key, b.updated_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanBanner(row rowScanner) (*Banner, error) {
	var b Banner
	err := row.Scan(&b.Enabled, &b.EyebrowRU, &b.EyebrowKY, &b.TitleRU, &b.TitleKY, &b.ButtonRU, &b.ButtonKY,
		&b.LinkCategoryID, &b.LinkCategorySlug, &b.BgColor, &b.TextColor, &b.ImageKey, &b.BgImageKey, &b.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// Get returns the banner. A missing row (only after a manual DELETE — the
// migration seeds it) is not an error: see notConfigured.
func (s *Store) Get(ctx context.Context) (*Banner, error) {
	const q = `SELECT ` + selectColumns + `
		FROM home_banner b LEFT JOIN categories c ON c.id = b.link_category_id
		WHERE b.id = 1`

	b, err := scanBanner(s.db.QueryRowContext(ctx, q))
	if errors.Is(err, sql.ErrNoRows) {
		return notConfigured(), nil
	}
	return b, err
}

// Update validates in and replaces every writable field of the banner
// (creating the row if it is missing), returning the banner as stored.
// in is normalized first (trimmed texts, upper-case colors), on a copy.
func (s *Store) Update(ctx context.Context, in Input) (*Banner, error) {
	in = in.Normalized()
	if err := in.Validate(); err != nil {
		return nil, err
	}

	const q = `
		WITH b AS (
			INSERT INTO home_banner (id, enabled, eyebrow_ru, eyebrow_ky, title_ru, title_ky, button_ru, button_ky,
			                         link_category_id, bg_color, text_color, image_key, bg_image_key, updated_at)
			VALUES (1, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now())
			ON CONFLICT (id) DO UPDATE SET
				enabled = EXCLUDED.enabled,
				eyebrow_ru = EXCLUDED.eyebrow_ru, eyebrow_ky = EXCLUDED.eyebrow_ky,
				title_ru = EXCLUDED.title_ru, title_ky = EXCLUDED.title_ky,
				button_ru = EXCLUDED.button_ru, button_ky = EXCLUDED.button_ky,
				link_category_id = EXCLUDED.link_category_id,
				bg_color = EXCLUDED.bg_color, text_color = EXCLUDED.text_color,
				image_key = EXCLUDED.image_key, bg_image_key = EXCLUDED.bg_image_key,
				updated_at = now()
			RETURNING *
		)
		SELECT ` + selectColumns + `
		FROM b LEFT JOIN categories c ON c.id = b.link_category_id`

	b, err := scanBanner(s.db.QueryRowContext(ctx, q,
		in.Enabled, in.EyebrowRU, in.EyebrowKY, in.TitleRU, in.TitleKY, in.ButtonRU, in.ButtonKY,
		in.LinkCategoryID, in.BgColor, in.TextColor, in.ImageKey, in.BgImageKey))
	if pgErrCode(err) == pgForeignKeyViolation {
		return nil, errUnknownCategory()
	}
	return b, err
}

func pgErrCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}
