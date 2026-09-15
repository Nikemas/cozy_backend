package admin

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// chdir switches the process's working directory to dir for the duration
// of the test, restoring the original on cleanup — NewRenderer resolves
// its "admin/templates/..." paths relative to cwd, matching how
// cmd/server actually runs (from the repo root). Mirrors internal/web's
// render_test.go helper of the same name.
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
// (internal/admin/render_test.go), so NewRenderer's relative
// "admin/templates" path resolves correctly however `go test` is invoked.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// TestNewRendererParsesAllScreens is a cheap smoke test that every
// screen's .gohtml file parses without error alongside the shared layout
// partials — the one end-to-end check of the template layer this repo can
// run without a live Postgres (no live DB on this machine, see the task
// brief's Verification section).
func TestNewRendererParsesAllScreens(t *testing.T) {
	restore := chdir(t, repoRoot(t))
	defer restore()

	if _, err := NewRenderer(); err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
}
