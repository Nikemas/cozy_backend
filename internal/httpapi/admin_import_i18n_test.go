package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/catalog"
)

// The import page's fetch sends Accept: application/json and the page's
// language; file-level errors must then be JSON in that language.
func TestImportFileErrorsFollowPageLanguage(t *testing.T) {
	cases := []struct {
		name, filename, body, wantCode string
		wantMessage                    map[string]string
	}{
		{"unsupported format", "products.txt", "x", "unsupported_format", map[string]string{
			"ru": "поддерживаются только файлы .csv и .xlsx",
			"ky": ".csv жана .xlsx файлдары гана колдоого алынат",
		}},
		{"empty file", "products.csv", "", "empty_file", map[string]string{
			"ru": "файл импорта пустой",
			"ky": "импорт файлы бош",
		}},
		{"missing columns", "products.csv", "name_ru,brand\nКеды,Cozy\n", "missing_columns", map[string]string{
			"ru": "в файле нет обязательных колонок: Категория (category), Цена (price) — скачайте шаблон импорта",
			"ky": "файлда милдеттүү тилкелер жок: Категория (category), Цена (price) — импорттун үлгүсүн жүктөп алыңыз",
		}},
	}
	handler := apperr.Wrap(importProductsHandler(newImportDeps(), nil))
	for _, c := range cases {
		for _, lang := range []string{"ru", "ky"} {
			req := newImportRequest(t, c.filename, "", c.body)
			req.Header.Set("Accept", "application/json")
			req.Header.Set("Accept-Language", lang)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			var body struct{ Code, Message string }
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("%s/%s: not JSON: %q", c.name, lang, rec.Body.String())
			}
			if rec.Code != http.StatusBadRequest || body.Code != c.wantCode || body.Message != c.wantMessage[lang] {
				t.Errorf("%s/%s: %d %+v, want 400 %s %q", c.name, lang, rec.Code, body, c.wantCode, c.wantMessage[lang])
			}
			if got := rec.Header().Get("Content-Language"); got != lang {
				t.Errorf("%s/%s: Content-Language = %q", c.name, lang, got)
			}
		}
	}
}

func TestImportRowMessagesFollowPageLanguage(t *testing.T) {
	handler := apperr.Wrap(importProductsHandler(newImportDeps(), nil))
	req := newImportRequestWithFields(t, "products.csv", "", "name_ru,category,price\nКеды,sneakers,\n",
		map[string]string{"dry_run": "1"})
	req.AddCookie(&http.Cookie{Name: "admin_lang", Value: "ky"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var res catalog.ImportResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0].Message != "Баасы көрсөтүлгөн жок" {
		t.Errorf("rows = %+v, want the Kyrgyz \"no price\" message", res.Rows)
	}
}
