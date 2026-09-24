package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/storefront"
)

func postLang(t *testing.T, h *handlers, lang, customerID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/lang", strings.NewReader("lang="+lang))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if customerID != "" {
		req = req.WithContext(context.WithValue(req.Context(), customerIDKey, customerID))
	}
	w := httptest.NewRecorder()
	if err := h.setLang(w, req); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestSetLangSavesLanguageForLoggedInCustomer(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE customers SET lang = $2")).
		WithArgs("cust-1", "ky").WillReturnResult(sqlmock.NewResult(0, 1))

	h := &handlers{customers: storefront.NewCustomerRepo(db)}
	w := postLang(t, h, "ky", "cust-1")

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	if c := w.Header().Get("Set-Cookie"); !strings.Contains(c, langCookieName+"=ky") {
		t.Errorf("Set-Cookie = %q, want the language cookie", c)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestSetLangAnonymousOnlySetsCookie(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	h := &handlers{customers: storefront.NewCustomerRepo(db)}
	w := postLang(t, h, "ru", "")
	if !strings.Contains(w.Header().Get("Set-Cookie"), langCookieName+"=ru") {
		t.Error("cookie not set")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("anonymous visitor must not touch the DB: %v", err)
	}
}
