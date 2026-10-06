package catalog

import (
	"fmt"
	"strings"
)

// Search input limits, enforced in searchTokens so every caller of
// searchCondition (REST /products and /products/facets, the website
// catalog, the admin product list) gets them: each token adds an ILIKE
// over four columns plus a variant subquery, so an unbounded q is a cheap
// way to make the database do a lot of work.
const (
	maxSearchQueryRunes = 100 // longer q is truncated, not rejected
	maxSearchTokens     = 5   // extra tokens are dropped, not rejected
)

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// searchCondition builds the WHERE fragment shared by the storefront, the
// REST API and the admin list for a free-text query. Each whitespace-
// separated token must match (case-insensitively, Latin or Cyrillic) the
// RU/KY name, the brand or any variant SKU; tokens are ANDed so
// "nike air" finds a Nike product named "Air Max". Values are bound as
// parameters with LIKE wildcards escaped. It returns "" when q is blank.
func searchCondition(q string, args []any) (string, []any) {
	tokens := searchTokens(q)
	if len(tokens) == 0 {
		return "", args
	}
	parts := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		args = append(args, "%"+likeEscaper.Replace(tok)+"%")
		i := len(args)
		parts = append(parts, fmt.Sprintf(
			`(name_ru ILIKE $%[1]d OR name_ky ILIKE $%[1]d OR brand ILIKE $%[1]d OR EXISTS (SELECT 1 FROM product_variants pv WHERE pv.product_id = products.id AND pv.sku ILIKE $%[1]d))`, i))
	}
	return strings.Join(parts, " AND "), args
}

// searchTokens truncates q to maxSearchQueryRunes runes, splits it on
// whitespace and keeps at most the first maxSearchTokens tokens.
func searchTokens(q string) []string {
	q = truncateRunes(q, maxSearchQueryRunes)
	tokens := strings.Fields(q)
	if len(tokens) > maxSearchTokens {
		tokens = tokens[:maxSearchTokens]
	}
	return tokens
}

// truncateRunes returns the first n runes of s without converting the
// whole (possibly huge) string to a rune slice.
func truncateRunes(s string, n int) string {
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}
