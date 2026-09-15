package web

import "net/http"

// sitemap serves an empty <urlset> until Task 2 has real product/category
// rows to enumerate under /product/:slug and /catalog/:slug (see
// Slugify).
func (h *handlers) sitemap(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, err := w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"></urlset>` + "\n"))
	return err
}
