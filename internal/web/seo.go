package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/Nikemas/cozy_backend/internal/i18n"
)

// SEOMeta is everything layout.gohtml puts in <head> for search engines
// and link previews. base() fills sensible defaults; shop/product/static
// pages override what they know better.
type SEOMeta struct {
	Title       string // full <title>
	Description string // <meta name="description">, ≤ ~160 chars
	Canonical   string // absolute canonical URL ("" → omitted)
	// AltRU/AltKY are the hreflang alternates (same page, ?lang=ky for
	// Kyrgyz); empty for pages that aren't indexed.
	AltRU    string
	AltKY    string
	OGType   string // "website" or "product"
	OGImage  string // absolute URL
	OGLocale string
	// JSONLD is a pre-encoded schema.org JSON-LD document ("" → omitted).
	JSONLD template.JS
}

// privateScreens are account/transactional screens search engines must
// not index (also Disallow-ed in robots.txt).
var privateScreens = map[string]bool{
	"cart": true, "checkout": true, "done": true, "fav": true, "profile": true,
	"orders": true, "addresses": true, "lang": true, "error": true,
}

// siteURL is the site's public origin: PUBLIC_BASE_URL when configured
// (always in prod), else derived from the request (local dev).
func (h *handlers) siteURL(r *http.Request) string {
	if h.cfg != nil && h.cfg.PublicBaseURL != "" {
		return h.cfg.PublicBaseURL
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// absURL joins the site origin and a path (which may carry a query).
func (h *handlers) absURL(r *http.Request, path string) string {
	return h.siteURL(r) + path
}

// withLangParam adds ?lang=<lang> to an absolute URL (Kyrgyz alternates).
func withLangParam(rawURL, lang string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	q.Set("lang", lang)
	u.RawQuery = q.Encode()
	return u.String()
}

// setCanonical points the page's canonical URL and hreflang alternates at
// path (site-relative, may include a query). For a Kyrgyz page reached via
// ?lang=ky the canonical keeps the lang parameter, so each language
// version is canonical to itself.
func (h *handlers) setCanonical(r *http.Request, data *PageData, path string) {
	base := h.absURL(r, path)
	data.SEO.AltRU = base
	data.SEO.AltKY = withLangParam(base, i18n.LangKY)
	data.SEO.Canonical = base
	if r.URL.Query().Get("lang") == i18n.LangKY {
		data.SEO.Canonical = data.SEO.AltKY
	}
}

// defaultSEO fills data.SEO for screen: title "<screen> | Cozy", the site
// description, canonical = current path, noindex for private screens.
func (h *handlers) defaultSEO(r *http.Request, data *PageData, screen string) {
	lang := data.Lang
	data.NoIndex = privateScreens[screen]
	data.SEO = SEOMeta{
		Title:       fmt.Sprintf(h.t(lang, "seo.page.title"), h.t(lang, "screen."+screen+".title")),
		Description: h.t(lang, "seo.home.description"),
		OGType:      "website",
		OGImage:     h.absURL(r, "/static/img/og.png"),
		OGLocale:    ogLocale(lang),
	}
	if key := "seo." + screen + ".description"; h.t(lang, key) != key {
		data.SEO.Description = h.t(lang, key)
	}
	if !data.NoIndex {
		h.setCanonical(r, data, r.URL.Path)
	}
}

func ogLocale(lang string) string {
	if lang == i18n.LangKY {
		return "ky_KG"
	}
	return "ru_RU"
}

// truncateText shortens s to at most n runes on a word boundary, adding
// an ellipsis — for meta descriptions built from product descriptions.
func truncateText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)[:n-1]
	if i := strings.LastIndex(string(r), " "); i > n/2 {
		return strings.TrimRight(string(r)[:i], ",.;:—- ") + "…"
	}
	return string(r) + "…"
}

// jsonLD encodes v as a JSON-LD script body. encoding/json escapes <, >
// and & so the result can't break out of the <script> element.
func jsonLD(v any) template.JS {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return template.JS(b) //nolint:gosec // JSON from encoding/json, HTML-significant chars escaped
}

// productJSONLD is the schema.org Product for a product page.
func productJSONLD(pd *ProductData, canonical string, price float64) template.JS {
	availability := "https://schema.org/OutOfStock"
	if pd.InStock {
		availability = "https://schema.org/InStock"
	}
	images := make([]string, 0, len(pd.Photos))
	for _, p := range pd.Photos {
		images = append(images, p.URL)
	}
	doc := map[string]any{
		"@context": "https://schema.org",
		"@type":    "Product",
		"name":     pd.Name,
		"url":      canonical,
		"offers": map[string]any{
			"@type":         "Offer",
			"url":           canonical,
			"priceCurrency": "KGS",
			"price":         fmt.Sprintf("%.0f", price),
			"availability":  availability,
			"itemCondition": "https://schema.org/NewCondition",
		},
	}
	if len(images) > 0 {
		doc["image"] = images
	}
	if pd.Description != "" {
		doc["description"] = truncateText(pd.Description, 500)
	}
	if pd.Brand != "" {
		doc["brand"] = map[string]any{"@type": "Brand", "name": pd.Brand}
	}
	return jsonLD(doc)
}

// websiteJSONLD describes the site and its search box (home page only).
func websiteJSONLD(siteURL string) template.JS {
	return jsonLD(map[string]any{
		"@context": "https://schema.org",
		"@type":    "WebSite",
		"name":     "Cozy",
		"url":      siteURL + "/",
		"potentialAction": map[string]any{
			"@type":       "SearchAction",
			"target":      siteURL + "/?q={search_term_string}",
			"query-input": "required name=search_term_string",
		},
	})
}
