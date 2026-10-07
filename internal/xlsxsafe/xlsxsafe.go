// Package xlsxsafe guards spreadsheet exports against formula (CSV)
// injection: a text cell built from shop data — a product, category or
// point name, an SKU, a colour — that starts with = + - @ TAB or CR could
// be evaluated as a formula by Excel/LibreOffice once the file is
// re-saved as CSV, pasted, or re-typed. Text prefixes such a value with
// an apostrophe (the OWASP-recommended neutralizer); Unescape is its exact
// inverse, used by the product importer so an exported/template file
// re-uploaded as-is round-trips unchanged.
package xlsxsafe

import "strings"

// quote is the neutralizing prefix.
const quote = "'"

// needsQuote reports whether s, ignoring any leading apostrophes, starts
// with a formula trigger. Looking past existing apostrophes keeps Text
// reversible: an already-quoted "'=x" gets a second apostrophe, and
// Unescape removes exactly that one, giving back "'=x".
func needsQuote(s string) bool {
	rest := strings.TrimLeft(s, quote)
	if rest == "" {
		return false
	}
	switch rest[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return true
	}
	return false
}

// Text returns s safe to write as a spreadsheet text cell.
func Text(s string) string {
	if needsQuote(s) {
		return quote + s
	}
	return s
}

// Unescape undoes Text: it drops the one apostrophe Text added and leaves
// every other value (including ordinary leading apostrophes) untouched.
func Unescape(s string) string {
	if strings.HasPrefix(s, quote) && needsQuote(s[len(quote):]) {
		return s[len(quote):]
	}
	return s
}

// Cell returns v with Text applied when v is a string; numbers and every
// other type pass through so they stay numeric cells.
func Cell(v any) any {
	if s, ok := v.(string); ok {
		return Text(s)
	}
	return v
}

// Row returns a copy of row with Cell applied to every value.
func Row(row []any) []any {
	out := make([]any, len(row))
	for i, v := range row {
		out[i] = Cell(v)
	}
	return out
}
