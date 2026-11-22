package catalog

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// MaxProductCodeLen caps a product code (runes): long enough for any
// supplier article, short enough to read out over the phone.
const MaxProductCodeLen = 40

// productCodeIndex is the unique index behind "one product per code"
// (migration 000034: lower(model_code)).
const productCodeIndex = "idx_products_model_code"

// NormalizeProductCode trims a typed product code. Blank returns nil —
// "no code given": Create then generates one (CZ-NNNNN, see migration
// 000051) and Update keeps the stored one. A code longer than
// MaxProductCodeLen or containing control characters is rejected.
func NormalizeProductCode(s string) (*string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(s) > MaxProductCodeLen || strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return nil, apperr.BadRequest("invalid_product_code", "код товара: до 40 символов, без переносов строк")
	}
	return &s, nil
}

// validateCodePtr checks an already-set ProductInput.Code.
func validateCodePtr(code *string) error {
	if code == nil {
		return nil
	}
	norm, err := NormalizeProductCode(*code)
	if err != nil {
		return err
	}
	if norm == nil || *norm != *code {
		return apperr.BadRequest("invalid_product_code", "код товара: до 40 символов, без переносов строк")
	}
	return nil
}

// ErrProductCodeTaken is the error for a code another product already
// has (codes are compared ignoring case).
func ErrProductCodeTaken() error {
	return apperr.Conflict("product_code_taken", "товар с таким кодом уже есть")
}

// IsProductCodeTaken reports whether err is the unique violation of the
// product code index.
func IsProductCodeTaken(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation && pgErr.ConstraintName == productCodeIndex
}
