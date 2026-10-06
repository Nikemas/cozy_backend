package web

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/Nikemas/cozy_backend/internal/httpmw"
)

// staticDir holds the site's CSS/images, served under /static/.
const staticDir = "web/static"

// assetHashLen is how many hex chars of a file's SHA-256 go into its
// ?v= cache-busting parameter — plenty to tell deploys apart.
const assetHashLen = 10

// versionedMaxAge is the browser cache lifetime of a /static/ URL whose
// ?v= matches the file's current content hash: the URL changes whenever
// the bytes do, so it can be cached for a year. Unversioned (or stale-
// versioned) requests keep the short staticMaxAge.
const versionedMaxAge = 365 * 24 * time.Hour

// assetVersions maps a path relative to staticDir ("css/site.css") to the
// short content hash of that file, computed once at startup.
type assetVersions map[string]string

// loadAssetVersions hashes every regular file under dir.
func loadAssetVersions(dir string) (assetVersions, error) {
	out := assetVersions{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p) //nolint:gosec // p comes from walking our own static dir
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])[:assetHashLen]
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("web: hashing static assets in %s: %w", dir, err)
	}
	return out, nil
}

// url is the "asset" template func: /static/<rel>?v=<hash>, or the plain
// /static/<rel> for a file it doesn't know (never a broken link).
func (v assetVersions) url(rel string) string {
	if h, ok := v[rel]; ok {
		return "/static/" + rel + "?v=" + h
	}
	return "/static/" + rel
}

// staticHandler serves staticDir (mounted behind StripPrefix("/static/"))
// with a year-long Cache-Control when the request's ?v= is the file's
// current hash, and the short staticMaxAge otherwise — so an HTML page
// cached from before a deploy never pins an old file for a year.
func staticHandler(dir string, versions assetVersions) http.Handler {
	short := httpmw.Static(dir, staticMaxAge)
	long := httpmw.Static(dir, versionedMaxAge)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := path.Clean("/" + r.URL.Path)[1:]
		if v := r.URL.Query().Get("v"); v != "" && v == versions[rel] {
			long.ServeHTTP(w, r)
			return
		}
		short.ServeHTTP(w, r)
	})
}

// originOf is rawURL's scheme://host, or "" if it has none.
func originOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
