package i18n

import "testing"

func TestUpperFirst(t *testing.T) {
	cases := map[string]string{
		"":                "",
		"товар не найден": "Товар не найден",
		"өлчөм көрсөтүлгөн жок":  "Өлчөм көрсөтүлгөн жок",
		"Уже с заглавной":        "Уже с заглавной",
		"point_id обязателен":    "point_id обязателен", // an identifier stays as written
		"«кавычка» не трогается": "«кавычка» не трогается",
		"5 строк": "5 строк",
	}
	for in, want := range cases {
		if got := UpperFirst(in); got != want {
			t.Errorf("UpperFirst(%q) = %q, want %q", in, got, want)
		}
	}
}
