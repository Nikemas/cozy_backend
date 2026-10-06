package storefront

import (
	"context"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

var customerRowColumns = []string{"id", "phone", "name", "lang", "promo_push", "promo_push_asked"}

// Setting promo_push through the API is the customer's explicit answer to
// the opt-in prompt, so the same UPDATE stamps promo_push_asked_at.
func TestUpdateProfileStampsPromoPushAskedAt(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	on := true
	mock.ExpectQuery(`promo_push_asked_at = CASE WHEN \$4::boolean IS NULL THEN promo_push_asked_at ELSE now\(\) END`).
		WithArgs("c1", nil, nil, true).
		WillReturnRows(sqlmock.NewRows(customerRowColumns).
			AddRow("c1", "+996700000000", nil, "ru", true, true))

	c, err := NewCustomerRepo(db).UpdateProfile(context.Background(), "c1", ProfileUpdate{PromoPush: &on})
	if err != nil {
		t.Fatal(err)
	}
	if !c.PromoPush || !c.PromoPushAsked {
		t.Errorf("customer = %+v, want promo_push and promo_push_asked", c)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestGetByIDScansPromoPushAsked(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(regexp.QuoteMeta(`promo_push_asked_at IS NOT NULL`)).
		WithArgs("c1").
		WillReturnRows(sqlmock.NewRows(customerRowColumns).
			AddRow("c1", "+996700000000", nil, "ru", false, false))

	c, err := NewCustomerRepo(db).GetByID(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if c == nil || c.PromoPush || c.PromoPushAsked {
		t.Errorf("customer = %+v, want promo_push=false, not asked", c)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
