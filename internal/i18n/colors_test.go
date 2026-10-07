package i18n

import "testing"

func TestColorLabel(t *testing.T) {
	tests := []struct {
		name  string
		lang  string
		color string
		want  string
	}{
		{"ru returns input unchanged", LangRU, "Чёрный", "Чёрный"},
		{"unknown lang returns input unchanged", "en", "Чёрный", "Чёрный"},
		{"ky capitalized input keeps capital", LangKY, "Чёрный", "Кара"},
		{"ky lowercase input stays lowercase", LangKY, "белый", "ак"},
		{"ky treats е as ё", LangKY, "Черный", "Кара"},
		{"ky trims spaces", LangKY, "  Белый  ", "Ак"},
		{"ky all caps input stays all caps", LangKY, "БЕЛЫЙ", "АК"},
		{"ky multi-word label", LangKY, "Молочный", "Сүт түстүү"},
		{"ky dark compound", LangKY, "Тёмно-синий", "Кочкул көк"},
		{"ky dark compound without ё", LangKY, "темно-зеленый", "кочкул жашыл"},
		{"ky light compound", LangKY, "Светло-серый", "Ачык боз"},
		{"ky compound with spaced hyphen", LangKY, "тёмно - серый", "кочкул боз"},
		{"ky unknown color unchanged", LangKY, "Терракотовый", "Терракотовый"},
		{"ky unknown compound unchanged", LangKY, "Тёмно-терракотовый", "Тёмно-терракотовый"},
		{"ky empty stays empty", LangKY, "", ""},
		{"ky blank keeps input", LangKY, "  ", "  "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ColorLabel(tt.lang, tt.color); got != tt.want {
				t.Fatalf("ColorLabel(%q, %q) = %q, want %q", tt.lang, tt.color, got, tt.want)
			}
		})
	}
}

// TestColorLabelCoversDemoCatalog pins the colors the demo catalog
// (cozy_e2e product_variants) actually uses: each must translate.
func TestColorLabelCoversDemoCatalog(t *testing.T) {
	demo := []string{
		"Тёмно-синий", "Голубой", "Фиолетовый", "Тёмно-зелёный", "Зелёный",
		"Тёмно-серый", "Бежевый", "Синий", "Розовый", "Пудровый", "Серый",
		"Красный", "Чёрный", "Молочный", "Жёлтый", "Хаки", "Бордовый",
		"Коричневый", "Белый",
	}
	for _, c := range demo {
		got := ColorLabel(LangKY, c)
		if got == c && c != "Хаки" {
			t.Errorf("ColorLabel(ky, %q) has no translation", c)
		}
	}
}
