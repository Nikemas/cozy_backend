package web

import (
	"encoding/xml"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/catalog"
)

// sitemapBaseURL is hardcoded to the site's known production domain (see
// the task brief: "сайт cozy.kg"). internal/config has no configurable
// base-URL field yet — wiring one is a reasonable future improvement,
// not required for Task 2's sitemap to be spec-compliant (absolute
// URLs).
const sitemapBaseURL = "https://cozy.kg"

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
	Loc string `xml:"loc"`
}

// sitemap enumerates every category and active product for search
// engines — Task 1 shipped this as an empty <urlset> since the catalog
// domain didn't exist yet; Task 2 is where real rows exist to walk (see
// web-plan Task 2 acceptance criteria).
func (h *handlers) sitemap(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()

	set := sitemapURLSet{Xmlns: "http://www.sitemaps.org/schemas/sitemap/0.9"}

	tree, err := h.categories.Tree(ctx)
	if err != nil {
		return err
	}
	walkCategories(tree, func(c *catalog.Category) {
		set.URLs = append(set.URLs, sitemapURL{Loc: sitemapBaseURL + "/catalog/" + c.Slug})
	})

	products, _, err := h.products.List(ctx, catalog.ListFilter{Page: 1, PageSize: sitemapPageSize})
	if err != nil {
		return err
	}
	for _, p := range products {
		set.URLs = append(set.URLs, sitemapURL{Loc: sitemapBaseURL + ProductPath(p.ID, p.NameRu)})
	}

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

func walkCategories(nodes []*catalog.Category, fn func(*catalog.Category)) {
	for _, n := range nodes {
		fn(n)
		walkCategories(n.Children, fn)
	}
}
