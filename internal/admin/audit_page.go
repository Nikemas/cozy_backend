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

// auditEntityTypes is the entity filter, in display order.
var auditEntityTypes = []struct{ Value, Label string }{
	{audit.EntityProduct, "Товар"},
	{audit.EntityVariant, "Вариация"},
	{audit.EntityStock, "Остаток"},
	{audit.EntityOrder, "Заказ"},
	{audit.EntityCategory, "Категория"},
	{audit.EntityPoint, "Точка продаж"},
	{audit.EntityStaff, "Сотрудник"},
}

func auditEntityLabel(t string) string {
	for _, e := range auditEntityTypes {
		if e.Value == t {
			return e.Label
		}
	}
	return t
}

var auditActionLabels = map[string]string{
	audit.ActionProductCreate:     "Создание",
	audit.ActionProductUpdate:     "Изменение",
	audit.ActionProductActivate:   "Активация",
	audit.ActionProductDeactivate: "Деактивация",
	audit.ActionProductDelete:     "Удаление",
	audit.ActionProductCategory:   "Смена категории",
	audit.ActionProductImages:     "Фото",
	audit.ActionVariantCreate:     "Создание",
	audit.ActionVariantUpdate:     "Изменение",
	audit.ActionVariantDelete:     "Удаление",
	audit.ActionStockUpdate:       "Изменение остатка",
	audit.ActionCategoryCreate:    "Создание",
	audit.ActionCategoryUpdate:    "Изменение",
	audit.ActionCategoryDelete:    "Удаление",
	audit.ActionPointCreate:       "Создание",
	audit.ActionPointUpdate:       "Изменение",
	audit.ActionPointActivate:     "Активация",
	audit.ActionPointDeactivate:   "Деактивация",
	audit.ActionStaffCreate:       "Создание",
	audit.ActionStaffActivate:     "Активация",
	audit.ActionStaffDeactivate:   "Деактивация",
	audit.ActionStaffPassword:     "Сброс пароля",
	audit.ActionOrderStatus:       "Смена статуса",
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
func resolveAuditFilter(p *auditParams) (audit.Filter, string) {
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
		if t, err := reports.ParseReportDate(p.From); err == nil {
			f.From = t
		} else {
			errs = append(errs, "дата «с» должна быть в формате ГГГГ-ММ-ДД")
			p.From = ""
		}
	}
	if p.To != "" {
		if t, err := reports.ParseReportDate(p.To); err == nil {
			f.To = t.AddDate(0, 0, 1) // the whole "to" day
		} else {
			errs = append(errs, "дата «по» должна быть в формате ГГГГ-ММ-ДД")
			p.To = ""
		}
	}
	if !f.From.IsZero() && !f.To.IsZero() && !f.From.Before(f.To) {
		errs = append(errs, "дата начала позже даты окончания")
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
	filter, errMsg := resolveAuditFilter(&p)

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

	data := buildAuditPageData(p, staffList, rows, total, errMsg)
	pageData := h.shellPageData("audit", "Журнал действий", st)
	pageData.Data = data
	if err := h.render.Render(w, "audit", pageData); err != nil {
		http.Error(w, "ошибка рендеринга страницы", http.StatusInternalServerError)
	}
}

func buildAuditPageData(p auditParams, staffList []staff.Staff, rows []audit.Row, total int, errMsg string) AuditPageData {
	data := AuditPageData{
		StaffID: p.Staff, Entity: p.Entity, From: p.From, To: p.To, Query: p.Q, Err: errMsg,
		Filtered: p.Staff != "" || p.Entity != "" || p.From != "" || p.To != "" || p.Q != "",
	}
	data.StaffOptions = append(data.StaffOptions, AuditOptionVM{Value: "", Label: "Все сотрудники", Selected: p.Staff == ""})
	for _, s := range staffList {
		label := s.Name
		if !s.IsActive {
			label += " (отключён)"
		}
		data.StaffOptions = append(data.StaffOptions, AuditOptionVM{Value: s.ID, Label: label, Selected: s.ID == p.Staff})
	}
	data.EntityOptions = append(data.EntityOptions, AuditOptionVM{Value: "", Label: "Все объекты", Selected: p.Entity == ""})
	for _, e := range auditEntityTypes {
		data.EntityOptions = append(data.EntityOptions, AuditOptionVM{Value: e.Value, Label: e.Label, Selected: e.Value == p.Entity})
	}

	for _, row := range rows {
		data.Rows = append(data.Rows, auditRowVM(row))
	}
	data.Empty = len(data.Rows) == 0
	data.CountLabel = fmt.Sprintf("%d %s", total, pluralRu(total, "запись", "записи", "записей"))
	pageCount := (total + auditPageSize - 1) / auditPageSize
	if pageCount < 1 {
		pageCount = 1
	}
	data.PageLabel = fmt.Sprintf("Страница %d из %d", p.Page, pageCount)
	prev, next := p, p
	prev.Page, next.Page = p.Page-1, p.Page+1
	data.HasPrev = p.Page > 1
	data.HasNext = total > p.Page*auditPageSize
	data.PrevURL, data.NextURL = prev.URL(), next.URL()
	return data
}

func auditRowVM(row audit.Row) AuditRowVM {
	vm := AuditRowVM{
		When:        row.At.In(reports.Location).Format("02.01.2006 15:04"),
		Staff:       row.StaffName,
		ActionLabel: auditActionLabels[row.Action],
		EntityLabel: auditEntityLabel(row.EntityType),
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
			fromLabel = orderStatusMetaFor(orders.OrderStatus(from)).Label
		}
		vm.Summary = fmt.Sprintf("%s: %s → %s", row.Summary, fromLabel, orderStatusMetaFor(orders.OrderStatus(to)).Label)
		if note, _ := row.Details["note"].(string); note != "" {
			vm.DetailsText = note
		}
		return vm
	}
	vm.DetailsText = auditDetailsText(row.Details)
	return vm
}

// auditDetailsText renders Details as "field: from → to; field: value",
// skipping ids already shown elsewhere on the row.
func auditDetailsText(details map[string]any) string {
	keys := make([]string, 0, len(details))
	for k := range details {
		switch k {
		case "product_id", "point_id", "bulk":
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		label := k
		if l, ok := productFieldLabels[k]; ok {
			label = l
		}
		v := details[k]
		if m, ok := v.(map[string]any); ok {
			if from, hasFrom := m["from"]; hasFrom {
				parts = append(parts, fmt.Sprintf("%s: %s → %s", label, auditValue(from), auditValue(m["to"])))
				continue
			}
		}
		parts = append(parts, fmt.Sprintf("%s: %s", label, auditValue(v)))
	}
	out := strings.Join(parts, "; ")
	if r := []rune(out); len(r) > 300 {
		out = string(r[:300]) + "…"
	}
	return out
}

func auditValue(v any) string {
	switch t := v.(type) {
	case nil:
		return "—"
	case string:
		if t == "" {
			return "—"
		}
		return t
	case bool:
		if t {
			return "да"
		}
		return "нет"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprint(t)
	}
}
