package broadcasts

import (
	"context"
	"database/sql/driver"
	"errors"
	"regexp"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

const productID = "3f1c2d4e-5a6b-4c7d-8e9f-0a1b2c3d4e5f"

func TestInputValidation(t *testing.T) {
	long := strings.Repeat("я", MaxTitleLen+1)
	cases := map[string]Input{
		"missing RU body":   {TitleRU: "t"},
		"title too long":    {TitleRU: long, BodyRU: "b"},
		"KY title too long": {TitleRU: "t", BodyRU: "b", TitleKY: long},
		"bad product id":    {TitleRU: "t", BodyRU: "b", LinkType: LinkProduct, LinkID: "nope"},
		"unknown link type": {TitleRU: "t", BodyRU: "b", LinkType: "url", LinkID: productID},
	}
	for name, in := range cases {
		if err := in.normalize(); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	ok := Input{TitleRU: "  Скидки ", BodyRU: "b", LinkType: LinkNone, LinkID: "ignored"}
	if err := ok.normalize(); err != nil || ok.TitleRU != "Скидки" || ok.LinkID != "" {
		t.Errorf("normalize = %+v, %v", ok, err)
	}
}

func TestLinkPath(t *testing.T) {
	if got := linkPath(LinkProduct, "p"); got != "/product/p" {
		t.Error(got)
	}
	if got := linkPath(LinkCategory, "c"); got != "/catalog?category=c" {
		t.Error(got)
	}
	if got := linkPath(LinkNone, ""); got != "" {
		t.Error(got)
	}
}

func TestCreateQueuesOnceAndIsIdempotentPerSubmitToken(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	repo := NewRepo(db)
	in := Input{TitleRU: "Скидки", BodyRU: "−20%", LinkType: LinkProduct, LinkID: productID}

	// First submit: no row for the token, product exists → inserted.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM broadcasts WHERE submit_token = $1")).
		WithArgs("tok").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name_ru FROM products WHERE id = $1 AND is_active = true")).
		WithArgs(productID).WillReturnRows(sqlmock.NewRows([]string{"name_ru"}).AddRow("Кроссовки"))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO broadcasts")).
		WithArgs("Скидки", "−20%", "", "", "/product/"+productID, "Кроссовки", "tok", "staff-1", "Айгерим").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("b1"))

	id, created, err := repo.Create(context.Background(), in, "tok", Author{StaffID: "staff-1", Name: "Айгерим"})
	if err != nil || id != "b1" || !created {
		t.Fatalf("first Create = %q, %v, %v", id, created, err)
	}

	// Resubmit of the same form: returns b1, inserts nothing.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM broadcasts WHERE submit_token = $1")).
		WithArgs("tok").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("b1"))
	id, created, err = repo.Create(context.Background(), in, "tok", Author{StaffID: "staff-1", Name: "Айгерим"})
	if err != nil || id != "b1" || created {
		t.Fatalf("resubmit Create = %q, %v, %v; want b1, false", id, created, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestCreateRejectsMissingProductAndToken(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	repo := NewRepo(db)
	in := Input{TitleRU: "t", BodyRU: "b", LinkType: LinkProduct, LinkID: productID}

	if _, _, err := repo.Create(context.Background(), in, "", Author{}); err == nil {
		t.Error("empty submit token: want error")
	}

	mock.ExpectQuery("SELECT id FROM broadcasts").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery("SELECT name_ru FROM products").WillReturnRows(sqlmock.NewRows([]string{"name_ru"}))
	_, _, err = repo.Create(context.Background(), in, "tok", Author{})
	var ae *apperr.AppError
	if !errors.As(err, &ae) || ae.Code != "invalid_broadcast_link" {
		t.Fatalf("err = %v, want invalid_broadcast_link", err)
	}
}

// anyArg matches any argument (the uuid cursor may be nil).
type anyArg struct{}

func (anyArg) Match(driver.Value) bool { return true }

func TestRecordBatchDeletesInvalidTokensAndAdvancesCursorInOneTx(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.ValueConverterOption(sliceConverter{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM device_tokens WHERE fcm_token = ANY($1)")).
		WithArgs([]string{"dead"}).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE broadcasts SET sent = sent + $2")).
		WithArgs("b1", 3, 1, 1, "cursor-id").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err = NewRepo(db).RecordBatch(context.Background(), "b1",
		BatchResult{Sent: 3, Failed: 1, InvalidRemoved: 1, Cursor: "cursor-id"}, []string{"dead"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestNextTargetsFiltersAudience(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectQuery(`c\.promo_push = true AND c\.deleted_at IS NULL AND \(\$1::uuid IS NULL OR d\.id > \$1::uuid\)`).
		WithArgs(anyArg{}, 200).
		WillReturnRows(sqlmock.NewRows([]string{"id", "fcm_token", "lang"}).AddRow("d1", "tok", "ky"))
	got, err := NewRepo(db).NextTargets(context.Background(), nil, 200)
	if err != nil || len(got) != 1 || got[0].Lang != "ky" {
		t.Fatalf("targets = %+v, %v", got, err)
	}
}

type sliceConverter struct{}

func (sliceConverter) ConvertValue(v any) (driver.Value, error) {
	if s, ok := v.([]string); ok {
		return s, nil
	}
	return driver.DefaultParameterConverter.ConvertValue(v)
}
