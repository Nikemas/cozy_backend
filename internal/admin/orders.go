// orders.go implements the Заказы screens (Wave 4 Task 3): the order list
// (Cozy Admin.dc.html lines 405-497) and order detail with status-change
// buttons (lines 499-556) — replacing Foundation's stub content for
// /admin/orders and adding /admin/orders/{id} + the status-change POST.
//
// Both pages call *orders.Service (internal/orders/admin.go, Wave 3)
// directly, in-process, the same way internal/web's handlers call
// *orders.Service — never through this app's own /admin/api/* HTTP layer.
package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/reports"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// adminOrdersService is the subset of *orders.Service the Заказы screens
// depend on — mirrors internal/httpapi/admin_orders.go's own
// adminOrderService interface (same idea, defined separately per package
// so each HTTP-facing layer only declares what it actually calls).
type adminOrdersService interface {
	AdminListOrders(ctx context.Context, filter orders.AdminListFilter) ([]orders.Order, int, error)
	AdminGetOrder(ctx context.Context, idOrNumber string) (*orders.Order, error)
	AdminUpdateStatus(ctx context.Context, idOrNumber string, newStatus orders.OrderStatus) (*orders.Order, error)
}

// ---------- view models ----------

// StatusChipLink is one entry in the order list's status-filter row.
// Class picks the chip's color when Active (design canvas ~line 1180: an
// active chip takes its status's own fg/bg, "Все" takes the primary
// orange) via admin.css's .admin-chip-btn.active.admin-chip--* rules;
// an inactive chip is always neutral regardless of Class.
type StatusChipLink struct {
	Label  string
	URL    string
	Class  string
	Active bool
}

// RangeOptionLink is one <option> of the date-range select.
type RangeOptionLink struct {
	Value    string
	Label    string
	URL      string
	Selected bool
}

// OrderRowView backs one row of the order list, desktop table and mobile
// card alike (orders.gohtml ranges over the same slice for both).
type OrderRowView struct {
	URL          string
	ThumbURL     string // first item's photo, "" when it has none
	Number       string
	DateLabel    string
	Phone        string
	ItemsCount   int
	ItemsLabel   string
	TotalLabel   string
	PaymentLabel string
	StatusLabel  string
	StatusClass  string
}

// OrdersListData backs orders.gohtml's content template.
type OrdersListData struct {
	StatusChips  []StatusChipLink
	RangeOptions []RangeOptionLink
	Rows         []OrderRowView

	// Filter form state (fix/admin): search box, point select (owner/
	// manager only), custom date range, and notes about ignored filters.
	Query          string
	Status         string
	Range          string
	From           string
	To             string
	CanChoosePoint bool
	Points         []PointOptionVM
	Notes          []string
	Filtered       bool // any filter besides the defaults is active
	ResetURL       string

	Empty      bool
	CountLabel string
	HasPrev    bool
	HasNext    bool
	PrevURL    string
	NextURL    string
}

// OrderDetailItemView backs one row of the "Состав заказа" table.
type OrderDetailItemView struct {
	ThumbURL   string // product photo (the variant's color if tagged), "" when none
	Name       string
	Variant    string
	Qty        int
	PriceLabel string
}

// StatusButtonView is one button of the "Сменить статус" row — only valid
// next transitions from the order's current status, per buildStatusButtons.
type StatusButtonView struct {
	Label string
	Value string
	Class string
}

// OrderDetailData backs order_detail.gohtml's content template.
type OrderDetailData struct {
	ID            string
	Number        string
	DateLabel     string
	StatusLabel   string
	StatusClass   string
	Phone         string
	PaymentLabel  string
	AddressText   string
	Comment       string
	Items         []OrderDetailItemView
	TotalLabel    string
	StatusButtons []StatusButtonView
	// Order history / money flags (fix/orders-integrity, order_history.go).
	OrderHistoryData
}

// ---------- presentation helpers ----------

// orderStatusMeta is the label + CSS chip modifier for one orders.
// OrderStatus, copied from the design canvas's STATUS constant (Cozy Admin
// .dc.html, data-dc-script block, ~line 791) — colors themselves live in
// admin.css's .admin-chip--* rules, not here, matching how every other
// admin.css class is used from templates via a class name rather than an
// inline style. NOTE: the canvas's map key for courier_assigned is the
// short "courier" (a JS variable name, not a real status value) — mapped
// here by the actual orders.OrderStatus enum value instead.
type orderStatusMeta struct {
	Label string
	Class string
}

func orderStatusMetaFor(s orders.OrderStatus) orderStatusMeta {
	switch s {
	case orders.StatusPlaced:
		return orderStatusMeta{Label: "Оформлен", Class: "admin-chip--placed"}
	case orders.StatusConfirmed:
		return orderStatusMeta{Label: "Подтверждён", Class: "admin-chip--confirmed"}
	case orders.StatusCourierAssigned:
		return orderStatusMeta{Label: "Передан курьеру", Class: "admin-chip--courier"}
	case orders.StatusDelivered:
		return orderStatusMeta{Label: "Доставлен", Class: "admin-chip--delivered"}
	case orders.StatusCancelled:
		return orderStatusMeta{Label: "Отменён", Class: "admin-chip--cancelled"}
	default:
		return orderStatusMeta{Label: string(s), Class: "admin-chip--placed"}
	}
}

// paymentLabel mirrors the design canvas's `o.paid ? 'Онлайн, оплачено' :
// 'При получении'`, extended with the other online payment states (Task S:
// online_card orders carry orders.payment_status) so staff don't ship an
// order that was never paid.
func paymentLabel(pm orders.PaymentMethod, ps *orders.PaymentStatus) string {
	if pm != orders.PaymentOnlineCard {
		return "При получении"
	}
	if ps == nil {
		return "Онлайн"
	}
	switch *ps {
	case orders.PaymentPaid:
		return "Онлайн, оплачено"
	case orders.PaymentPending:
		return "Онлайн, ожидает оплаты"
	case orders.PaymentFailed:
		return "Онлайн, оплата не прошла"
	case orders.PaymentCancelled:
		return "Онлайн, оплата отменена"
	case orders.PaymentRefunded:
		return "Онлайн, возврат"
	default:
		return "Онлайн, " + string(*ps)
	}
}

// formatSom renders amount as "7 900 сом" — duplicated from internal/web/
// orders_handlers.go's identical helper rather than imported: it's
// presentation (thousands grouping + a hardcoded currency suffix), owned
// independently by each HTML-facing package, same rationale as that
// file's own comment on why statusView lives there and not in
// internal/orders.
func formatSom(amount float64) string {
	whole := int64(amount + 0.5)
	sign := ""
	if whole < 0 {
		sign = "-"
		whole = -whole
	}
	digits := strconv.FormatInt(whole, 10)

	var grouped strings.Builder
	for i, d := range digits {
		if i != 0 && (len(digits)-i)%3 == 0 {
			grouped.WriteByte(' ')
		}
		grouped.WriteRune(d)
	}
	return sign + grouped.String() + " сом"
}

// pluralRu picks the Russian plural form for n — "1 заказ"/"2 заказа"/
// "5 заказов" — mirroring the design canvas's plural() helper (~line 838).
func pluralRu(n int, one, few, many string) string {
	if n < 0 {
		n = -n
	}
	switch {
	case n%10 == 1 && n%100 != 11:
		return one
	case n%10 >= 2 && n%10 <= 4 && (n%100 < 10 || n%100 >= 20):
		return few
	default:
		return many
	}
}

// nextStatusOptions is the order status state machine, restated here as a
// pure function purely to drive which buttons the detail page shows.
//
// Source of truth: internal/orders/order.go's unexported
// validStatusTransition, which internal/orders.Service.AdminUpdateStatus
// actually enforces server-side. That function isn't exported, so it
// can't be called from here directly — this is a small, deliberately
// duplicated copy (see the Wave 4 Task 3 brief), not a second
// independently-evolving state machine. If the transition table in
// order.go ever changes, this must change with it.
func nextStatusOptions(from orders.OrderStatus) []orders.OrderStatus {
	switch from {
	case orders.StatusPlaced:
		return []orders.OrderStatus{orders.StatusConfirmed, orders.StatusCancelled}
	case orders.StatusConfirmed:
		return []orders.OrderStatus{orders.StatusCourierAssigned, orders.StatusCancelled}
	case orders.StatusCourierAssigned:
		return []orders.OrderStatus{orders.StatusDelivered, orders.StatusCancelled}
	default:
		// StatusDelivered and StatusCancelled are terminal.
		return nil
	}
}

// buildStatusButtons returns the status-change buttons for an order
// currently in status `from`, for a staff member with role `role`.
//
// Design decision (canvas ~line 1051): only `owner` may cancel an order —
// `manager` gets every other valid transition but never "Отменён". This
// is enforced at the UI layer only (internal/admin), by omitting the
// button here; internal/orders.Service.AdminUpdateStatus itself does NOT
// check role beyond "owner/manager/point_staff may update status at their
// permitted points" — a known, accepted gap between the design and the
// backend's actual authorization, out of scope for this task to close
// (see this task's report/Deviations). orderStatusUpdate below re-checks
// this same rule server-side (in internal/admin, not internal/orders)
// before calling AdminUpdateStatus, so a manager can't bypass the hidden
// button by POSTing status=cancelled directly.
func buildStatusButtons(from orders.OrderStatus, role staff.Role) []StatusButtonView {
	options := nextStatusOptions(from)
	buttons := make([]StatusButtonView, 0, len(options))
	for _, s := range options {
		if s == orders.StatusCancelled && role != staff.RoleOwner {
			continue
		}
		meta := orderStatusMetaFor(s)
		buttons = append(buttons, StatusButtonView{Label: meta.Label, Value: string(s), Class: meta.Class})
	}
	return buttons
}

// ---------- filter definitions ----------

// orderStatusFilters is the fixed status-chip row (design canvas
// ~line 405): "Все" first (empty query value = no status filter), then
// one chip per real orders.OrderStatus, in state-machine order.
var orderStatusFilters = []struct {
	Value string
	Label string
	Class string
}{
	{"", "Все", "admin-chip--all"},
	{string(orders.StatusPlaced), "Оформлен", "admin-chip--placed"},
	{string(orders.StatusConfirmed), "Подтверждён", "admin-chip--confirmed"},
	{string(orders.StatusCourierAssigned), "Передан курьеру", "admin-chip--courier"},
	{string(orders.StatusDelivered), "Доставлен", "admin-chip--delivered"},
	{string(orders.StatusCancelled), "Отменён", "admin-chip--cancelled"},
}

// orderRangeOptions is the date-range <select> (design canvas ~line 410).
var orderRangeOptions = []struct {
	Value string
	Label string
}{
	{"7", "Последние 7 дней"},
	{"30", "Последние 30 дней"},
	{"all", "Весь период"},
	{"custom", "Произвольный период"},
}

// ordersListURL builds /admin/orders?status=...&range=...&page=... —
// kept for callers that only vary those three; see ordersListParams.URL
// for the full filter set.
func ordersListURL(status, rng string, page int) string {
	return ordersListParams{Status: status, Range: rng, Page: page}.URL()
}

// ---------- handlers ----------

// ordersListPage handles GET /admin/orders: search (order number or
// customer phone), status chips, point select (owner/manager), preset or
// custom date range, table (desktop) / cards (mobile), empty state.
// Invalid filter values are dropped with a note instead of reaching SQL.
func (h *handlers) ordersListPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, _ := staff.FromContext(ctx)

	params := parseOrdersListParams(r.URL.Query())

	canChoosePoint := st.Role != staff.RolePointStaff
	var pointOpts []PointOptionVM
	if canChoosePoint {
		pts, err := h.pointsRepo.List(ctx)
		if err != nil {
			h.renderInternalErr(w, err)
			return
		}
		known := false
		for _, p := range pts {
			if p.ID == params.Point {
				known = true
			}
		}
		if !known {
			params.Point = ""
		}
		pointOpts = append(pointOpts, PointOptionVM{ID: "", Name: "Все точки", Selected: params.Point == ""})
		for _, p := range pts {
			pointOpts = append(pointOpts, PointOptionVM{ID: p.ID, Name: p.Name, Selected: p.ID == params.Point})
		}
	} else {
		params.Point = "" // point_staff: forced below, never from the URL
	}

	filter, notes := resolveOrderFilter(&params, time.Now())

	// point_staff only ever sees its own point's orders (the JSON API's
	// rule, httpapi/admin_orders.go); no point at all fails closed.
	list := []orders.Order{}
	total := 0
	if st.Role != staff.RolePointStaff || st.PointID != nil {
		if st.Role == staff.RolePointStaff {
			filter.PointID = st.PointID
		}
		var err error
		list, total, err = h.orderMeta.Search(ctx, filter)
		if err != nil {
			h.handleOrdersServiceError(w, err)
			return
		}
	}

	data := h.buildOrdersListViewFor(ctx, list, total, params)
	data.CanChoosePoint = canChoosePoint
	data.Points = pointOpts
	data.Notes = notes

	pageData := h.shellPageData("orders", "Заказы", st)
	pageData.Data = data
	if err := h.render.Render(w, "orders", pageData); err != nil {
		http.Error(w, "ошибка рендеринга страницы", http.StatusInternalServerError)
	}
}

// buildOrdersListView is buildOrdersListViewFor with only status/range/
// page set (kept for existing callers/tests).
func (h *handlers) buildOrdersListView(ctx context.Context, list []orders.Order, total int, statusParam, rangeParam string, page int) OrdersListData {
	return h.buildOrdersListViewFor(ctx, list, total, ordersListParams{Status: statusParam, Range: rangeParam, Page: page})
}

// buildOrdersListViewFor turns a page of orders into OrdersListData. Each
// row's customer phone and item count come from loadOrderListMeta — two
// batch queries for the whole page, not one lookup per row.
func (h *handlers) buildOrdersListViewFor(ctx context.Context, list []orders.Order, total int, p ordersListParams) OrdersListData {
	chips := make([]StatusChipLink, 0, len(orderStatusFilters))
	for _, f := range orderStatusFilters {
		cp := p
		cp.Status, cp.Page = f.Value, 1
		chips = append(chips, StatusChipLink{
			Label:  f.Label,
			URL:    cp.URL(),
			Class:  f.Class,
			Active: f.Value == p.Status,
		})
	}

	ranges := make([]RangeOptionLink, 0, len(orderRangeOptions))
	for _, ro := range orderRangeOptions {
		cp := p
		cp.Range, cp.Page = ro.Value, 1
		ranges = append(ranges, RangeOptionLink{
			Value:    ro.Value,
			Label:    ro.Label,
			URL:      cp.URL(),
			Selected: ro.Value == p.Range,
		})
	}

	itemCounts, phones := h.loadOrderListMeta(ctx, list)
	thumbs := h.loadOrderThumbs(ctx, list)
	rows := make([]OrderRowView, 0, len(list))
	for _, o := range list {
		meta := orderStatusMetaFor(o.Status)
		itemsCount := itemCounts[o.ID]
		rows = append(rows, OrderRowView{
			URL:          "/admin/orders/" + o.ID,
			ThumbURL:     h.photoURL(thumbs[o.ID]),
			Number:       o.OrderNumber,
			DateLabel:    o.CreatedAt.In(reports.Location).Format("02.01.2006"),
			Phone:        phones[o.CustomerID],
			ItemsCount:   itemsCount,
			ItemsLabel:   fmt.Sprintf("%d %s", itemsCount, pluralRu(itemsCount, "товар", "товара", "товаров")),
			TotalLabel:   formatSom(o.TotalAmount),
			PaymentLabel: paymentLabel(o.PaymentMethod, o.PaymentStatus),
			StatusLabel:  meta.Label,
			StatusClass:  meta.Class,
		})
	}

	prev, next := p, p
	prev.Page, next.Page = p.Page-1, p.Page+1
	return OrdersListData{
		StatusChips:  chips,
		RangeOptions: ranges,
		Rows:         rows,
		Empty:        len(rows) == 0,
		CountLabel:   fmt.Sprintf("%d %s", total, pluralRu(total, "заказ", "заказа", "заказов")),
		HasPrev:      p.Page > 1,
		HasNext:      total > p.Page*orders.AdminPageSize,
		PrevURL:      prev.URL(),
		NextURL:      next.URL(),

		Query:    p.Q,
		Status:   p.Status,
		Range:    p.Range,
		From:     p.From,
		To:       p.To,
		Filtered: p.Q != "" || p.Status != "" || p.Point != "" || (p.Range != "" && p.Range != "all"),
		ResetURL: "/admin/orders",
	}
}

// loadOrderListMeta batch-loads every row's item count and customer phone
// (two queries per page, see orderListMeta). A lookup failure degrades to
// 0 items / an empty phone for the page rather than failing it — the same
// tolerance the former per-row lookups had — and is logged.
func (h *handlers) loadOrderListMeta(ctx context.Context, list []orders.Order) (map[string]int, map[string]string) {
	if h.orderMeta == nil || len(list) == 0 {
		return map[string]int{}, map[string]string{}
	}
	orderIDs := make([]string, 0, len(list))
	customerIDs := make([]string, 0, len(list))
	seenCustomer := make(map[string]bool, len(list))
	for _, o := range list {
		orderIDs = append(orderIDs, o.ID)
		if !seenCustomer[o.CustomerID] {
			seenCustomer[o.CustomerID] = true
			customerIDs = append(customerIDs, o.CustomerID)
		}
	}

	counts, err := h.orderMeta.ItemCounts(ctx, orderIDs)
	if err != nil {
		slog.WarnContext(ctx, "admin orders list: item counts lookup failed", "err", err)
		counts = map[string]int{}
	}
	phones, err := h.orderMeta.CustomerPhones(ctx, customerIDs)
	if err != nil {
		slog.WarnContext(ctx, "admin orders list: customer phones lookup failed", "err", err)
		phones = map[string]string{}
	}
	return counts, phones
}

// loadOrderThumbs batch-loads each listed order's first-item photo (one
// query per page); a failure only drops the thumbnails.
func (h *handlers) loadOrderThumbs(ctx context.Context, list []orders.Order) map[string]string {
	if h.orderMeta == nil || len(list) == 0 {
		return map[string]string{}
	}
	ids := make([]string, len(list))
	for i, o := range list {
		ids[i] = o.ID
	}
	thumbs, err := h.orderMeta.OrderThumbs(ctx, ids)
	if err != nil {
		slog.WarnContext(ctx, "admin orders list: thumbnails lookup failed", "err", err)
		return map[string]string{}
	}
	return thumbs
}

// photoURL is thumbURL tolerant of an empty key or a nil config (tests).
func (h *handlers) photoURL(objectKey string) string {
	if objectKey == "" || h.cfg == nil {
		return ""
	}
	return h.thumbURL(objectKey)
}

// orderDetailPage handles GET /admin/orders/{id}: full order info, items +
// total, and the status-change buttons for the order's current status.
// {id} may be either the order's UUID or its order_number, same dual
// lookup as AdminGetOrder itself.
func (h *handlers) orderDetailPage(w http.ResponseWriter, r *http.Request) {
	st, _ := staff.FromContext(r.Context())
	id := r.PathValue("id")

	order, err := h.ordersSvc.AdminGetOrder(r.Context(), id)
	if err != nil {
		h.handleOrdersServiceError(w, err)
		return
	}
	if !staffCanSeeOrder(st, order) {
		http.Error(w, "заказ не найден", http.StatusNotFound)
		return
	}

	data := h.buildOrderDetailView(r.Context(), order, st.Role)

	// shellPageData("orders", ...) computes NavItems with "Заказы"
	// highlighted in the sidebar (this is a drill-down of that section);
	// Screen is then set to "order_detail" purely for readability — it
	// isn't read by any template, only Render's explicit screen argument
	// below picks order_detail.gohtml (see render.go's PageData doc
	// comment and handlers.go's renderLogin for the same pattern).
	pageData := h.shellPageData("orders", fmt.Sprintf("Заказ %s", order.OrderNumber), st)
	pageData.Screen = "order_detail"
	pageData.ShowBack = true
	if msg := r.URL.Query().Get("status_error"); msg != "" {
		pageData.Toast = msg
	}
	pageData.Data = data

	if err := h.render.Render(w, "order_detail", pageData); err != nil {
		http.Error(w, "ошибка рендеринга страницы", http.StatusInternalServerError)
	}
}

// buildOrderDetailView turns one orders.Order into OrderDetailData.
func (h *handlers) buildOrderDetailView(ctx context.Context, o *orders.Order, role staff.Role) OrderDetailData {
	meta := orderStatusMetaFor(o.Status)

	phone := ""
	if c, err := h.customers.GetByID(ctx, o.CustomerID); err == nil && c != nil {
		phone = c.Phone
	}

	addressText, comment := h.orderDeliveryInfo(ctx, o)

	variantIDs := make([]string, 0, len(o.Items))
	for _, it := range o.Items {
		variantIDs = append(variantIDs, it.VariantID)
	}
	thumbs := map[string]string{}
	if h.orderMeta != nil && len(variantIDs) > 0 {
		if m, err := h.orderMeta.VariantThumbs(ctx, variantIDs); err == nil {
			thumbs = m
		} else {
			slog.WarnContext(ctx, "admin order detail: thumbnails lookup failed", "err", err)
		}
	}

	items := make([]OrderDetailItemView, 0, len(o.Items))
	for _, it := range o.Items {
		variant := ""
		if it.SizeSnapshot != "" || it.ColorSnapshot != "" {
			variant = fmt.Sprintf("Размер %s, %s", it.SizeSnapshot, strings.ToLower(it.ColorSnapshot))
		}
		items = append(items, OrderDetailItemView{
			ThumbURL:   h.photoURL(thumbs[it.VariantID]),
			Name:       it.ProductNameSnapshot,
			Variant:    variant,
			Qty:        it.Quantity,
			PriceLabel: formatSom(it.Price * float64(it.Quantity)),
		})
	}

	return OrderDetailData{
		ID:               o.ID,
		Number:           o.OrderNumber,
		DateLabel:        o.CreatedAt.In(reports.Location).Format("02.01.2006 15:04"),
		StatusLabel:      meta.Label,
		StatusClass:      meta.Class,
		Phone:            phone,
		PaymentLabel:     paymentLabel(o.PaymentMethod, o.PaymentStatus),
		AddressText:      addressText,
		Comment:          comment,
		Items:            items,
		TotalLabel:       formatSom(o.TotalAmount),
		StatusButtons:    buildStatusButtons(o.Status, role),
		OrderHistoryData: buildOrderHistoryData(o),
	}
}

// orderDeliveryInfo resolves the "Доставка" card's address line: a saved
// customer_addresses row when o.AddressID is set (a delivery order —
// exactly one of AddressID/pickup-point-choice is ever set at checkout,
// see internal/orders/order.go's CreateOrder/validateFulfillment), or the
// chosen pickup point's own address when it's nil (self-pickup — o.PointID
// is then the customer's chosen point, not a fulfillment warehouse; for a
// delivery order PointID instead names whichever warehouse happened to
// fulfill it, which isn't meaningful to show here, so it's only consulted
// in the nil-AddressID branch).
func (h *handlers) orderDeliveryInfo(ctx context.Context, o *orders.Order) (address, comment string) {
	comment = "—"
	if o.Comment != nil && *o.Comment != "" {
		comment = *o.Comment
	}

	if o.AddressID != nil {
		if a, err := h.addresses.GetByID(ctx, o.CustomerID, *o.AddressID); err == nil && a != nil {
			address = a.AddressText
			if a.Label != nil && *a.Label != "" {
				address = *a.Label + ": " + address
			}
			return address, comment
		}
	}

	if o.PointID != nil {
		if pts, err := h.pointsRepo.List(ctx); err == nil {
			for _, p := range pts {
				if p.ID == *o.PointID {
					return "Самовывоз: " + p.Name + ", " + p.Address, comment
				}
			}
		}
	}

	return "—", comment
}

// orderStatusUpdate handles POST /admin/orders/{id}/status: the "Сменить
// статус" buttons on the detail page. Success redirects back to the (now
// updated) detail page; a rejected transition or an unauthorized cancel
// redirects back with ?status_error=... so orderDetailPage surfaces it as
// a toast instead of a raw error page.
func (h *handlers) orderStatusUpdate(w http.ResponseWriter, r *http.Request) {
	st, _ := staff.FromContext(r.Context())
	id := r.PathValue("id")
	detailURL := "/admin/orders/" + id

	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, detailURL+"?status_error="+url.QueryEscape("не удалось прочитать форму"), http.StatusSeeOther)
		return
	}
	newStatus := orders.OrderStatus(r.FormValue("status"))

	// Re-check manager-can't-cancel server-side (still within
	// internal/admin, not internal/orders — see buildStatusButtons' doc
	// comment): hiding the button isn't enough on its own, since nothing
	// stops a manager from POSTing status=cancelled directly.
	if newStatus == orders.StatusCancelled && st.Role != staff.RoleOwner {
		http.Redirect(w, r, detailURL+"?status_error="+url.QueryEscape("только владелец может отменить заказ"), http.StatusSeeOther)
		return
	}

	if st.Role == staff.RolePointStaff {
		order, err := h.ordersSvc.AdminGetOrder(r.Context(), id)
		if err != nil || !staffCanSeeOrder(st, order) {
			http.Error(w, "заказ не найден", http.StatusNotFound)
			return
		}
	}

	if _, err := h.ordersSvc.AdminUpdateStatus(r.Context(), id, newStatus); err != nil {
		msg := "не удалось изменить статус"
		var appErr *apperr.AppError
		if errors.As(err, &appErr) {
			msg = appErr.Message
		}
		http.Redirect(w, r, detailURL+"?status_error="+url.QueryEscape(msg), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, detailURL, http.StatusSeeOther)
}

// staffCanSeeOrder is the point-based RBAC rule for one order: owner and
// manager see every order, point_staff only orders of its own point (an
// order with no point, or a point_staff with none, is hidden) — the same
// rule httpapi/admin_orders.go enforces for the JSON API.
func staffCanSeeOrder(st *staff.Staff, o *orders.Order) bool {
	if st == nil || o == nil {
		return false
	}
	if st.Role != staff.RolePointStaff {
		return true
	}
	return st.PointID != nil && o.PointID != nil && *st.PointID == *o.PointID
}

// handleOrdersServiceError translates an error from *orders.Service into
// an HTTP response for a page (as opposed to apperr.Wrap's JSON body,
// which makes no sense for a page a person is looking at in a browser —
// same rationale as auth_gate.go's requireStaffRole over staffSvc.
// RequireRole).
func (h *handlers) handleOrdersServiceError(w http.ResponseWriter, err error) {
	var appErr *apperr.AppError
	if errors.As(err, &appErr) {
		http.Error(w, appErr.Message, appErr.Status)
		return
	}
	http.Error(w, "внутренняя ошибка", http.StatusInternalServerError)
}
