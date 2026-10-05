package banner

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

func sp(s string) *string { return &s }

func validInput() Input {
	return Input{
		Enabled: true, TitleRU: "Доставка по Бишкеку", BgColor: "#fff3e9", TextColor: "#1A1A1A", EyebrowColor: "#b35400",
	}
}

func assertAppErr(t *testing.T, err error, status int, key string) {
	t.Helper()
	var ae *apperr.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("want *apperr.AppError, got %T (%v)", err, err)
	}
	if ae.Status != status || ae.MessageKey() != key {
		t.Errorf("got %d %s, want %d %s", ae.Status, ae.MessageKey(), status, key)
	}
}

func TestInputValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(in Input) Input
		wantKey string // "" = valid
	}{
		{"valid", func(in Input) Input { return in }, ""},
		{"disabled banner still needs a title", func(in Input) Input { in.Enabled = false; in.TitleRU = "  "; return in }, "err.banner_title_required"},
		{"enabled banner needs a russian title", func(in Input) Input { in.TitleRU = ""; return in }, "err.banner_title_required"},
		{"bad eyebrow color", func(in Input) Input { in.EyebrowColor = "orange"; return in }, "err.invalid_color.banner_eyebrow"},
		{"eyebrow at limit", func(in Input) Input { in.EyebrowKY = strings.Repeat("ы", MaxEyebrowLen); return in }, ""},
		{"eyebrow over limit", func(in Input) Input { in.EyebrowRU = strings.Repeat("ы", MaxEyebrowLen+1); return in }, "err.banner_text_too_long.eyebrow"},
		{"ky title over limit", func(in Input) Input { in.TitleKY = strings.Repeat("a", MaxTitleLen+1); return in }, "err.banner_text_too_long.title"},
		{"button over limit", func(in Input) Input { in.ButtonRU = strings.Repeat("a", MaxButtonLen+1); return in }, "err.banner_text_too_long.button"},
		{"short hex bg", func(in Input) Input { in.BgColor = "#FFF"; return in }, "err.invalid_color.banner_bg"},
		{"css injection in text color", func(in Input) Input { in.TextColor = "red;background:url(x)"; return in }, "err.invalid_color.banner_text"},
		{"category not a uuid", func(in Input) Input { in.LinkCategoryID = sp("sneakers"); return in }, "err.invalid_category_id"},
		{"category uuid", func(in Input) Input { in.LinkCategoryID = sp("6f1c1f9e-7a43-4c55-9d0b-0a4f3c1d2e3f"); return in }, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.mutate(validInput()).Normalized().Validate()
			if c.wantKey == "" {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			assertAppErr(t, err, http.StatusBadRequest, c.wantKey)
		})
	}
}

func TestNormalizedDoesNotMutateInput(t *testing.T) {
	blank := "  "
	in := Input{TitleRU: "  Заголовок ", BgColor: " #abcdef", LinkCategoryID: &blank}

	out := in.Normalized()

	if out.TitleRU != "Заголовок" || out.BgColor != "#ABCDEF" || out.LinkCategoryID != nil {
		t.Errorf("normalized = %+v", out)
	}
	if in.TitleRU != "  Заголовок " || in.BgColor != " #abcdef" || in.LinkCategoryID != &blank || blank != "  " {
		t.Errorf("input was modified: %+v", in)
	}
}

func TestTextsForFallsBackToRussianPerField(t *testing.T) {
	b := Banner{EyebrowRU: "Обувь", TitleRU: "Доставка", ButtonRU: "Смотреть", TitleKY: "Жеткирүү", ButtonKY: "  "}

	ky := b.TextsFor("ky")
	ru := b.TextsFor("ru")

	if ky != (Texts{Eyebrow: "Обувь", Title: "Жеткирүү", Button: "Смотреть"}) {
		t.Errorf("ky = %+v", ky)
	}
	if ru != (Texts{Eyebrow: "Обувь", Title: "Доставка", Button: "Смотреть"}) {
		t.Errorf("ru = %+v", ru)
	}
}

var bannerCols = []string{"enabled", "eyebrow_ru", "eyebrow_ky", "title_ru", "title_ky", "button_ru", "button_ky",
	"link_category_id", "slug", "bg_color", "text_color", "eyebrow_color", "image_key", "bg_image_key", "updated_at"}

func newMock(t *testing.T) (*Store, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStore(db), mock
}

func TestStoreGetJoinsCategorySlug(t *testing.T) {
	store, mock := newMock(t)
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`FROM home_banner b LEFT JOIN categories c ON c.id = b.link_category_id\s+WHERE b.id = 1`).
		WillReturnRows(sqlmock.NewRows(bannerCols).AddRow(true, "Обувь", "", "Доставка", "", "Смотреть", "Көрүү",
			"c1", "sneakers", "#FFF3E9", "#1A1A1A", "#B35400", "banners/a.png", nil, at))

	b, err := store.Get(context.Background())

	if err != nil {
		t.Fatal(err)
	}
	if !b.Enabled || b.TitleRU != "Доставка" || b.LinkCategorySlug == nil || *b.LinkCategorySlug != "sneakers" ||
		b.ImageKey == nil || *b.ImageKey != "banners/a.png" || b.BgImageKey != nil || !b.UpdatedAt.Equal(at) {
		t.Errorf("banner = %+v", b)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestStoreGetMissingRowIsDisabledDefault(t *testing.T) {
	store, mock := newMock(t)
	mock.ExpectQuery(`FROM home_banner`).WillReturnRows(sqlmock.NewRows(bannerCols))

	b, err := store.Get(context.Background())

	if err != nil {
		t.Fatal(err)
	}
	if b.Enabled || b.BgColor != DefaultBgColor || b.TextColor != DefaultTextColor || b.EyebrowColor != DefaultEyebrowColor {
		t.Errorf("banner = %+v", b)
	}
}

func TestStoreGetPassesThroughDBErrors(t *testing.T) {
	store, mock := newMock(t)
	boom := errors.New("connection reset")
	mock.ExpectQuery(`FROM home_banner`).WillReturnError(boom)

	if _, err := store.Get(context.Background()); !errors.Is(err, boom) {
		t.Errorf("err = %v, want passthrough", err)
	}
}

func TestStoreUpdateUpsertsNormalizedInput(t *testing.T) {
	store, mock := newMock(t)
	in := Input{Enabled: true, TitleRU: " Новинки ", ButtonRU: "Смотреть", BgColor: "#abcdef", TextColor: "#000000", EyebrowColor: "#ff0000",
		LinkCategoryID: sp(""), ImageKey: sp("banners/x.jpg")}
	mock.ExpectQuery(`INSERT INTO home_banner .* ON CONFLICT \(id\) DO UPDATE SET`).
		WithArgs(true, "", "", "Новинки", "", "Смотреть", "", nil, "#ABCDEF", "#000000", "#FF0000", "banners/x.jpg", nil).
		WillReturnRows(sqlmock.NewRows(bannerCols).AddRow(true, "", "", "Новинки", "", "Смотреть", "",
			nil, nil, "#ABCDEF", "#000000", "#FF0000", "banners/x.jpg", nil, time.Now()))

	b, err := store.Update(context.Background(), in)

	if err != nil {
		t.Fatal(err)
	}
	if b.TitleRU != "Новинки" || b.BgColor != "#ABCDEF" {
		t.Errorf("banner = %+v", b)
	}
	if in.TitleRU != " Новинки " {
		t.Error("Update modified the caller's input")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestStoreUpdateValidatesBeforeTouchingDB(t *testing.T) {
	store := NewStore(nil) // a nil *sql.DB would panic if reached

	_, err := store.Update(context.Background(), Input{Enabled: true, TitleRU: "T", BgColor: "blue", TextColor: "#000000", EyebrowColor: "#000000"})

	assertAppErr(t, err, http.StatusBadRequest, "err.invalid_color.banner_bg")
}

func TestStoreUpdateMapsMissingCategoryToBadRequest(t *testing.T) {
	store, mock := newMock(t)
	in := validInput()
	in.LinkCategoryID = sp("6f1c1f9e-7a43-4c55-9d0b-0a4f3c1d2e3f")
	mock.ExpectQuery(`INSERT INTO home_banner`).WillReturnError(&pgconn.PgError{Code: pgForeignKeyViolation})

	_, err := store.Update(context.Background(), in)

	assertAppErr(t, err, http.StatusBadRequest, "err.invalid_category_id")
}
