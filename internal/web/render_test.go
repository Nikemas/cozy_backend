package web

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/i18n"
)

// chdir switches the process's working directory to dir for the duration
// of the test, restoring the original on cleanup. NewRenderer/i18n.Load
// resolve their template/locale paths relative to cwd, matching how
// cmd/server actually runs (from the repo root).
func chdir(t *testing.T, dir string) func() {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("os.Chdir(%q): %v", dir, err)
	}
	return func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatalf("os.Chdir(%q) restore: %v", orig, err)
		}
	}
}

// repoRoot resolves the repository root from this test file's own path
// (internal/web/render_test.go), so NewRenderer's relative "web/templates"
// and i18n.Load's "locales" paths resolve correctly however `go test` is
// invoked, without needing a live database.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// TestNewRendererParsesAllScreens is a cheap smoke test that every
// screen's .gohtml file (including cart.gohtml/checkout.gohtml/
// done.gohtml, which Task 3 fills in/adds) parses without error, in both
// languages, alongside the shared layout partials. It doesn't touch a
// database — NewRenderer only parses templates — so it's the one
// end-to-end check of the template layer this repo can run without a
// live Postgres.
func TestNewRendererParsesAllScreens(t *testing.T) {
	root := repoRoot(t)
	restore := chdir(t, root)
	defer restore()

	bundle, err := i18n.Load("locales")
	if err != nil {
		t.Fatalf("i18n.Load: %v", err)
	}

	if _, err := NewRenderer(bundle); err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
}
