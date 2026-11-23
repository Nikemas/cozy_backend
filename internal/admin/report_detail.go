// report_detail.go — detail reports (feat/product-code): one catalog
// category (with its subcategories), one brand, or one product by its
// code, over the same periods as the summary page. The numbers come from
// reports.Repo's detail queries (through the shared CachedRepo); the
// summary's brand/category rows link here, and every report exports to
// Excel (report_detail_xlsx.go). Owner/manager only, like /admin/reports.
package admin

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/reports"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

const (
	categoryReportPath = "/admin/reports/category"
	brandReportPath    = "/admin/reports/brand"
	productReportPath  = "/admin/reports/product"
	xlsxSuffix         = ".xlsx"

	// noBrandParam is the brand report's ?brand= value for products
	// without a brand (an empty value means "nothing chosen yet").
	noBrandParam = "__none__"
)

// Detail report kinds (ReportDetailData.Kind).
const (
	reportKindCategory = "category"
	reportKindBrand    = "brand"
	reportKindProduct  = "product"
)

// reportDetailBackend is what the detail pages need from reports.CachedRepo.
type reportDetailBackend interface {
	CategoryProductSales(ctx context.Context, categoryID string, from, to time.Time) ([]reports.ProductSalesRow, error)
	BrandProductSales(ctx context.Context, brand string, from, to time.Time) ([]reports.ProductSalesRow, error)
	ProductCodeSales(ctx context.Context, productID string, from, to time.Time, pointNames map[string]string) (reports.ProductCodeSales, error)
	ProductByCode(ctx context.Context, code string) (*reports.ProductRef, error)
	ProductStock(ctx context.Context, productID string) ([]reports.StockCell, error)
	PointNames(ctx context.Context) (map[string]string, error)
}

// registerReportDetailRoutes mounts the three detail pages and their
// Excel exports behind gate (ownerOrManager).
func registerReportDetailRoutes(mux *http.ServeMux, h *handlers, gate func(http.HandlerFunc) http.HandlerFunc) {
	for _, kind := range []string{reportKindCategory, reportKindBrand, reportKindProduct} {
		path := "/admin/reports/" + kind
		mux.HandleFunc("GET "+path, gate(h.reportDetailPage(kind, false)))
		mux.HandleFunc("GET "+path+xlsxSuffix, gate(h.reportDetailPage(kind, true)))
	}
}

// HiddenParam is one <input type="hidden"> of a GET form.
type HiddenParam struct {
	Name  string
	Value string
}

// reportPeriodHidden is reportPeriodQuery as hidden inputs.
func reportPeriodHidden(period string, from, to time.Time) []HiddenParam {
	q := reportPeriodQuery(period, from, to)
	out := []HiddenParam{{Name: "period", Value: q.Get("period")}}
	if period == "custom" {
		out = append(out, HiddenParam{Name: "from", Value: q.Get("from")}, HiddenParam{Name: "to", Value: q.Get("to")})
	}
	return out
}

// withCategoryLinks points each summary category row at its detail report.
func withCategoryLinks(bars []CategoryBar, rows []reports.Row, period string, from, to time.Time) []CategoryBar {
	out := make([]CategoryBar, len(bars))
	copy(out, bars)
	for i := range out {
		if i < len(rows) && rows[i].ID != "" {
			out[i].URL = reportLink(categoryReportPath, period, from, to, url.Values{"id": {rows[i].ID}})
		}
	}
	return out
}

// withBrandLinks points each summary brand row at its detail report.
func withBrandLinks(bars []BrandBar, rows []reports.Row, period string, from, to time.Time) []BrandBar {
	out := make([]BrandBar, len(bars))
	copy(out, bars)
	for i := range out {
		if i < len(rows) {
			out[i].URL = reportLink(brandReportPath, period, from, to, url.Values{"brand": {brandParam(rows[i].Key)}})
		}
	}
	return out
}

func brandParam(brand string) string {
	if strings.TrimSpace(brand) == "" {
		return noBrandParam
	}
	return brand
}

// reportTitleKey is a detail report kind's title (locale key).
func reportTitleKey(kind string) string {
	switch kind {
	case reportKindCategory:
		return "admin.reports.detail_category"
	case reportKindBrand:
		return "admin.reports.detail_brand"
	default:
		return "admin.reports.detail_product"
	}
}

// reportSubject is a detail report's resolved subject.
type reportSubject struct {
	Param string // query param name: id / brand / code
	Value string // its value as submitted ("" = nothing chosen)
	Title string // display name ("" when unresolved)
	Err   string // why it could not be resolved
}

// reportDetailPage serves one kind's page (asXLSX false) or its export.
func (h *handlers) reportDetailPage(kind string, asXLSX bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t := h.tr(r)
		period, from, to, rangeErr := resolveReportRange(t, r.URL.Query(), time.Now())
		data, err := h.buildReportDetail(r.Context(), t, kind, r.URL.Query(), period, from, to)
		if err != nil {
			http.Error(w, t.T("admin.reports.build_failed"), http.StatusInternalServerError)
			return
		}
		if asXLSX {
			if !data.HasReport {
				http.Error(w, data.Err, http.StatusBadRequest)
				return
			}
			if err := writeReportDetailXLSX(w, t, data); err != nil {
				http.Error(w, t.T("admin.reports.build_failed"), http.StatusInternalServerError)
			}
			return
		}
		if rangeErr != "" {
			data.Err = rangeErr
		}
		h.renderReportDetail(w, r, data)
	}
}

func (h *handlers) renderReportDetail(w http.ResponseWriter, r *http.Request, data ReportDetailData) {
	st, _ := staff.FromContext(r.Context())
	page := h.shellPageData("report_detail", reportTitleKey(data.Kind), st)
	page.NavItems = navItemsForRole(st.Role, "reports")
	page.ShowBack = true
	page.BackURL = data.SummaryURL
	page.Data = data
	if err := h.render.Render(w, "report_detail", page); err != nil {
		http.Error(w, h.tr(r).T("admin.err.render"), http.StatusInternalServerError)
	}
}

// buildReportDetail resolves the subject from q and loads its report.
func (h *handlers) buildReportDetail(ctx context.Context, t tr, kind string, q url.Values, period string, from, to time.Time) (ReportDetailData, error) {
	data := ReportDetailData{
		Kind:         kind,
		Periods:      reportPeriodOptions(t, period),
		Custom:       period == "custom",
		From:         from.Format("2006-01-02"),
		To:           to.Format("2006-01-02"),
		PeriodLabel:  from.Format("02.01.2006") + " — " + to.Format("02.01.2006"),
		SummaryURL:   reportLink("/admin/reports", period, from, to, nil),
		PeriodParams: reportPeriodHidden(period, from, to),
		Action:       "/admin/reports/" + kind,
	}
	loadTo := to.AddDate(0, 0, 1) // the queries take [from, to)

	var err error
	switch kind {
	case reportKindCategory:
		err = h.buildCategoryReport(ctx, t, q.Get("id"), from, loadTo, period, &data)
	case reportKindBrand:
		err = h.buildBrandReport(ctx, t, q.Get("brand"), from, loadTo, period, &data)
	default:
		err = h.buildProductReport(ctx, t, q.Get("code"), from, loadTo, period, &data)
	}
	if err != nil {
		return ReportDetailData{}, err
	}
	if data.HasReport {
		data.ExportURL = reportLink(data.Action+xlsxSuffix, period, from, to, url.Values{data.Subject.Param: {data.Subject.Value}})
	}
	return data, nil
}

func (h *handlers) buildCategoryReport(ctx context.Context, t tr, id string, from, loadTo time.Time, period string, data *ReportDetailData) error {
	tree, err := h.categories.Tree(ctx)
	if err != nil {
		return err
	}
	data.Subject = reportSubject{Param: "id", Value: strings.TrimSpace(id)}
	data.Options = categoryPickOptions(t, tree, data.Subject.Value)
	if data.Subject.Value == "" {
		return nil
	}
	name := categoryLabel(tree, data.Subject.Value)
	if _, perr := uuid.Parse(data.Subject.Value); perr != nil || name == "" {
		data.Err = t.T("admin.reports.category_not_found")
		return nil
	}
	data.Subject.Title = name
	rows, err := h.reportDetails.CategoryProductSales(ctx, data.Subject.Value, from, loadTo)
	if err != nil {
		return err
	}
	data.setProductRows(t, rows, period, from, loadTo.AddDate(0, 0, -1))
	return nil
}

func (h *handlers) buildBrandReport(ctx context.Context, t tr, brand string, from, loadTo time.Time, period string, data *ReportDetailData) error {
	brands, err := h.products.DistinctBrands(ctx)
	if err != nil {
		return err
	}
	data.Subject = reportSubject{Param: "brand", Value: strings.TrimSpace(brand)}
	data.Options = brandPickOptions(t, brands, data.Subject.Value)
	if data.Subject.Value == "" {
		return nil
	}
	query := data.Subject.Value
	// Only a real brand (or "no brand") is queried: an arbitrary ?brand=
	// string would otherwise add one cache entry per value.
	if query != noBrandParam && !slices.ContainsFunc(brands, func(b string) bool { return strings.TrimSpace(b) == query }) {
		return nil
	}
	data.Subject.Title = query
	if query == noBrandParam {
		query = ""
		data.Subject.Title = t.T("admin.reports.no_brand")
	}
	rows, err := h.reportDetails.BrandProductSales(ctx, query, from, loadTo)
	if err != nil {
		return err
	}
	data.setProductRows(t, rows, period, from, loadTo.AddDate(0, 0, -1))
	return nil
}

func (h *handlers) buildProductReport(ctx context.Context, t tr, code string, from, loadTo time.Time, period string, data *ReportDetailData) error {
	data.Subject = reportSubject{Param: "code", Value: strings.TrimSpace(code)}
	if data.Subject.Value == "" {
		return nil
	}
	ref, err := h.reportDetails.ProductByCode(ctx, data.Subject.Value)
	if err != nil {
		return err
	}
	if ref == nil {
		data.Err = t.F("admin.reports.code_not_found", data.Subject.Value)
		return nil
	}
	pointNames, err := h.reportDetails.PointNames(ctx)
	if err != nil {
		return err
	}
	sales, err := h.reportDetails.ProductCodeSales(ctx, ref.ID, from, loadTo, pointNames)
	if err != nil {
		return err
	}
	stock, err := h.reportDetails.ProductStock(ctx, ref.ID)
	if err != nil {
		return err
	}
	data.Subject.Value = ref.Code
	data.Subject.Title = ref.Code + " · " + ref.Name
	data.HasReport = true
	data.Product = &ProductReportHeadVM{
		Code: ref.Code, Name: ref.Name, Brand: ref.Brand, Inactive: !ref.IsActive,
		EditURL: "/admin/products/" + ref.ID,
	}
	data.Variants, data.VariantTotal = buildVariantReportRows(t, sales.Variants)
	data.pointRows = sortedByRevenue(sales.Points)
	data.Points = buildPointBars(t, data.pointRows)
	data.Stock = buildStockMatrix(t, sales.Variants, stock)
	return nil
}

// setProductRows fills a category/brand report's table.
func (d *ReportDetailData) setProductRows(t tr, rows []reports.ProductSalesRow, period string, from, to time.Time) {
	d.HasReport = true
	d.Products = make([]ProductReportRowVM, len(rows))
	for i, row := range rows {
		d.Products[i] = productReportRow(t, row)
		if row.Code != "" {
			d.Products[i].URL = reportLink(productReportPath, period, from, to, url.Values{"code": {row.Code}})
		}
	}
	d.ProductTotal = productReportRow(t, reports.TotalProductSales(rows))
}

// --- pickers ---

// PickOption is one <option> of a detail report's subject select.
type PickOption struct {
	Value    string
	Label    string
	Selected bool
}

func categoryPickOptions(t tr, tree []*catalog.Category, selected string) []PickOption {
	out := []PickOption{{Value: "", Label: t.T("admin.reports.pick_placeholder"), Selected: selected == ""}}
	for _, c := range flatCategoryOptions(tree) {
		out = append(out, PickOption{Value: c.ID, Label: c.Label, Selected: c.ID == selected})
	}
	return out
}

// categoryLabel is the "Топ / Под" name of category id ("" if unknown).
func categoryLabel(tree []*catalog.Category, id string) string {
	for _, c := range flatCategoryOptions(tree) {
		if c.ID == id {
			return c.Label
		}
	}
	return ""
}

// brandPickOptions lists brands as the report groups them (trimmed,
// exact spelling), plus "no brand".
func brandPickOptions(t tr, brands []string, selected string) []PickOption {
	out := []PickOption{{Value: "", Label: t.T("admin.reports.pick_placeholder"), Selected: selected == ""}}
	seen := map[string]bool{}
	for _, b := range brands {
		b = strings.TrimSpace(b)
		if b == "" || seen[b] {
			continue
		}
		seen[b] = true
		out = append(out, PickOption{Value: b, Label: b, Selected: b == selected})
	}
	out = append(out, PickOption{Value: noBrandParam, Label: t.T("admin.reports.no_brand"), Selected: selected == noBrandParam})
	return out
}

// sortedByRevenue is a revenue-descending copy of rows (buildPointBars'
// order, which is stable on already-sorted input).
func sortedByRevenue(rows []reports.Row) []reports.Row {
	out := append([]reports.Row(nil), rows...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Revenue > out[j].Revenue })
	return out
}
