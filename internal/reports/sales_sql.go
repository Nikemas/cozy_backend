package reports

import (
	"context"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// Sales is AggregateSales computed in Postgres: the same rows (same keys,
// counts and revenue, sorted by key) for orders created in [from, to),
// without loading every order and line item of the range into memory —
// a year of orders is tens of thousands of rows plus their items, while
// the result is at most a few hundred rows. AggregateSales stays the
// reference definition (and what the integration test compares against).
//
// Per groupBy, matching AggregateSales:
//   - day: Bishkek calendar day of created_at; revenue = total_amount,
//     items = the order's summed quantities;
//   - point: as day, keyed via pointNames (pointKey), rows of points that
//     share a display name merged;
//   - product: per product_name_snapshot line; revenue = price × quantity,
//     orders = distinct orders.
//
// Only orders that count as sales (SaleConditionSQL) are included.
func (r *Repo) Sales(ctx context.Context, from, to time.Time, groupBy GroupBy, pointNames map[string]string) ([]Row, error) {
	switch groupBy {
	case GroupByDay:
		// Grouped by the date value, formatted afterwards: hashing dates is
		// cheaper than sorting the formatted strings in the locale collation.
		return r.salesByOrder(ctx, `(o.created_at AT TIME ZONE '`+locationName+`')::date`, `to_char(k, 'YYYY-MM-DD')`, from, to, nil)
	case GroupByPoint:
		return r.salesByOrder(ctx, `o.point_id`, `COALESCE(k::text, '')`, from, to, func(pointID string) string {
			if pointID == "" {
				return UnknownPointKey
			}
			if name, ok := pointNames[pointID]; ok && name != "" {
				return name
			}
			return pointID
		})
	case GroupByProduct:
		return r.salesByProduct(ctx, from, to)
	default:
		return nil, apperr.BadRequest("invalid_group_by", "group_by должен быть одним из: day, product, point")
	}
}

// locationName is Location as Postgres names it (its own tz database, so
// the day boundaries match orderDayKey's).
const locationName = "Asia/Bishkek"

// salesByOrder groups whole orders by groupExpr (an SQL expression over
// orders o), then renders each group value k as text with keyExpr;
// mapKey, when set, maps that text to the report key (rows mapping to the
// same key are merged).
func (r *Repo) salesByOrder(ctx context.Context, groupExpr, keyExpr string, from, to time.Time, mapKey func(string) string) ([]Row, error) {
	q := `
		SELECT ` + keyExpr + ` AS key, order_count, item_count, revenue FROM (
			SELECT ` + groupExpr + ` AS k,
			       COUNT(*) AS order_count,
			       COALESCE(SUM(items.qty), 0) AS item_count,
			       COALESCE(SUM(o.total_amount), 0) AS revenue
			FROM orders o
			LEFT JOIN LATERAL (
				SELECT SUM(oi.quantity) AS qty FROM order_items oi WHERE oi.order_id = o.id
			) items ON true
			WHERE o.created_at >= $1 AND o.created_at < $2 AND ` + SaleConditionSQL + `
			GROUP BY 1
		) sales`
	return r.querySalesRows(ctx, q, from, to, mapKey)
}

func (r *Repo) salesByProduct(ctx context.Context, from, to time.Time) ([]Row, error) {
	const q = `
		SELECT oi.product_name_snapshot COLLATE "C" AS key,
		       COUNT(DISTINCT oi.order_id) AS order_count,
		       COALESCE(SUM(oi.quantity), 0) AS item_count,
		       COALESCE(SUM(oi.price * oi.quantity), 0) AS revenue
		FROM order_items oi
		JOIN orders o ON o.id = oi.order_id
		WHERE o.created_at >= $1 AND o.created_at < $2 AND ` + SaleConditionSQL + `
		GROUP BY 1`
	return r.querySalesRows(ctx, q, from, to, nil)
}

// Grouping text keys under COLLATE "C" groups exactly the same rows (the
// database collation is deterministic, so equal means byte-equal either
// way) but compares bytes instead of running the locale's collation for
// every one of the sort/aggregate comparisons — that alone was over half
// of a year-long report's time. Display order is decided in Go anyway.

// querySalesRows scans (key, order_count, item_count, revenue) rows,
// merges rows whose mapped key coincides and sorts by key exactly like
// AggregateSales (byte-wise, sort.Strings — not the database collation).
func (r *Repo) querySalesRows(ctx context.Context, q string, from, to time.Time, mapKey func(string) string) ([]Row, error) {
	rows, err := r.db.QueryContext(ctx, q, from, to)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	byKey := make(map[string]*Row)
	for rows.Next() {
		var row Row
		if err := rows.Scan(&row.Key, &row.OrderCount, &row.ItemCount, &row.Revenue); err != nil {
			return nil, err
		}
		if mapKey != nil {
			row.Key = mapKey(row.Key)
		}
		acc, ok := byKey[row.Key]
		if !ok {
			acc = &Row{Key: row.Key}
			byKey[row.Key] = acc
		}
		acc.OrderCount += row.OrderCount
		acc.ItemCount += row.ItemCount
		acc.Revenue += row.Revenue
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return sortedRows(byKey, func(_ string, a *Row) Row { return *a }), nil
}
