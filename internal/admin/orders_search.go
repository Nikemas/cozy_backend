// orders_search.go backs the Заказы list's filters: search by order number
// or customer phone, point of sale, status, and a preset or custom date
// range. orders.Service.AdminListOrders can't search by phone (it has no
// customers join), so the list uses this screen-specific read model —
// same columns and page size as AdminListOrders — instead.
package admin

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/reports"
)

// orderSearchFilter is the resolved (validated) list filter.
type orderSearchFilter struct {
	Status  *orders.OrderStatus
	PointID *string
	From    *time.Time // inclusive
	To      *time.Time // exclusive
	Query   string
	Page    int
}

// ordersListParams is the raw query string state of the list page, kept
// verbatim so every link (chips, pager) preserves the other filters.
type ordersListParams struct {
	Status string
	Range  string // "all" | "7" | "30" | "custom"
	From   string // YYYY-MM-DD, custom range only
	To     string
	Point  string
	Q      string
	Page   int
}

func parseOrdersListParams(q url.Values) ordersListParams {
	p := ordersListParams{
		Status: q.Get("status"),
		Range:  q.Get("range"),
		From:   strings.TrimSpace(q.Get("from")),
		To:     strings.TrimSpace(q.Get("to")),
		Point:  q.Get("point"),
		Q:      strings.TrimSpace(q.Get("q")),
		Page:   parsePositiveInt(q.Get("page"), 1),
	}
	if p.Range == "" {
		p.Range = "all"
	}
	if len([]rune(p.Q)) > 100 {
		p.Q = string([]rune(p.Q)[:100])
	}
	return p
}

// URL renders p as /admin/orders?..., omitting defaults.
func (p ordersListParams) URL() string {
	v := url.Values{}
	if p.Status != "" {
		v.Set("status", p.Status)
	}
	if p.Range != "" && p.Range != "all" {
		v.Set("range", p.Range)
		if p.Range == "custom" {
			if p.From != "" {
				v.Set("from", p.From)
			}
			if p.To != "" {
				v.Set("to", p.To)
			}
		}
	}
	if p.Point != "" {
		v.Set("point", p.Point)
	}
	if p.Q != "" {
		v.Set("q", p.Q)
	}
	if p.Page > 1 {
		v.Set("page", strconv.Itoa(p.Page))
	}
	if len(v) == 0 {
		return "/admin/orders"
	}
	return "/admin/orders?" + v.Encode()
}

func validOrderStatus(s string) bool {
	switch orders.OrderStatus(s) {
	case orders.StatusPlaced, orders.StatusConfirmed, orders.StatusCourierAssigned, orders.StatusDelivered, orders.StatusCancelled:
		return true
	}
	return false
}

// resolveOrderFilter validates p into a filter. Invalid values never reach
// SQL (an unknown status used to be cast to the order_status enum and
// 500): they are dropped from p and reported in notes instead.
func resolveOrderFilter(p *ordersListParams, now time.Time) (orderSearchFilter, []string) {
	var notes []string
	f := orderSearchFilter{Query: p.Q, Page: p.Page}

	if p.Status != "" {
		if validOrderStatus(p.Status) {
			s := orders.OrderStatus(p.Status)
			f.Status = &s
		} else {
			notes = append(notes, "Неизвестный статус — показаны заказы во всех статусах")
			p.Status = ""
		}
	}

	if p.Point != "" {
		pt := p.Point
		f.PointID = &pt
	}

	switch p.Range {
	case "all":
	case "7", "30":
		days, _ := strconv.Atoi(p.Range)
		loc := reports.Location
		local := now.In(loc)
		from := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -(days - 1))
		f.From = &from
	case "custom":
		if p.From != "" {
			from, err := reports.ParseReportDate(p.From)
			if err != nil {
				notes = append(notes, "Дата «с» не распознана")
				p.From = ""
			} else {
				f.From = &from
			}
		}
		if p.To != "" {
			to, err := reports.ParseReportDate(p.To)
			if err != nil {
				notes = append(notes, "Дата «по» не распознана")
				p.To = ""
			} else {
				end := to.AddDate(0, 0, 1)
				f.To = &end
			}
		}
		if f.From != nil && f.To != nil && !f.From.Before(*f.To) {
			notes = append(notes, "Дата «с» позже даты «по»")
		}
	default:
		p.Range = "all"
	}
	return f, notes
}

// phoneDigits returns the digits of q when q looks like (part of) a phone
// number — at least 3 digits and nothing but digits/phone punctuation —
// else "".
func phoneDigits(q string) string {
	var b strings.Builder
	for _, r := range q {
		switch {
		case unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '+' || r == ' ' || r == '-' || r == '(' || r == ')':
		default:
			return ""
		}
	}
	if b.Len() < 3 {
		return ""
	}
	return b.String()
}

// Search returns one page of orders matching f, newest first, plus the
// total number of matches.
func (r *orderListMetaRepo) Search(ctx context.Context, f orderSearchFilter) ([]orders.Order, int, error) {
	page := f.Page
	if page < 1 {
		page = 1
	}
	if maxPage := 1_000_000 / orders.AdminPageSize; page > maxPage {
		page = maxPage
	}

	conds := []string{"TRUE"}
	var args []any
	add := func(cond string, arg any) {
		args = append(args, arg)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.Status != nil {
		add("o.status = $%d", string(*f.Status))
	}
	if f.PointID != nil {
		add("o.point_id = $%d", *f.PointID)
	}
	if f.From != nil {
		add("o.created_at >= $%d", *f.From)
	}
	if f.To != nil {
		add("o.created_at < $%d", *f.To)
	}
	if f.Query != "" {
		args = append(args, "%"+escapeLike(f.Query)+"%")
		numberArg := len(args)
		if digits := phoneDigits(f.Query); digits != "" {
			args = append(args, "%"+digits+"%")
			conds = append(conds, fmt.Sprintf(
				"(o.order_number ILIKE $%d OR regexp_replace(c.phone, '[^0-9]', '', 'g') LIKE $%d)", numberArg, len(args)))
		} else {
			conds = append(conds, fmt.Sprintf("o.order_number ILIKE $%d", numberArg))
		}
	}

	from := `FROM orders o JOIN customers c ON c.id = o.customer_id WHERE ` + strings.Join(conds, " AND ")

	var total int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) "+from, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	listArgs := append(append([]any{}, args...), orders.AdminPageSize, (page-1)*orders.AdminPageSize)
	q := fmt.Sprintf(`
		SELECT o.id, o.order_number, o.customer_id, o.address_id, o.point_id, o.status, o.payment_method,
		       o.payment_status, o.total_amount, o.comment, o.created_at, o.updated_at
		%s
		ORDER BY o.created_at DESC
		LIMIT $%d OFFSET $%d`, from, len(args)+1, len(args)+2)

	rows, err := r.db.QueryContext(ctx, q, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	list := []orders.Order{}
	for rows.Next() {
		var o orders.Order
		if err := rows.Scan(&o.ID, &o.OrderNumber, &o.CustomerID, &o.AddressID, &o.PointID, &o.Status, &o.PaymentMethod,
			&o.PaymentStatus, &o.TotalAmount, &o.Comment, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, 0, err
		}
		list = append(list, o)
	}
	return list, total, rows.Err()
}
