package httpmw

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"time"
)

// AssetHashLen is how many hex chars of a file's SHA-256 go into its
// ?v= cache-busting parameter — plenty to tell deploys apart.
const AssetHashLen = 10

// VersionedMaxAge is the browser cache lifetime of a static URL whose ?v=
// matches the file's current content hash: the URL changes whenever the
// bytes do, so it can be cached for a year. Unversioned (or stale-
// versioned) requests keep the caller's short max-age.
const VersionedMaxAge = 365 * 24 * time.Hour

// AssetVersions maps a path relative to a static dir ("css/site.css") to
// the short content hash of that file, computed once at startup. Shared by
// the storefront (/static/) and the admin (/admin/static/).
type AssetVersions map[string]string

// LoadAssetVersions hashes every regular file under dir.
func LoadAssetVersions(dir string) (AssetVersions, error) {
	out := AssetVersions{}
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
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])[:AssetHashLen]
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("httpmw: hashing static assets in %s: %w", dir, err)
	}
	return out, nil
}

// URL is prefix+rel+"?v=<hash>", or the plain prefix+rel for a file it
// doesn't know (never a broken link). prefix ends in "/" ("/static/").
func (v AssetVersions) URL(prefix, rel string) string {
	if h, ok := v[rel]; ok {
		return prefix + rel + "?v=" + h
	}
	return prefix + rel
}

// VersionedStatic serves dir (mounted behind a StripPrefix) with a
// year-long Cache-Control when the request's ?v= is the file's current
// hash, and shortMaxAge otherwise — so an HTML page cached from before a
// deploy never pins an old file for a year.
func VersionedStatic(dir string, versions AssetVersions, shortMaxAge time.Duration) http.Handler {
	short := Static(dir, shortMaxAge)
	long := Static(dir, VersionedMaxAge)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := path.Clean("/" + r.URL.Path)[1:]
		if v := r.URL.Query().Get("v"); v != "" && v == versions[rel] {
			long.ServeHTTP(w, r)
			return
		}
		short.ServeHTTP(w, r)
	})
}
