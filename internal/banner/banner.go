// Package banner is the promo banner at the top of the site's home page
// and the mobile app's home screen (table home_banner, migration 000039):
// a single row the owner/manager edits from the admin panel
// (/admin/banner), rendered by internal/web's shop screen and served to
// the app by GET /api/v1/banner (internal/httpapi/banner.go).
//
// One banner, not a carousel (YAGNI): Get always returns the singleton,
// Update replaces every writable field of it.
package banner

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/i18n"
)

// Length limits, in characters — the same numbers as the CHECKs in
// migration 000039.
const (
	MaxEyebrowLen = 60
	MaxTitleLen   = 120
	MaxButtonLen  = 30
)

// Default colors — the site's --cozy-accent-bg / --cozy-ink tokens
// (web/static/css/site.css), also the column defaults in 000039.
const (
	DefaultBgColor   = "#FFF3E9"
	DefaultTextColor = "#1A1A1A"
)

// hexColor is the only color format accepted: "#RRGGBB". Anything else
// is rejected at the boundary, which is what makes the value safe to put
// into an inline style attribute on the site.
var hexColor = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// Banner mirrors the home_banner row, plus the slug of the linked
// category (joined in, so the site and the app can build a link without a
// second query).
type Banner struct {
	Enabled          bool
	EyebrowRU        string
	EyebrowKY        string
	TitleRU          string
	TitleKY          string
	ButtonRU         string
	ButtonKY         string
	LinkCategoryID   *string
	LinkCategorySlug *string
	BgColor          string
	TextColor        string
	ImageKey         *string
	BgImageKey       *string
	UpdatedAt        time.Time
}

// Texts are the banner's texts in one language.
type Texts struct {
	Eyebrow string
	Title   string
	Button  string
}

// TextsFor returns the banner's texts in lang. Kyrgyz falls back to the
// Russian text field by field, so a half-translated banner never shows an
// empty title; any language other than ky gets Russian.
func (b Banner) TextsFor(lang string) Texts {
	ru := Texts{Eyebrow: b.EyebrowRU, Title: b.TitleRU, Button: b.ButtonRU}
	if lang != i18n.LangKY {
		return ru
	}
	return Texts{
		Eyebrow: orFallback(b.EyebrowKY, ru.Eyebrow),
		Title:   orFallback(b.TitleKY, ru.Title),
		Button:  orFallback(b.ButtonKY, ru.Button),
	}
}

func orFallback(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// notConfigured is what Get returns when the singleton row is missing
// (it is seeded by 000039, so only after a manual DELETE): a disabled
// banner with the default colors — the site then shows its static texts
// and the app hides the banner, and the admin form still opens and
// recreates the row on save.
func notConfigured() *Banner {
	return &Banner{BgColor: DefaultBgColor, TextColor: DefaultTextColor}
}

// Input carries the writable fields of the banner.
type Input struct {
	Enabled        bool
	EyebrowRU      string
	EyebrowKY      string
	TitleRU        string
	TitleKY        string
	ButtonRU       string
	ButtonKY       string
	LinkCategoryID *string // nil = the whole catalog
	BgColor        string
	TextColor      string
	ImageKey       *string
	BgImageKey     *string
}

// Normalized returns a copy of in with texts trimmed, colors upper-cased,
// and empty optional ids/keys turned into nil. in itself is not modified.
func (in Input) Normalized() Input {
	out := in
	out.EyebrowRU = strings.TrimSpace(in.EyebrowRU)
	out.EyebrowKY = strings.TrimSpace(in.EyebrowKY)
	out.TitleRU = strings.TrimSpace(in.TitleRU)
	out.TitleKY = strings.TrimSpace(in.TitleKY)
	out.ButtonRU = strings.TrimSpace(in.ButtonRU)
	out.ButtonKY = strings.TrimSpace(in.ButtonKY)
	out.BgColor = strings.ToUpper(strings.TrimSpace(in.BgColor))
	out.TextColor = strings.ToUpper(strings.TrimSpace(in.TextColor))
	out.LinkCategoryID = nilIfBlank(in.LinkCategoryID)
	out.ImageKey = nilIfBlank(in.ImageKey)
	out.BgImageKey = nilIfBlank(in.BgImageKey)
	return out
}

func nilIfBlank(p *string) *string {
	if p == nil {
		return nil
	}
	v := strings.TrimSpace(*p)
	if v == "" {
		return nil
	}
	return &v
}

// Validate checks in (expected already Normalized) against the limits of
// migration 000039, returning an apperr.BadRequest the admin form shows
// as is. Called by Store.Update too; exported so the admin handler can
// reject a bad form before uploading any picture.
func (in Input) Validate() error {
	if in.Enabled && in.TitleRU == "" {
		return apperr.BadRequest("banner_title_required", "укажите заголовок баннера на русском")
	}
	if err := checkLen(in.EyebrowRU, in.EyebrowKY, MaxEyebrowLen, "eyebrow"); err != nil {
		return err
	}
	if err := checkLen(in.TitleRU, in.TitleKY, MaxTitleLen, "title"); err != nil {
		return err
	}
	if err := checkLen(in.ButtonRU, in.ButtonKY, MaxButtonLen, "button"); err != nil {
		return err
	}
	if !hexColor.MatchString(in.BgColor) {
		return apperr.BadRequest("invalid_color", "цвет фона баннера должен быть в формате #RRGGBB").WithVariant("banner_bg")
	}
	if !hexColor.MatchString(in.TextColor) {
		return apperr.BadRequest("invalid_color", "цвет текста баннера должен быть в формате #RRGGBB").WithVariant("banner_text")
	}
	if in.LinkCategoryID != nil {
		if _, err := uuid.Parse(*in.LinkCategoryID); err != nil {
			return errUnknownCategory()
		}
	}
	return nil
}

// checkLen rejects a RU or KY text longer than limit characters — one
// variant of banner_text_too_long per field (literal messages, so the
// apperr AST test can match them against locales/errors.ru.yaml).
func checkLen(ru, ky string, limit int, variant string) error {
	if utf8.RuneCountInString(ru) <= limit && utf8.RuneCountInString(ky) <= limit {
		return nil
	}
	switch variant {
	case "eyebrow":
		return apperr.BadRequest("banner_text_too_long", "надзаголовок баннера длиннее 60 символов").WithVariant("eyebrow")
	case "title":
		return apperr.BadRequest("banner_text_too_long", "заголовок баннера длиннее 120 символов").WithVariant("title")
	default:
		return apperr.BadRequest("banner_text_too_long", "текст кнопки баннера длиннее 30 символов").WithVariant("button")
	}
}

func errUnknownCategory() error {
	return apperr.BadRequest("invalid_category_id", "категория не найдена")
}
