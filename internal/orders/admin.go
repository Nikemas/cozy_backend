package orders

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// AdminPageSize is the fixed page size for GET /admin/api/orders — this
// wave doesn't expose a page_size query param, mirroring catalog.
// DefaultPageSize's fixed-size approach in internal/httpapi/catalog.go.
const AdminPageSize = 50

// AdminListFilter carries the query params accepted by
// GET /admin/api/orders. PointID is already resolved by the handler (a
// point_staff caller has it forced to their own point, see
// internal/httpapi/admin_orders.go) so this type itself has no RBAC
// awareness.
type AdminListFilter struct {
	Status  *OrderStatus
	PointID *string
	From    *time.Time
	To      *time.Time
	Query   string // substring match on order_number
	Page    int    // 1-based
}

// AdminListOrders returns orders matching filter (parent rows only, no
// nested Items — mirrors how internal/httpapi/catalog.go's product list
// returns rows without nested variants), newest first, plus the total
// count of matching rows for pagination.
func (s *Service) AdminListOrders(ctx context.Context, filter AdminListFilter) ([]Order, int, error) {
	page := filter.Page
	if page < 1 {
		page = 1
	}

	conditions := []string{"1 = 1"}
	var args []any

	if filter.Status != nil {
		args = append(args, *filter.Status)
		conditions = append(conditions, fmt.Sprintf("status = $%d", len(args)))
	}
	if filter.PointID != nil {
		args = append(args, *filter.PointID)
		conditions = append(conditions, fmt.Sprintf("point_id = $%d", len(args)))
	}
	if filter.From != nil {
		args = append(args, *filter.From)
		conditions = append(conditions, fmt.Sprintf("created_at >= $%d", len(args)))
	}
	if filter.To != nil {
		args = append(args, *filter.To)
		conditions = append(conditions, fmt.Sprintf("created_at <= $%d", len(args)))
	}
	if filter.Query != "" {
		args = append(args, "%"+filter.Query+"%")
		conditions = append(conditions, fmt.Sprintf("order_number ILIKE $%d", len(args)))
	}

	where := "WHERE " + strings.Join(conditions, " AND ")

	var total int
	countQuery := "SELECT COUNT(*) FROM orders " + where
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limitArgs := append(append([]any{}, args...), AdminPageSize, adminSafeOffset(page))
	listQuery := fmt.Sprintf(`
		SELECT `+orderColumns+`
		FROM orders
		%s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d`, where, len(args)+1, len(args)+2)

	rows, err := s.db.QueryContext(ctx, listQuery, limitArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	list := []Order{}
	for rows.Next() {
		var o Order
		if err := scanOrder(rows, &o); err != nil {
			return nil, 0, err
		}
		list = append(list, o)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	return list, total, nil
}

// adminSafeOffset mirrors catalog.safeOffset: it computes the SQL OFFSET
// for a page/AdminPageSize pair without overflowing when page is
// adversarially large (e.g. a `?page=` query param near math.MaxInt).
func adminSafeOffset(page int) int {
	if page <= 1 {
		return 0
	}
	if maxPage := math.MaxInt32 / AdminPageSize; page > maxPage {
		page = maxPage
	}
	return (page - 1) * AdminPageSize
}

// AdminGetOrder returns one order (with its Items) by UUID or
// order_number, with no customer scoping — admin callers may look up any
// order. Point-based RBAC (point_staff restricted to their own point) is
// enforced by the caller (internal/httpapi/admin_orders.go), not here,
// since this method has no notion of the calling staff member.
//
// The admin view also carries the order's status history.
func (s *Service) AdminGetOrder(ctx context.Context, idOrNumber string) (*Order, error) {
	o, err := s.getOrderAnyCustomer(ctx, idOrNumber)
	if err != nil {
		return nil, err
	}
	hist, err := s.StatusHistory(ctx, o.ID)
	if err != nil {
		return nil, err
	}
	o.History = hist
	return o, nil
}

// getOrderAnyCustomer is AdminGetOrder without the history.
func (s *Service) getOrderAnyCustomer(ctx context.Context, idOrNumber string) (*Order, error) {
	q := `SELECT ` + orderColumns + ` FROM orders WHERE ` + orderKeyPredicate(idOrNumber, 1)

	var o Order
	row := s.db.QueryRowContext(ctx, q, idOrNumber)
	if err := scanOrderRow(row, &o); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("order_not_found", "заказ не найден")
		}
		return nil, err
	}

	list := []Order{o}
	if err := s.attachItems(ctx, list); err != nil {
		return nil, err
	}
	return &list[0], nil
}

// validStatusTransition is the order status state machine: only these
// transitions are valid —
//
//	placed           -> confirmed, cancelled
//	confirmed        -> courier_assigned, cancelled
//	courier_assigned -> delivered, cancelled
//	delivered        -> (terminal, nothing)
//	cancelled        -> (terminal, nothing)
//
// A pure function (no DB) so it's unit-testable in isolation — see
// admin_test.go for the table-driven test covering every combination.
func validStatusTransition(from, to OrderStatus) bool {
	if from == to {
		return false
	}
	switch from {
	case StatusPlaced:
		return to == StatusConfirmed || to == StatusCancelled
	case StatusConfirmed:
		return to == StatusCourierAssigned || to == StatusCancelled
	case StatusCourierAssigned:
		return to == StatusDelivered || to == StatusCancelled
	default:
		// StatusDelivered and StatusCancelled are terminal; anything else
		// (including an unrecognized from value) is invalid.
		return false
	}
}
