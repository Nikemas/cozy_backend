package storefront

import (
	"context"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// SetName's blank-name check runs before any database access, so it's
// testable with a nil *sql.DB — the rest of SetName (and every other
// CustomerRepo method) needs a live Postgres, not available in this
// environment (see the Task 4 handoff report).
func TestSetNameRejectsBlankName(t *testing.T) {
	r := NewCustomerRepo(nil)
	if err := r.SetName(context.Background(), "some-id", "   "); err == nil {
		t.Fatal("SetName with a blank name: got nil error, want one")
	}
}

func TestUpdateProfileValidation(t *testing.T) {
	r := NewCustomerRepo(nil) // validation runs before any DB access
	blank, bad := "  ", "en"
	if _, err := r.UpdateProfile(context.Background(), "id", ProfileUpdate{Name: &blank}); err == nil {
		t.Error("blank name: want error")
	}
	if _, err := r.UpdateProfile(context.Background(), "id", ProfileUpdate{Lang: &bad}); err == nil {
		t.Error("lang=en: want invalid_lang error")
	}
	if err := r.SetLang(context.Background(), "id", "de"); err == nil {
		t.Error("SetLang(de): want error")
	}
}

func TestUpdateProfilePartialSingleUpdate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	lang, off := "ky", false
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE customers SET")).
		WithArgs("c1", nil, "ky", false).
		WillReturnRows(sqlmock.NewRows([]string{"id", "phone", "name", "lang", "promo_push"}).
			AddRow("c1", "+996700000000", "Айбек", "ky", false))

	c, err := NewCustomerRepo(db).UpdateProfile(context.Background(), "c1", ProfileUpdate{Lang: &lang, PromoPush: &off})
	if err != nil {
		t.Fatal(err)
	}
	if c.Lang != "ky" || c.PromoPush || c.Name == nil || *c.Name != "Айбек" {
		t.Errorf("customer = %+v", c)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestSetLangUpdatesOnlyWhenChanged(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE customers SET lang = $2, updated_at = now() WHERE id = $1 AND lang <> $2")).
		WithArgs("c1", "ky").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := NewCustomerRepo(db).SetLang(context.Background(), "c1", "ky"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
