package httpapi

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// adminOrderService is the subset of *orders.Service the admin order
// handlers depend on, so handler-level tests can inject a fake instead of
// a live database — mirrors stockUpserter in admin_catalog.go.
type adminOrderService interface {
	AdminListOrders(ctx context.Context, filter orders.AdminListFilter) ([]orders.Order, int, error)
	AdminGetOrder(ctx context.Context, idOrNumber string) (*orders.Order, error)
	AdminUpdateStatus(ctx context.Context, idOrNumber string, newStatus orders.OrderStatus) (*orders.Order, error)
}

// RegisterAdminOrdersRoutes mounts the admin order-management endpoints
// under /admin/api/*: listing with filters, one order's detail, and status
// transitions. RequireRole lets owner, manager AND point_staff all reach
// the handlers — point_staff's restriction to their own point (staff.
// PointID) is then enforced inside each handler, the same RBAC-nuance
// pattern as admin_catalog.go's updateStockHandler.
func RegisterAdminOrdersRoutes(mux *http.ServeMux, db *sql.DB, staffSvc *staff.Service) {
	svc := orders.NewService(db)

	allRoles := staffSvc.RequireRole(staff.RoleOwner, staff.RoleManager, staff.RolePointStaff)

	mux.Handle("GET /admin/api/orders", allRoles(apperr.Wrap(listAdminOrdersHandler(svc))))
	mux.Handle("GET /admin/api/orders/{id}", allRoles(apperr.Wrap(getAdminOrderHandler(svc))))
	mux.Handle("PUT /admin/api/orders/{id}/status", allRoles(apperr.Wrap(updateAdminOrderStatusHandler(svc))))
}

// adminOrderListResponse mirrors productListResponse in catalog.go.
type adminOrderListResponse struct {
	Items    []orders.Order `json:"items"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
	Total    int            `json:"total"`
}

func listAdminOrdersHandler(svc adminOrderService) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		st, ok := staff.FromContext(r.Context())
		if !ok {
			// Defensive: RequireRole should always have populated this.
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}

		filter, err := parseAdminOrderListFilter(r.URL.Query())
		if err != nil {
			return err
		}

		// point_staff sees only orders at their own point — silently
		// override whatever point_id query param was passed rather than
		// erroring, per the RBAC nuance.
		if st.Role == staff.RolePointStaff {
			if st.PointID == nil {
				// Defensive: a point_staff record should always have a
				// PointID. Fail closed (empty result) rather than leaking
				// every point's orders.
				return writeJSON(w, http.StatusOK, adminOrderListResponse{
					Items: []orders.Order{}, Page: filter.Page, PageSize: orders.AdminPageSize,
				})
			}
			filter.PointID = st.PointID
		}

		items, total, err := svc.AdminListOrders(r.Context(), filter)
		if err != nil {
			return err
		}

		return writeJSON(w, http.StatusOK, adminOrderListResponse{
			Items:    items,
			Page:     filter.Page,
			PageSize: orders.AdminPageSize,
			Total:    total,
		})
	}
}

// parseAdminOrderListFilter turns the query params of
// GET /admin/api/orders into an orders.AdminListFilter. Pure (no DB), so
// it's unit-testable on its own — see admin_orders_test.go.
func parseAdminOrderListFilter(q url.Values) (orders.AdminListFilter, error) {
	var filter orders.AdminListFilter

	if v := q.Get("status"); v != "" {
		status := orders.OrderStatus(v)
		switch status {
		case orders.StatusPlaced, orders.StatusConfirmed, orders.StatusCourierAssigned, orders.StatusDelivered, orders.StatusCancelled:
			filter.Status = &status
		default:
			return filter, apperr.BadRequest("invalid_status", "некорректный статус заказа")
		}
	}

	if v := q.Get("point_id"); v != "" {
		filter.PointID = &v
	}

	if v := q.Get("from"); v != "" {
		t, err := parseAdminDate(v)
		if err != nil {
			return filter, apperr.BadRequest("invalid_from", "некорректная дата from")
		}
		filter.From = &t
	}

	if v := q.Get("to"); v != "" {
		t, err := parseAdminDate(v)
		if err != nil {
			return filter, apperr.BadRequest("invalid_to", "некорректная дата to")
		}
		filter.To = &t
	}

	filter.Query = q.Get("q")

	filter.Page = 1
	if v := q.Get("page"); v != "" {
		page, err := strconv.Atoi(v)
		if err != nil || page < 1 {
			return filter, apperr.BadRequest("invalid_page", "некорректный page")
		}
		filter.Page = page
	}

	return filter, nil
}

// parseAdminDate accepts either RFC3339 ("2026-09-15T00:00:00Z") or a bare
// date ("2026-09-15", interpreted as UTC midnight) for the from/to filters.
func parseAdminDate(v string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t, nil
	}
	return time.Time{}, apperr.BadRequest("invalid_date", "дата должна быть в формате RFC3339 или YYYY-MM-DD")
}

// staffCanAccessOrderPoint reports whether st may see/act on an order at
// orderPointID: owner/manager always can; point_staff only when the order
// has a point_id and it matches their own — an order with no point_id
// (orderPointID == nil) is NOT visible to point_staff, only to
// owner/manager, same as a point mismatch.
func staffCanAccessOrderPoint(st *staff.Staff, orderPointID *string) bool {
	if st.Role != staff.RolePointStaff {
		return true
	}
	if st.PointID == nil || orderPointID == nil {
		return false
	}
	return *st.PointID == *orderPointID
}

// getAdminOrderHandler serves GET /admin/api/orders/{id}. {id} may be
// either the order's UUID or its human-readable order_number
// (COZY-YYYYMMDD-NNN) — orders.Service.AdminGetOrder already handles both,
// mirroring the customer-facing GET /api/v1/orders/{id} dual lookup.
func getAdminOrderHandler(svc adminOrderService) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		st, ok := staff.FromContext(r.Context())
		if !ok {
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}

		order, err := svc.AdminGetOrder(r.Context(), r.PathValue("id"))
		if err != nil {
			return err
		}

		if !staffCanAccessOrderPoint(st, order.PointID) {
			return apperr.Forbidden("forbidden", "сотрудник точки может просматривать только заказы своей точки")
		}

		return writeJSON(w, http.StatusOK, order)
	}
}

// updateOrderStatusRequest is the PUT /admin/api/orders/{id}/status body.
type updateOrderStatusRequest struct {
	Status orders.OrderStatus `json:"status"`
}

// updateAdminOrderStatusHandler serves PUT /admin/api/orders/{id}/status.
// It re-checks the same point_staff-own-point restriction as
// getAdminOrderHandler before applying the transition — a point_staff
// member must not be able to change the status of another point's order
// any more than they can view it.
func updateAdminOrderStatusHandler(svc adminOrderService) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		st, ok := staff.FromContext(r.Context())
		if !ok {
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}

		id := r.PathValue("id")

		existing, err := svc.AdminGetOrder(r.Context(), id)
		if err != nil {
			return err
		}
		if !staffCanAccessOrderPoint(st, existing.PointID) {
			return apperr.Forbidden("forbidden", "сотрудник точки может изменять только заказы своей точки")
		}

		var req updateOrderStatusRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		order, err := svc.AdminUpdateStatus(r.Context(), id, req.Status)
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, order)
	}
}
