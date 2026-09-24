// audit_page.go (fix/admin-ops, W5): the owner-only "Журнал" screen —
// GET /admin/audit, the audit journal (internal/audit) with filters by
// staff member, entity type, date range and entity id / order number,
// paginated.
package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/audit"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/reports"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// auditLister is (*audit.Log).List, as an interface for tests.
type auditLister interface {
	List(ctx context.Context, f audit.Filter) ([]audit.Row, int, error)
}

const auditPageSize = 50

// auditEntityTypes is the entity filter, in display order (Label is a
// locale key).
var auditEntityTypes = []struct{ Value, Label string }{
	{audit.EntityProduct, "admin.audit.entity.product"},
	{audit.EntityVariant, "admin.audit.entity.variant"},
	{audit.EntityStock, "admin.audit.entity.stock"},
	{audit.EntityOrder, "admin.audit.entity.order"},
	{audit.EntityCategory, "admin.audit.entity.category"},
	{audit.EntityPoint, "admin.audit.entity.point"},
	{audit.EntityStaff, "admin.audit.entity.staff"},
}

func auditEntityLabel(t string) string {
	for _, e := range auditEntityTypes {
		if e.Value == t {
			return e.Label
		}
	}
	return t
}

// auditActionLabels are locale keys.
var auditActionLabels = map[string]string{
	audit.ActionProductCreate:     "admin.audit.action.create",
	audit.ActionProductUpdate:     "admin.audit.action.update",
	audit.ActionProductActivate:   "admin.audit.action.activate",
	audit.ActionProductDeactivate: "admin.audit.action.deactivate",
	audit.ActionProductDelete:     "admin.audit.action.delete",
	audit.ActionProductCategory:   "admin.audit.action.category",
	audit.ActionProductImages:     "admin.audit.action.images",
	audit.ActionVariantCreate:     "admin.audit.action.create",
	audit.ActionVariantUpdate:     "admin.audit.action.update",
	audit.ActionVariantDelete:     "admin.audit.action.delete",
	audit.ActionStockUpdate:       "admin.audit.action.stock",
	audit.ActionCategoryCreate:    "admin.audit.action.create",
	audit.ActionCategoryUpdate:    "admin.audit.action.update",
	audit.ActionCategoryDelete:    "admin.audit.action.delete",
	audit.ActionPointCreate:       "admin.audit.action.create",
	audit.ActionPointUpdate:       "admin.audit.action.update",
	audit.ActionPointActivate:     "admin.audit.action.activate",
	audit.ActionPointDeactivate:   "admin.audit.action.deactivate",
	audit.ActionStaffCreate:       "admin.audit.action.create",
	audit.ActionStaffActivate:     "admin.audit.action.activate",
	audit.ActionStaffDeactivate:   "admin.audit.action.deactivate",
	audit.ActionStaffPassword:     "admin.audit.action.password",
	audit.ActionOrderStatus:       "admin.audit.action.order_status",
}

// AuditRowVM is one journal line.
type AuditRowVM struct {
	When        string
	Staff       string
	ActionLabel string
	EntityLabel string
	EntityID    string
	EntityURL   string
	Summary     string
	DetailsText string
	IP          string
}

// AuditOptionVM is one <option> of a filter select.
type AuditOptionVM struct {
	Value    string
	Label    string
	Selected bool
}

// AuditPageData backs audit.gohtml.
type AuditPageData struct {
	StaffOptions  []AuditOptionVM
	EntityOptions []AuditOptionVM
	StaffID       string
	Entity        string
	From          string
	To            string
	Query         string
	Err           string

	Rows       []AuditRowVM
	Empty      bool
	Filtered   bool
	CountLabel string
	PageLabel  string
	HasPrev    bool
	HasNext    bool
	PrevURL    string
	NextURL    string
}

// auditParams is the page's query string.
type auditParams struct {
	Staff, Entity, From, To, Q string
	Page                       int
}

func parseAuditParams(q url.Values) auditParams {
	return auditParams{
		Staff:  strings.TrimSpace(q.Get("staff")),
		Entity: strings.TrimSpace(q.Get("entity")),
		From:   strings.TrimSpace(q.Get("from")),
		To:     strings.TrimSpace(q.Get("to")),
		Q:      strings.TrimSpace(q.Get("q")),
		Page:   parsePositiveInt(q.Get("page"), 1),
	}
}

func (p auditParams) URL() string {
	v := url.Values{}
	for k, val := range map[string]string{"staff": p.Staff, "entity": p.Entity, "from": p.From, "to": p.To, "q": p.Q} {
		if val != "" {
			v.Set(k, val)
		}
	}
	if p.Page > 1 {
		v.Set("page", strconv.Itoa(p.Page))
	}
	if len(v) == 0 {
		return "/admin/audit"
	}
	return "/admin/audit?" + v.Encode()
}

// resolveAuditFilter validates p into an audit.Filter; bad dates or an
// unknown entity type are dropped with a message instead of failing.
func resolveAuditFilter(t tr, p *auditParams) (audit.Filter, string) {
	f := audit.Filter{StaffID: p.Staff, EntityQuery: p.Q, Page: p.Page, PageSize: auditPageSize}
	var errs []string
	if p.Entity != "" {
		if auditEntityLabel(p.Entity) == p.Entity {
			p.Entity = ""
		} else {
			f.EntityType = p.Entity
		}
	}
	if p.From != "" {
		if d, err := reports.ParseReportDate(p.From); err == nil {
			f.From = d
		} else {
			errs = append(errs, t.T("admin.audit.err_from"))
			p.From = ""
		}
	}
	if p.To != "" {
		if d, err := reports.ParseReportDate(p.To); err == nil {
			f.To = d.AddDate(0, 0, 1) // the whole "to" day
		} else {
			errs = append(errs, t.T("admin.audit.err_to"))
			p.To = ""
		}
	}
	if !f.From.IsZero() && !f.To.IsZero() && !f.From.Before(f.To) {
		errs = append(errs, t.T("admin.audit.err_range"))
	}
	if r := []rune(p.Q); len(r) > 100 {
		p.Q = string(r[:100])
		f.EntityQuery = p.Q
	}
	return f, strings.Join(errs, "; ")
}

// auditPage handles GET /admin/audit (owner only).
func (h *handlers) auditPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, _ := staff.FromContext(ctx)

	p := parseAuditParams(r.URL.Query())
	filter, errMsg := resolveAuditFilter(h.tr(r), &p)

	var staffList []staff.Staff
	if h.staffSvc != nil {
		var err error
		if staffList, err = h.staffSvc.ListStaff(ctx); err != nil {
			h.renderInternalErr(w, err)
			return
		}
	}

	rows, total, err := h.auditList.List(ctx, filter)
	if err != nil {
		h.renderInternalErr(w, err)
		return
	}

	data := buildAuditPageData(h.tr(r), p, staffList, rows, total, errMsg)
	pageData := h.shellPageData("audit", "admin.audit.title", st)
	pageData.Data = data
	if err := h.render.Render(w, "audit", pageData); err != nil {
		http.Error(w, h.tr(r).T("admin.err.render"), http.StatusInternalServerError)
	}
}

func buildAuditPageData(t tr, p auditParams, staffList []staff.Staff, rows []audit.Row, total int, errMsg string) AuditPageData {
	data := AuditPageData{
		StaffID: p.Staff, Entity: p.Entity, From: p.From, To: p.To, Query: p.Q, Err: errMsg,
		Filtered: p.Staff != "" || p.Entity != "" || p.From != "" || p.To != "" || p.Q != "",
	}
	data.StaffOptions = append(data.StaffOptions, AuditOptionVM{Value: "", Label: t.T("admin.audit.all_staff"), Selected: p.Staff == ""})
	for _, s := range staffList {
		label := s.Name
		if !s.IsActive {
			label += " " + t.T("admin.audit.staff_inactive")
		}
		data.StaffOptions = append(data.StaffOptions, AuditOptionVM{Value: s.ID, Label: label, Selected: s.ID == p.Staff})
	}
	data.EntityOptions = append(data.EntityOptions, AuditOptionVM{Value: "", Label: t.T("admin.audit.all_entities"), Selected: p.Entity == ""})
	for _, e := range auditEntityTypes {
		data.EntityOptions = append(data.EntityOptions, AuditOptionVM{Value: e.Value, Label: t.T(e.Label), Selected: e.Value == p.Entity})
	}

	for _, row := range rows {
		data.Rows = append(data.Rows, auditRowVM(t, row))
	}
	data.Empty = len(data.Rows) == 0
	data.CountLabel = t.N(total, "admin.plural.record")
	pageCount := (total + auditPageSize - 1) / auditPageSize
	if pageCount < 1 {
		pageCount = 1
	}
	data.PageLabel = t.F("admin.audit.page_of", p.Page, pageCount)
	prev, next := p, p
	prev.Page, next.Page = p.Page-1, p.Page+1
	data.HasPrev = p.Page > 1
	data.HasNext = total > p.Page*auditPageSize
	data.PrevURL, data.NextURL = prev.URL(), next.URL()
	return data
}

func auditRowVM(t tr, row audit.Row) AuditRowVM {
	vm := AuditRowVM{
		When:        row.At.In(reports.Location).Format("02.01.2006 15:04"),
		Staff:       row.StaffName,
		ActionLabel: t.T(auditActionLabels[row.Action]),
		EntityLabel: t.T(auditEntityLabel(row.EntityType)),
		EntityID:    row.EntityID,
		Summary:     row.Summary,
		IP:          row.IP,
	}
	if vm.Staff == "" {
		vm.Staff = "—"
	}
	if vm.ActionLabel == "" {
		vm.ActionLabel = row.Action
	}
	productID, _ := row.Details["product_id"].(string)
	switch row.EntityType {
	case audit.EntityProduct:
		vm.EntityURL = "/admin/products/" + row.EntityID
	case audit.EntityVariant, audit.EntityStock:
		if productID != "" {
			vm.EntityURL = "/admin/products/" + productID
		}
	case audit.EntityOrder:
		vm.EntityURL = "/admin/orders/" + row.EntityID
	case audit.EntityCategory:
		vm.EntityURL = "/admin/categories"
	case audit.EntityPoint:
		vm.EntityURL = "/admin/points"
	case audit.EntityStaff:
		vm.EntityURL = "/admin/staff"
	}
	if row.Action == audit.ActionOrderStatus {
		from, _ := row.Details["from"].(string)
		to, _ := row.Details["to"].(string)
		fromLabel := "—"
		if from != "" {
			fromLabel = orderStatusMetaFor(t, orders.OrderStatus(from)).Label
		}
		vm.Summary = fmt.Sprintf("%s: %s → %s", row.Summary, fromLabel, orderStatusMetaFor(t, orders.OrderStatus(to)).Label)
		if note, _ := row.Details["note"].(string); note != "" {
			vm.DetailsText = note
		}
		return vm
	}
	vm.DetailsText = auditDetailsText(t, row.Details)
	return vm
}

// auditDetailLabels names the non-product detail keys (locale keys).
var auditDetailLabels = map[string]string{
	"quantity": "admin.audit.field.quantity", "point": "admin.audit.field.point", "is_active": "admin.audit.field.is_active",
	"name": "admin.audit.field.name", "address": "admin.audit.field.address", "phone": "admin.audit.field.phone",
	"role": "admin.audit.field.role", "size": "admin.audit.field.size", "color": "admin.audit.field.color",
	"sku": "admin.audit.field.sku", "price_override": "admin.audit.field.price_override", "count": "admin.audit.field.quantity",
	"slug": "admin.audit.field.slug", "sort_order": "admin.audit.field.sort_order", "parent_id": "admin.audit.field.parent",
}

// auditDetailsText renders Details as "field: from → to; field: value",
// skipping ids already shown elsewhere on the row.
func auditDetailsText(t tr, details map[string]any) string {
	keys := make([]string, 0, len(details))
	for k := range details {
		switch k {
		case "product_id", "point_id", "bulk", "category_id":
			continue // ids: already in the summary / entity link
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		label := k
		if l, ok := productFieldLabels[k]; ok {
			label = t.T(l)
		} else if l, ok := auditDetailLabels[k]; ok {
			label = t.T(l)
		}
		v := details[k]
		if m, ok := v.(map[string]any); ok {
			if from, hasFrom := m["from"]; hasFrom {
				parts = append(parts, fmt.Sprintf("%s: %s → %s", label, auditValue(t, from), auditValue(t, m["to"])))
				continue
			}
		}
		parts = append(parts, fmt.Sprintf("%s: %s", label, auditValue(t, v)))
	}
	out := strings.Join(parts, "; ")
	if r := []rune(out); len(r) > 300 {
		out = string(r[:300]) + "…"
	}
	return out
}

func auditValue(t tr, v any) string {
	switch x := v.(type) {
	case nil:
		return "—"
	case string:
		if x == "" {
			return "—"
		}
		return x
	case bool:
		if x {
			return t.T("admin.common.yes")
		}
		return t.T("admin.common.no")
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprint(x)
	}
}
