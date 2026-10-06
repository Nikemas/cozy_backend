package web

import (
	"encoding/xml"
	"net/http"
	"strings"
	"time"

	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/i18n"
)

// sitemapPageSize is a generous upper bound on how many products a
// sitemap enumerates in one go. catalog.ProductRepo.List still paginates
// internally; this just asks for effectively "all of them" in one page.
const sitemapPageSize = 5000

type sitemapURLSet struct {
	XMLName    xml.Name     `xml:"urlset"`
	Xmlns      string       `xml:"xmlns,attr"`
	XmlnsXHTML string       `xml:"xmlns:xhtml,attr"`
	URLs       []sitemapURL `xml:"url"`
}

type sitemapURL struct {
	Loc        string       `xml:"loc"`
	LastMod    string       `xml:"lastmod,omitempty"`
	Alternates []sitemapAlt `xml:"xhtml:link"`
}

// sitemapAlt is an <xhtml:link rel="alternate" hreflang=".." href=".."/>
// language alternate (Google's sitemap hreflang format).
type sitemapAlt struct {
	Rel      string `xml:"rel,attr"`
	Hreflang string `xml:"hreflang,attr"`
	Href     string `xml:"href,attr"`
}

// sitemapStaticPaths are the indexable non-catalog pages.
var sitemapStaticPaths = []string{"/branches", "/about", "/delivery", "/contacts", "/terms", "/privacy", "/" + accountDeletionPage}

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
//
// Every page is listed once per language — the Russian URL and its
// ?lang=ky twin, each canonical to itself (setCanonical) — and, as Google
// asks for sitemap hreflang, each <url> carries the full set of
// xhtml:link alternates (ru, ky, x-default = ru), itself included,
// matching the <link rel="alternate"> tags in the page head.
func buildSitemap(base string, tree []*catalog.Category, products []catalog.Product) sitemapURLSet {
	set := sitemapURLSet{
		Xmlns:      "http://www.sitemaps.org/schemas/sitemap/0.9",
		XmlnsXHTML: "http://www.w3.org/1999/xhtml",
	}
	add := func(path, lastMod string) {
		set.URLs = append(set.URLs, sitemapLangURLs(base+path, lastMod)...)
	}

	var newest time.Time
	for _, p := range products {
		if p.UpdatedAt.After(newest) {
			newest = p.UpdatedAt
		}
	}
	add("/", sitemapDate(newest))

	walkCategories(tree, func(c *catalog.Category) {
		add("/catalog/"+c.Slug, "")
	})
	for _, p := range products {
		add(ProductPath(p.ID, p.NameRu), sitemapDate(p.UpdatedAt))
	}
	for _, path := range sitemapStaticPaths {
		add(path, "")
	}
	return set
}

// sitemapLangURLs is the Russian and Kyrgyz <url> entries for the page at
// the absolute ruURL, both listing all language alternates.
func sitemapLangURLs(ruURL, lastMod string) []sitemapURL {
	kyURL := withLangParam(ruURL, i18n.LangKY)
	alts := []sitemapAlt{
		{Rel: "alternate", Hreflang: i18n.LangRU, Href: ruURL},
		{Rel: "alternate", Hreflang: i18n.LangKY, Href: kyURL},
		{Rel: "alternate", Hreflang: "x-default", Href: ruURL},
	}
	return []sitemapURL{
		{Loc: ruURL, LastMod: lastMod, Alternates: alts},
		{Loc: kyURL, LastMod: lastMod, Alternates: alts},
	}
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
	"/favorites", "/addresses", "/login/", "/logout", "/lang", "/account/",
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
