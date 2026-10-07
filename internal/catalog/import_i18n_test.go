package catalog

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/i18n"
)

func TestImportRowMessagesFollowLang(t *testing.T) {
	csv := "article,name_ru,category,price,size,color,sku\n" +
		"A1,Модель A,sneakers,1000,38,red,A1-38\n" +
		"A1,,,-5,40,red,A1-40\n" +
		"C1,Модель C,nope,1000,38,red,C1-38\n"
	cases := []struct {
		lang                               string
		wantNegative, wantSkipped, wantCat string
	}{
		{i18n.LangRU, `Цена не может быть отрицательной: "-5"`, "Модель не импортирована из-за ошибки в строке 3", `Категория "nope" не найдена`},
		{i18n.LangKY, `Баасы терс болбошу керек: "-5"`, "Модель 3-саптагы катадан улам импорттолгон жок", `Категория "nope" табылган жок`},
		{"en", `Цена не может быть отрицательной: "-5"`, "Модель не импортирована из-за ошибки в строке 3", `Категория "nope" не найдена`},
	}
	for _, c := range cases {
		t.Run(c.lang, func(t *testing.T) {
			res := runCSV(t, newMemStore(), csv, ImportOptions{DryRun: true, Lang: c.lang})
			if r := rowStatus(t, res, 3); r.Status != RowStatusError || r.Message != c.wantNegative {
				t.Errorf("row 3 = %+v, want message %q", r, c.wantNegative)
			}
			if r := rowStatus(t, res, 2); r.Status != RowStatusSkipped || r.Message != c.wantSkipped {
				t.Errorf("row 2 = %+v, want message %q", r, c.wantSkipped)
			}
			if r := rowStatus(t, res, 4); r.Status != RowStatusError || r.Message != c.wantCat {
				t.Errorf("row 4 = %+v, want message %q", r, c.wantCat)
			}
		})
	}
}

func TestImportNestedRowMessageIsTranslated(t *testing.T) {
	csv := "name_ru,category,price,price_override,size,color\nКеды,sneakers,100,abc,38,red\n"
	res := runCSV(t, newMemStore(), csv, ImportOptions{DryRun: true, Lang: i18n.LangKY})
	want := `Вариациянын баасы: баасы сан эмес: "abc"`
	if r := rowStatus(t, res, 2); r.Message != want {
		t.Errorf("row 2 message = %q, want %q", r.Message, want)
	}
}

// TestImportMessageKeysAreTranslated: every "import.*" key the importer
// uses exists in both locales/errors.*.yaml.
func TestImportMessageKeysAreTranslated(t *testing.T) {
	files, err := filepath.Glob("import*.go")
	if err != nil {
		t.Fatal(err)
	}
	keyRE := regexp.MustCompile(`"(import\.[a-z_]+)"`)
	found := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range keyRE.FindAllStringSubmatch(string(src), -1) {
			found++
			for _, lang := range []string{i18n.LangRU, i18n.LangKY} {
				if !apperr.HasTranslation(lang, m[1]) {
					t.Errorf("%s: %q missing from locales/errors.%s.yaml", f, m[1], lang)
				}
			}
		}
	}
	if found < 20 {
		t.Fatalf("found only %d import.* keys — scan broken?", found)
	}
}
