package admin

import (
	"net/http/httptest"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/staff"
)

// newTestRenderer parses the real templates from the repo root — see
// repoRoot/chdir in render_test.go — so these tests exercise the exact
// .gohtml files that ship, not a copy.
func newTestRenderer(t *testing.T) *Renderer {
	t.Helper()
	restore := chdir(t, repoRoot(t))
	defer restore()

	rr, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	return rr
}

// TestRenderLoginExecutes exercises the login screen's states (no
// error, sticky phone + error after a failed attempt) end to end — a nil
// field reference in login.gohtml would panic at Execute time, not parse
// time, so TestNewRendererParsesAllScreens alone wouldn't catch it. This
// is also the "static render in a test" visual-check substitute the task
// brief allows in place of a manual curl round-trip (no live Postgres on
// this machine).
func TestRenderLoginExecutes(t *testing.T) {
	rr := newTestRenderer(t)

	cases := []struct {
		name string
		data PageData
	}{
		{"fresh", PageData{Screen: "login", PageTitle: "Вход", ShowSidebar: false}},
		{"with error", PageData{Screen: "login", PageTitle: "Вход", ShowSidebar: false, Phone: "+996555123456", Err: "неверный телефон или пароль"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			if err := rr.Render(w, "login", c.data); err != nil {
				t.Fatalf("Render: %v", err)
			}
			if w.Code != 200 {
				t.Fatalf("status = %d, want 200", w.Code)
			}
		})
	}
}

// TestRenderNoAccessExecutes exercises the point_staff landing page.
func TestRenderNoAccessExecutes(t *testing.T) {
	rr := newTestRenderer(t)

	st := &staff.Staff{ID: "s1", Name: "Данияр К.", Role: staff.RolePointStaff, IsActive: true}
	w := httptest.NewRecorder()
	data := PageData{Screen: "no_access", PageTitle: "Нет доступа", ShowSidebar: false, Staff: st}
	if err := rr.Render(w, "no_access", data); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

// TestRenderShellScreensExecute exercises every stub screen inside the
// full app shell (sidebar + header), for both an owner (sees all 5 nav
// items) and a manager (sees 3) — covers the sidebar/header partials
// executing with a real *staff.Staff and a non-empty NavItems slice, not
// just the bare stub content.
func TestRenderShellScreensExecute(t *testing.T) {
	rr := newTestRenderer(t)

	owner := &staff.Staff{ID: "s1", Name: "Айгерим Б.", Role: staff.RoleOwner, IsActive: true}
	manager := &staff.Staff{ID: "s2", Name: "Данияр К.", Role: staff.RoleManager, IsActive: true}

	screens := []struct {
		screen string
		title  string
	}{
		{"orders", "Заказы"},
		{"products", "Товары"},
		{"reports", "Отчёты"},
		{"points", "Склад и точки"},
		{"staff", "Сотрудники"},
	}

	for _, sc := range screens {
		for _, st := range []*staff.Staff{owner, manager} {
			t.Run(sc.screen+"/"+string(st.Role), func(t *testing.T) {
				data := PageData{
					Screen:      sc.screen,
					PageTitle:   sc.title,
					ShowSidebar: true,
					Staff:       st,
					Initials:    initialsFor(st.Name),
					RoleLabel:   roleLabel(st.Role),
					NavItems:    navItemsForRole(st.Role, sc.screen),
				}
				w := httptest.NewRecorder()
				if err := rr.Render(w, sc.screen, data); err != nil {
					t.Fatalf("Render: %v", err)
				}
				if w.Code != 200 {
					t.Fatalf("status = %d, want 200", w.Code)
				}
			})
		}
	}
}
