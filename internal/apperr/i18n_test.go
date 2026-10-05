package apperr

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type decodedError struct {
	status   int
	code     string
	message  string
	language string
}

func serveError(t *testing.T, req *http.Request, err error) decodedError {
	t.Helper()
	w := httptest.NewRecorder()
	WriteError(w, req, err)
	var body errorBody
	if decodeErr := json.Unmarshal(w.Body.Bytes(), &body); decodeErr != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), decodeErr)
	}
	return decodedError{status: w.Code, code: body.Code, message: body.Message, language: w.Header().Get("Content-Language")}
}

func TestErrorMessageFollowsAcceptLanguage(t *testing.T) {
	appErr := NotFound("order_not_found", "заказ не найден")
	for _, path := range []string{"/api/v1/orders/x", "/admin/api/orders/x"} {
		results := map[string]decodedError{}
		for _, lang := range []string{"ru", "ky"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Accept-Language", lang)
			results[lang] = serveError(t, req, appErr)
		}
		ru, ky := results["ru"], results["ky"]
		if ru.code != "order_not_found" || ky.code != ru.code || ky.status != http.StatusNotFound || ru.status != ky.status {
			t.Errorf("%s: code/status must not depend on language: ru=%+v ky=%+v", path, ru, ky)
		}
		if ru.message != "заказ не найден" || ru.language != "ru" {
			t.Errorf("%s: ru = %+v", path, ru)
		}
		if ky.message != "буйрутма табылган жок" || ky.language != "ky" {
			t.Errorf("%s: ky = %+v", path, ky)
		}
	}
}

func TestLangFromRequest(t *testing.T) {
	cases := []struct {
		name, target, acceptLang string
		cookie                   *http.Cookie
		want                     string
	}{
		{"default", "/api/v1/x", "", nil, "ru"},
		{"accept ky", "/api/v1/x", "ky", nil, "ky"},
		{"accept region", "/api/v1/x", "ky-KG,ru;q=0.5", nil, "ky"},
		{"q-values prefer ky", "/api/v1/x", "ru;q=0.4, ky;q=0.9, en;q=0.8", nil, "ky"},
		{"q-values prefer ru", "/api/v1/x", "ky;q=0.2, ru;q=0.7", nil, "ru"},
		{"unknown language -> ru", "/api/v1/x", "en-US,en;q=0.9", nil, "ru"},
		{"query beats header", "/api/v1/x?lang=ky", "ru", nil, "ky"},
		{"unknown query falls through", "/api/v1/x?lang=en", "ky", nil, "ky"},
		{"header beats cookie", "/api/v1/x", "ru", &http.Cookie{Name: "cozy_lang", Value: "ky"}, "ru"},
		{"site cookie", "/api/v1/x", "en", &http.Cookie{Name: "cozy_lang", Value: "ky"}, "ky"},
		{"admin cookie under /admin", "/admin/api/x", "", &http.Cookie{Name: "admin_lang", Value: "ky"}, "ky"},
		{"site cookie ignored under /admin", "/admin/api/x", "", &http.Cookie{Name: "cozy_lang", Value: "ky"}, "ru"},
		{"admin cookie ignored on site API", "/api/v1/x", "", &http.Cookie{Name: "admin_lang", Value: "ky"}, "ru"},
		{"bad cookie value", "/api/v1/x", "", &http.Cookie{Name: "cozy_lang", Value: "de"}, "ru"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, c.target, nil)
			if c.acceptLang != "" {
				req.Header.Set("Accept-Language", c.acceptLang)
			}
			if c.cookie != nil {
				req.AddCookie(c.cookie)
			}
			if got := LangFromRequest(req); got != c.want {
				t.Errorf("LangFromRequest = %q, want %q", got, c.want)
			}
		})
	}
}

func TestUnknownCodeKeepsMessageAndSaysRussian(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
	req.Header.Set("Accept-Language", "ky")
	got := serveError(t, req, Conflict("brand_new_code", "новая ошибка"))
	if got.code != "brand_new_code" || got.message != "новая ошибка" || got.language != "ru" || got.status != http.StatusConflict {
		t.Errorf("untranslated code = %+v, want the Russian message with Content-Language: ru", got)
	}
}

func TestLocalizedMessageFillsParams(t *testing.T) {
	appErr := Conflict("insufficient_stock", "товара «Кеды, 42» осталось только 1 шт.").
		WithVariant("left").WithParams(map[string]string{"name": "Кеды, 42", "count": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders", nil)
	req.Header.Set("Accept-Language", "ky")
	got := serveError(t, req, appErr)
	if want := "«Кеды, 42» товарынан 1 даана гана калды"; got.message != want || got.language != "ky" {
		t.Errorf("ky message = %+v, want %q", got, want)
	}
	if got.code != "insufficient_stock" {
		t.Errorf("code = %q", got.code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/orders", nil)
	if got := serveError(t, req, appErr); got.message != appErr.Message || got.language != "ru" {
		t.Errorf("ru message = %+v, want the original %q", got, appErr.Message)
	}
}

func TestWithVariantAndParamsDoNotMutate(t *testing.T) {
	base := BadRequest("invalid_name", "укажите имя")
	params := map[string]string{"x": "1"}
	v := base.WithVariant("too_long").WithParams(params)
	params["x"] = "2"
	if base.MessageKey() != "err.invalid_name" || base.params != nil {
		t.Errorf("base mutated: %+v", base)
	}
	if v.MessageKey() != "err.invalid_name.too_long" || v.params["x"] != "1" || v.Code != "invalid_name" || v.Status != http.StatusBadRequest {
		t.Errorf("variant = %+v", v)
	}
}

func TestAcceptJSONGetsJSONErrorOutsideAPIPaths(t *testing.T) {
	SetHTMLRenderer(func(w http.ResponseWriter, r *http.Request, e *AppError) bool {
		t.Fatal("HTML renderer must not be called when the client asks for JSON")
		return true
	})
	defer SetHTMLRenderer(nil)

	req := httptest.NewRequest(http.MethodPost, "/admin/products/import", nil)
	req.Header.Set("Accept", "application/json")
	req.AddCookie(&http.Cookie{Name: "admin_lang", Value: "ky"})
	got := serveError(t, req, BadRequest("empty_file", "файл импорта пустой"))
	if got.code != "empty_file" || got.message != "импорт файлы бош" || got.language != "ky" {
		t.Errorf("import error = %+v", got)
	}
}

func TestHTMLErrorPagesUnaffected(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/orders", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json;q=0.9")
	WriteError(w, req, NotFound("order_not_found", "заказ не найден"))
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("browser page got Content-Type %q, want HTML", ct)
	}
}
