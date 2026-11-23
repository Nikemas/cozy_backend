package catalog

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

func TestNormalizeProductCode(t *testing.T) {
	cases := []struct {
		in      string
		want    string // "" = nil
		wantErr bool
	}{
		{"", "", false},
		{"   ", "", false},
		{"  CZ-00012 ", "CZ-00012", false},
		{"AB 123/4", "AB 123/4", false},
		{strings.Repeat("я", MaxProductCodeLen), strings.Repeat("я", MaxProductCodeLen), false},
		{strings.Repeat("x", MaxProductCodeLen+1), "", true},
		{"AB\n12", "", true},
		{"AB\t12", "", true},
	}
	for _, c := range cases {
		got, err := NormalizeProductCode(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("NormalizeProductCode(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if c.wantErr {
			var ae *apperr.AppError
			if !errors.As(err, &ae) || ae.Code != "invalid_product_code" {
				t.Errorf("NormalizeProductCode(%q) err = %v, want invalid_product_code", c.in, err)
			}
			continue
		}
		gotStr := ""
		if got != nil {
			gotStr = *got
		}
		if gotStr != c.want || (c.want == "") != (got == nil) {
			t.Errorf("NormalizeProductCode(%q) = %v, want %q", c.in, got, c.want)
		}
	}
}

func TestProductInputValidateRejectsUntrimmedCode(t *testing.T) {
	code := " CZ-1"
	in := ProductInput{CategoryID: "c", NameRu: "a", NameKy: "a", BasePrice: 1, Code: &code}
	if err := in.validate(); err == nil {
		t.Fatal("validate accepted an untrimmed code")
	}
	ok := "CZ-1"
	in.Code = &ok
	if err := in.validate(); err != nil {
		t.Fatalf("validate(CZ-1) = %v", err)
	}
}

func TestTranslateProductWriteErrMapsCodeConflict(t *testing.T) {
	dup := fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: pgUniqueViolation, ConstraintName: productCodeIndex})
	var ae *apperr.AppError
	if err := translateProductWriteErr(dup); !errors.As(err, &ae) || ae.Code != "product_code_taken" {
		t.Fatalf("translateProductWriteErr = %v, want product_code_taken", err)
	}
	other := &pgconn.PgError{Code: pgUniqueViolation, ConstraintName: "something_else"}
	if IsProductCodeTaken(other) {
		t.Fatal("another unique index is not a code conflict")
	}
}

func TestAdminListConditionsColorFilter(t *testing.T) {
	conds, args := buildAdminListConditions(AdminListFilter{Color: "Чёрный"})
	if len(conds) != 1 || !strings.Contains(conds[0], "pv.color = $1") || len(args) != 1 || args[0] != "Чёрный" {
		t.Fatalf("conds=%v args=%v", conds, args)
	}
}

func TestSearchConditionMatchesProductCode(t *testing.T) {
	cond, _ := searchCondition("CZ-00012", nil)
	if !strings.Contains(cond, "model_code ILIKE $1") {
		t.Fatalf("search condition does not look at the code: %s", cond)
	}
}
