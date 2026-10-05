package catalog

import (
	"fmt"
	"strings"
)

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// searchCondition builds the WHERE fragment shared by the storefront, the
// REST API and the admin list for a free-text query. Each whitespace-
// separated token must match (case-insensitively, Latin or Cyrillic) the
// RU/KY name, the brand or any variant SKU; tokens are ANDed so
// "nike air" finds a Nike product named "Air Max". Values are bound as
// parameters with LIKE wildcards escaped. It returns "" when q is blank.
func searchCondition(q string, args []any) (string, []any) {
	tokens := strings.Fields(q)
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
