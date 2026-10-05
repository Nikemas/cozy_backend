package i18n

import "testing"

func TestFromAcceptLanguage(t *testing.T) {
	cases := []struct {
		header   string
		wantLang string
		wantOK   bool
	}{
		{"ky", LangKY, true},
		{"ru", LangRU, true},
		{"ky-KG,ky;q=0.9", LangKY, true},
		{"ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7", LangRU, true},
		{"en-US,ky;q=0.5,ru;q=0.4", LangKY, true},
		{"ru;q=0.3, ky;q=0.8", LangKY, true},
		{"ky;q=0.8, ru", LangRU, true},     // missing q = 1
		{"ru, ky", LangRU, true},           // tie -> earlier entry
		{"ky;q=0, ru;q=0.1", LangRU, true}, // q=0 = not acceptable
		{"ky;q=0", "", false},
		{"KY-kg", LangKY, true},
		{"kir", LangKY, true},
		{"en-US,en;q=0.9", "", false},
		{"*", "", false},
		{"", "", false},
		{"ky;q=abc", LangKY, true},
	}
	for _, c := range cases {
		lang, ok := FromAcceptLanguage(c.header)
		if lang != c.wantLang || ok != c.wantOK {
			t.Errorf("FromAcceptLanguage(%q) = %q, %v; want %q, %v", c.header, lang, ok, c.wantLang, c.wantOK)
		}
	}
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{"ru": LangRU, "ru-RU": LangRU, "ky": LangKY, "ky_KG": LangKY, " KY ": LangKY}
	for in, want := range cases {
		if got, ok := Normalize(in); !ok || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "en", "*", "kz"} {
		if got, ok := Normalize(in); ok {
			t.Errorf("Normalize(%q) = %q, want unsupported", in, got)
		}
	}
}

func TestFill(t *testing.T) {
	got := Fill("товар «{name}»: осталось {n} шт., {missing}", map[string]string{"name": "Кеды", "n": "2"})
	if want := "товар «Кеды»: осталось 2 шт., {missing}"; got != want {
		t.Errorf("Fill = %q, want %q", got, want)
	}
	if got := Fill("без параметров", nil); got != "без параметров" {
		t.Errorf("Fill without params = %q", got)
	}
}
