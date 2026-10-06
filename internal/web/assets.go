package web

import (
	"net/http"
	"net/url"

	"github.com/Nikemas/cozy_backend/internal/httpmw"
)

// staticDir holds the site's CSS/images/fonts, served under /static/.
const staticDir = "web/static"

// staticURLPrefix is where staticDir is mounted (routes.go).
const staticURLPrefix = "/static/"

// assetHashLen is how many hex chars of a file's SHA-256 go into its
// ?v= cache-busting parameter (httpmw.AssetHashLen).
const assetHashLen = httpmw.AssetHashLen

// assetVersions maps a path relative to staticDir ("css/site.css") to the
// short content hash of that file, computed once at startup.
type assetVersions httpmw.AssetVersions

// loadAssetVersions hashes every regular file under dir.
func loadAssetVersions(dir string) (assetVersions, error) {
	v, err := httpmw.LoadAssetVersions(dir)
	return assetVersions(v), err
}

// url is the "asset" template func: /static/<rel>?v=<hash>, or the plain
// /static/<rel> for a file it doesn't know (never a broken link).
func (v assetVersions) url(rel string) string {
	return httpmw.AssetVersions(v).URL(staticURLPrefix, rel)
}

// staticHandler serves staticDir (mounted behind StripPrefix("/static/"))
// with a year-long Cache-Control when the request's ?v= is the file's
// current hash, and the short staticMaxAge otherwise — so an HTML page
// cached from before a deploy never pins an old file for a year.
func staticHandler(dir string, versions assetVersions) http.Handler {
	return httpmw.VersionedStatic(dir, httpmw.AssetVersions(versions), staticMaxAge)
}

// originOf is rawURL's scheme://host, or "" if it has none.
func originOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
