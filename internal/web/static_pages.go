package web

import "net/http"

// staticPage serves one of the legal/info pages (see render.go's
// staticPages): /about, /contacts, /delivery, /privacy, /terms. The mobile
// app links to <apiBase>/privacy, so these must stay public and stable.
func (h *handlers) staticPage(name string) func(http.ResponseWriter, *http.Request) error {
	screen := "page_" + name
	return func(w http.ResponseWriter, r *http.Request) error {
		return h.render.Render(w, screen, h.base(r, screen))
	}
}
