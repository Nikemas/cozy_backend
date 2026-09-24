package web

import (
	"encoding/xml"
	"net/http"
	"strings"
	"time"

	"github.com/Nikemas/cozy_backend/internal/catalog"
)

// sitemapPageSize is a generous upper bound on how many products a
// sitemap enumerates in one go. catalog.ProductRepo.List still paginates
// internally; this just asks for effectively "all of them" in one page.
const sitemapPageSize = 5000

type sitemapURLSet struct {
	XMLName xml.Name     `xml:"urlset"`
	Xmlns   string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

type sitemapURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

// sitemapStaticPaths are the indexable non-catalog pages.
var sitemapStaticPaths = []string{"/branches", "/about", "/delivery", "/contacts", "/terms", "/privacy"}

// sitemap enumerates the home page, every category, every active product
// and the info pages, with absolute URLs on the configured public origin
// (PUBLIC_BASE_URL) and lastmod from products.updated_at.
func (h *handlers) sitemap(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	base := h.siteURL(r)

	tree, err := h.categories.Tree(ctx)
	if err != nil {
		return err
	}
	products, _, err := h.products.List(ctx, catalog.ListFilter{Page: 1, PageSize: sitemapPageSize})
	if err != nil {
		return err
	}

	set := buildSitemap(base, tree, products)
	out, err := xml.MarshalIndent(set, "", "  ")
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	if _, err := w.Write([]byte(xml.Header)); err != nil {
		return err
	}
	_, err = w.Write(out)
	return err
}

// buildSitemap is the pure part of sitemap: home (lastmod = newest
// product change), categories, products (own lastmod), info pages.
func buildSitemap(base string, tree []*catalog.Category, products []catalog.Product) sitemapURLSet {
	set := sitemapURLSet{Xmlns: "http://www.sitemaps.org/schemas/sitemap/0.9"}

	var newest time.Time
	for _, p := range products {
		if p.UpdatedAt.After(newest) {
			newest = p.UpdatedAt
		}
	}
	set.URLs = append(set.URLs, sitemapURL{Loc: base + "/", LastMod: sitemapDate(newest)})

	walkCategories(tree, func(c *catalog.Category) {
		set.URLs = append(set.URLs, sitemapURL{Loc: base + "/catalog/" + c.Slug})
	})
	for _, p := range products {
		set.URLs = append(set.URLs, sitemapURL{Loc: base + ProductPath(p.ID, p.NameRu), LastMod: sitemapDate(p.UpdatedAt)})
	}
	for _, path := range sitemapStaticPaths {
		set.URLs = append(set.URLs, sitemapURL{Loc: base + path})
	}
	return set
}

func sitemapDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02")
}

func walkCategories(nodes []*catalog.Category, fn func(*catalog.Category)) {
	for _, n := range nodes {
		fn(n)
		walkCategories(n.Children, fn)
	}
}

// robotsDisallow are private/transactional paths crawlers should skip
// (the pages themselves also carry noindex).
var robotsDisallow = []string{
	"/api/", "/admin", "/cart", "/checkout", "/order/", "/orders", "/profile",
	"/favorites", "/addresses", "/login/", "/logout", "/lang",
}

// robots serves robots.txt with an absolute Sitemap URL on the public
// origin (a relative Sitemap: line is invalid per the robots.txt spec).
func (h *handlers) robots(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString("User-agent: *\n")
	for _, p := range robotsDisallow {
		b.WriteString("Disallow: " + p + "\n")
	}
	b.WriteString("Allow: /\n\nSitemap: " + h.siteURL(r) + "/sitemap.xml\n")

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}
