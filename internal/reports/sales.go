// Package reports builds the admin sales report (§8/§14 of the ТЗ):
// orders aggregated by day, product or point of sale over a date range,
// served as JSON and as an Excel export. The aggregation math lives here as
// pure functions over []orders.Order — no database dependency, so it's
// fully unit-testable — while the SQL that loads those orders lives in
// repo.go.
package reports

import (
	"sort"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/orders"
)

// GroupBy selects how SalesReport buckets orders.
type GroupBy string

const (
	GroupByDay     GroupBy = "day"
	GroupByProduct GroupBy = "product"
	GroupByPoint   GroupBy = "point"
)

// ParseGroupBy validates the group_by query parameter. A pure function so
// the validation rule is unit-testable without spinning up an HTTP request.
func ParseGroupBy(s string) (GroupBy, error) {
	switch GroupBy(s) {
	case GroupByDay, GroupByProduct, GroupByPoint:
		return GroupBy(s), nil
	default:
		return "", apperr.BadRequest("invalid_group_by", "group_by должен быть одним из: day, product, point")
	}
}

// dateLayout is the wire format for from/to query params — plain calendar
// dates, no time-of-day or timezone (§8 ТЗ just asks for "a date range").
const dateLayout = "2006-01-02"

// ParseReportDate parses a "YYYY-MM-DD" query param into a UTC midnight
// time.Time. A pure function, unit-testable without a database.
func ParseReportDate(s string) (time.Time, error) {
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return time.Time{}, apperr.BadRequest("invalid_date", "дата должна быть в формате YYYY-MM-DD")
	}
	return t, nil
}

// ValidateRange checks that from <= to. A pure function so this rule is
// unit-testable without a database or HTTP request.
func ValidateRange(from, to time.Time) error {
	if from.After(to) {
		return apperr.BadRequest("invalid_range", "дата начала не может быть позже даты окончания")
	}
	return nil
}

// Row is one line of the aggregated report: revenue and counts for one key
// (a calendar day, a product name, or a point-of-sale name), per GroupBy.
type Row struct {
	Key        string
	OrderCount int
	ItemCount  int
	Revenue    float64
}

// AggregateSales buckets ordersList by groupBy and returns one Row per
// distinct key, sorted by Key ascending (chronological for "day",
// alphabetical for "product"/"point" — deterministic either way, which
// matters for JSON/Excel output and for tests).
//
// Cancelled orders are excluded entirely — not just from revenue, but from
// every number in the report (order/item counts too). See the package's
// Deviations note in tasks/todo.md for why: the brief only spells out
// "excluded from revenue", but a report row that counts a cancelled order
// toward order_count/item_count while reporting zero revenue for it reads
// as a contradiction ("3 orders, $0") rather than useful data, so the
// simpler, fully-excluded reading was chosen.
//
// pointNames resolves a point_id to a display name for GroupByPoint; a
// missing or nil entry falls back to the raw point_id (or "unknown" if the
// order has no point at all). It's a plain map, not a DB call, so the
// function stays pure.
func AggregateSales(ordersList []orders.Order, groupBy GroupBy, pointNames map[string]string) ([]Row, error) {
	switch groupBy {
	case GroupByDay:
		return aggregateByOrder(ordersList, orderDayKey), nil
	case GroupByPoint:
		return aggregateByOrder(ordersList, func(o orders.Order) string {
			return pointKey(o, pointNames)
		}), nil
	case GroupByProduct:
		return aggregateByItem(ordersList), nil
	default:
		return nil, apperr.BadRequest("invalid_group_by", "group_by должен быть одним из: day, product, point")
	}
}

func orderDayKey(o orders.Order) string {
	return o.CreatedAt.UTC().Format(dateLayout)
}

func pointKey(o orders.Order, pointNames map[string]string) string {
	if o.PointID == nil || *o.PointID == "" {
		return "unknown"
	}
	if name, ok := pointNames[*o.PointID]; ok && name != "" {
		return name
	}
	return *o.PointID
}

// aggregateByOrder buckets whole orders by keyFn — used for "day" and
// "point", where the natural unit is one order (its already-computed
// TotalAmount, and the sum of its line quantities).
func aggregateByOrder(ordersList []orders.Order, keyFn func(orders.Order) string) []Row {
	type acc struct {
		orderCount int
		itemCount  int
		revenue    float64
	}
	byKey := make(map[string]*acc)

	for _, o := range ordersList {
		if o.Status == orders.StatusCancelled {
			continue
		}
		key := keyFn(o)
		a, ok := byKey[key]
		if !ok {
			a = &acc{}
			byKey[key] = a
		}
		a.orderCount++
		a.revenue += o.TotalAmount
		for _, it := range o.Items {
			a.itemCount += it.Quantity
		}
	}

	return sortedRows(byKey, func(k string, a *acc) Row {
		return Row{Key: k, OrderCount: a.orderCount, ItemCount: a.itemCount, Revenue: a.revenue}
	})
}

// aggregateByItem buckets individual order line items by product name
// snapshot — used for "product", where the natural unit is a line item, not
// a whole order (one order can contain several products).
func aggregateByItem(ordersList []orders.Order) []Row {
	type acc struct {
		orders    map[string]bool
		itemCount int
		revenue   float64
	}
	byKey := make(map[string]*acc)

	for _, o := range ordersList {
		if o.Status == orders.StatusCancelled {
			continue
		}
		for _, it := range o.Items {
			key := it.ProductNameSnapshot
			a, ok := byKey[key]
			if !ok {
				a = &acc{orders: make(map[string]bool)}
				byKey[key] = a
			}
			a.orders[o.ID] = true
			a.itemCount += it.Quantity
			a.revenue += it.Price * float64(it.Quantity)
		}
	}

	return sortedRows(byKey, func(k string, a *acc) Row {
		return Row{Key: k, OrderCount: len(a.orders), ItemCount: a.itemCount, Revenue: a.revenue}
	})
}

// sortedRows converts a key->accumulator map into a Row slice sorted by key,
// factored out so both aggregation branches produce deterministic output the
// same way.
func sortedRows[A any](byKey map[string]*A, toRow func(string, *A) Row) []Row {
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	rows := make([]Row, 0, len(keys))
	for _, k := range keys {
		rows = append(rows, toRow(k, byKey[k]))
	}
	return rows
}
