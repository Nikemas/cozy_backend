package admin

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/internal/reports"
)

// formErrorCodes are apperr codes that services return into admin HTML
// forms through appErrMessage. Each needs an admin.apperr.<code> entry,
// or the Kyrgyz page falls back to the Russian service message.
var formErrorCodes = []string{
	// points.PointInput.validate (Точки продаж form, incl. city/coords from bf2d1ea)
	"invalid_name", "invalid_city", "invalid_address", "invalid_coordinates", "point_not_found", "point_in_use",
	// orders.Service.AdminUpdateStatus (order detail / bulk status)
	"invalid_status_transition", "payment_not_completed", "cancel_forbidden", "forbidden",
	// productStore / productOpsStore (product form, bulk actions, stock)
	"invalid_category_id", "invalid_name_ru", "invalid_name_ky", "invalid_base_price", "invalid_variant",
	"product_not_found", "variant_in_use", "variant_exists", "invalid_quantity", "invalid_variant_or_point",
	// reports period
	"invalid_date", "invalid_range", "range_too_long",
}

func TestFormErrorCodesTranslatedToKyrgyz(t *testing.T) {
	for _, code := range formErrorCodes {
		key := "admin.apperr." + code
		if !adminBundle.Has(i18n.LangKY, key) || !adminBundle.Has(i18n.LangRU, key) {
			t.Errorf("missing %s in admin.ru.yaml/admin.ky.yaml", key)
		}
	}
}

func TestAuditPointDetailsUseLocalizedFieldLabels(t *testing.T) {
	// Arrange: the details a point edit journals since bf2d1ea.
	details := map[string]any{
		"city":          map[string]any{"from": "Osh", "to": "Bishkek"},
		"working_hours": map[string]any{"from": "", "to": "10-20"},
		"coordinates":   map[string]any{"from": ", ", "to": "42.87, 74.59"},
	}

	// Act
	got := auditDetailsText(kyTr, details)

	// Assert
	for _, raw := range []string{"city:", "working_hours:", "coordinates:"} {
		if strings.Contains(got, raw) {
			t.Errorf("raw field name %q in %q", raw, got)
		}
	}
	for _, want := range []string{"шаар: Osh → Bishkek", "иш убактысы: — → 10-20"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in %q", want, got)
		}
	}
}

func TestTopBrandsLabelsNoBrandInPageLanguage(t *testing.T) {
	rows := []reports.Row{{Key: "Nike", Revenue: 2}, {Key: "", Revenue: 1}}

	ru, ky := buildTopBrands(ruTr, rows), buildTopBrands(kyTr, rows)

	if ru[0].Name != "Nike" || ky[0].Name != "Nike" {
		t.Errorf("real brand renamed: ru=%q ky=%q", ru[0].Name, ky[0].Name)
	}
	if ru[1].Name != "Без бренда" {
		t.Errorf("ru no-brand = %q", ru[1].Name)
	}
	if ky[1].Name != kyTr.T("admin.reports.no_brand") || ky[1].Name == ru[1].Name {
		t.Errorf("ky no-brand = %q", ky[1].Name)
	}
}

// templateCyrillicAllowed is Cyrillic that may stay literal in admin
// templates. It is empty: example values/placeholders, proper names and
// the currency suffix all go through locales/admin.*.yaml too
// (fix/admin-polish), so a Kyrgyz page never shows a stray literal.
var templateCyrillicAllowed = map[string]bool{}

var (
	tmplComment   = regexp.MustCompile(`(?s)\{\{-?\s*/\*.*?\*/\s*-?\}\}|<!--.*?-->`)
	tmplAction    = regexp.MustCompile(`(?s)\{\{.*?\}\}`)
	jsLineComment = regexp.MustCompile(`(?m)^\s*//.*$`)
	cyrillicRun   = regexp.MustCompile(`[\p{Cyrillic}][\p{Cyrillic}\p{L}\d %.,—–\-]*[\p{Cyrillic}.]|[\p{Cyrillic}]`)
)

// TestTemplatesHaveNoHardcodedRussian: outside template actions and
// comments, admin templates contain no Cyrillic except the allowlist —
// a new hardcoded label would show up untranslated on the Kyrgyz page.
func TestTemplatesHaveNoHardcodedRussian(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(repoRoot(t), templatesDir, "*.gohtml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no admin templates found (err=%v)", err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		body := visibleText(string(raw)) // drops the CYR_TO_LAT slug table
		body = tmplComment.ReplaceAllString(body, "")
		body = tmplAction.ReplaceAllString(body, "")
		body = jsLineComment.ReplaceAllString(body, "")
		var bad []string
		for _, m := range cyrillicRun.FindAllString(body, -1) {
			if !templateCyrillicAllowed[strings.TrimSpace(m)] {
				bad = append(bad, m)
			}
		}
		sort.Strings(bad)
		if len(bad) > 0 {
			t.Errorf("%s: hardcoded Cyrillic UI text (move to locales/admin.*.yaml): %q", filepath.Base(f), bad)
		}
	}
}

// standaloneErrorKey matches admin locale keys whose text is shown on its
// own as an error/flash line (not composed mid-sentence).
var standaloneErrorKey = regexp.MustCompile(`^admin\.(apperr|err)\.|_failed$|^admin\.(delivery|audit)\.err_|^admin\.staff\.passwords_mismatch$|^admin\.product\.err_stale_form$|^admin\.bulk\.already_in_status$`)

// TestStandaloneAdminErrorsAreCapitalized: an error line starts with a
// capital letter in both languages (fix/admin-polish).
func TestStandaloneAdminErrorsAreCapitalized(t *testing.T) {
	for _, lang := range []string{i18n.LangRU, i18n.LangKY} {
		for _, key := range adminBundle.Keys(lang) {
			if !standaloneErrorKey.MatchString(key) || key == "admin.broadcasts.stat_failed" {
				continue
			}
			if v := adminBundle.T(lang, key); i18n.UpperFirst(v) != v {
				t.Errorf("admin.%s.yaml %s starts lowercase: %q", lang, key, v)
			}
		}
	}
}
