package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/audit"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/locales"
)

var apiMsgPlaceholder = regexp.MustCompile(`\{([a-z_]+)\}`)

// fillAPIMsg fills tmpl from args as decoded from JSONB; ok is false when
// an argument is missing.
func fillAPIMsg(tmpl string, args map[string]any) (string, bool) {
	ok := true
	out := apiMsgPlaceholder.ReplaceAllStringFunc(tmpl, func(m string) string {
		v, has := args[m[1:len(m)-1]]
		if !has {
			ok = false
			return m
		}
		return fmt.Sprint(v)
	})
	return out, ok
}

// TestAPIAuditEntriesCarryMessages: every JSON-API journal entry carries a
// summary message translated in both admin locales; the Russian one,
// with its "(API)" suffix, reproduces the stored Summary.
func TestAPIAuditEntriesCarryMessages(t *testing.T) {
	bundle, err := i18n.LoadFS(locales.Admin, "admin.%s.yaml")
	if err != nil {
		t.Fatal(err)
	}
	brand := "Nike"
	p := &catalog.Product{ID: "p1", NameRu: "Кеды", CategoryID: "c1", BasePrice: 4500, Brand: &brand}
	cat := &categoryRequest{NameRu: "Кеды", NameKy: "Кеды", Slug: "kedy"}
	vr := &variantRequest{Size: "42", Color: "Белый"}
	entries := []audit.Entry{
		categoryEntry(audit.ActionCategoryCreate, "c1", "Создана категория «Кеды» (API)", cat),
		categoryEntry(audit.ActionCategoryUpdate, "c1", "Изменена категория «Кеды» (API)", cat),
		categoryEntry(audit.ActionCategoryDelete, "c1", "Удалена категория (API)", nil),
		productEntry(audit.ActionProductCreate, p, "Создан товар «Кеды» (API)"),
		productEntry(audit.ActionProductUpdate, p, "Изменён товар «Кеды» (API)"),
		productDeletedEntry("p1"),
		productImagesEntry("p1", 3),
		variantEntry(audit.ActionVariantCreate, "p1", "v1", "42", "Белый", "Добавлена вариация 42 / Белый (API)", vr),
		variantEntry(audit.ActionVariantUpdate, "p1", "v1", "42", "Белый", "Изменена вариация 42 / Белый (API)", vr),
		variantEntry(audit.ActionVariantDelete, "p1", "v1", "42", "Белый", "Удалена вариация 42 / Белый (API)", nil),
	}
	entries = append(entries, apiStockEntry(t))

	for _, e := range entries {
		b, err := json.Marshal(e.StoredDetails())
		if err != nil {
			t.Fatal(err)
		}
		var d map[string]any
		if err := json.Unmarshal(b, &d); err != nil {
			t.Fatal(err)
		}
		key, _ := d[audit.DetailMsgKey].(string)
		if !slices.Contains(audit.SummaryKeys, key) || d[audit.DetailMsgVia] != audit.ViaAPI {
			t.Errorf("%s: details = %s", e.Action, b)
			continue
		}
		args, _ := d[audit.DetailMsgArgs].(map[string]any)
		for _, lang := range []string{i18n.LangRU, i18n.LangKY} {
			if !bundle.Has(lang, key) {
				t.Errorf("%s: key %q missing in admin.%s.yaml", e.Action, key, lang)
				continue
			}
			text, ok := fillAPIMsg(bundle.T(lang, key), args)
			if !ok {
				t.Errorf("%s: %s template %q lacks arguments %v", e.Action, lang, bundle.T(lang, key), args)
			}
			if lang == i18n.LangRU {
				if got := text + " " + bundle.T(lang, audit.ViaKeyPrefix+audit.ViaAPI); got != e.Summary {
					t.Errorf("%s: ru message %q, stored summary %q", e.Action, got, e.Summary)
				}
			}
		}
	}
}

func apiStockEntry(t *testing.T) audit.Entry {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	mock.ExpectQuery(`JOIN points_of_sale pos ON pos.id = \$2`).WithArgs("v1", "p1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "size", "color", "point"}).AddRow("pr1", "Кеды", "42", "Белый", "Дордой"))
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := stockJournalEntry(context.Background(), tx, "v1", "p1", 3, 5)
	if err != nil || len(entries) != 1 {
		t.Fatalf("stockJournalEntry = %+v, %v", entries, err)
	}
	return entries[0]
}
