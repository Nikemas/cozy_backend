package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/importguard"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

const (
	testGuardSlotWait = 20 * time.Millisecond
	guardCSV          = "name_ru,category,price\nКеды,sneakers,1990\n"
)

// countingBody records whether the handler read the request body.
type countingBody struct {
	io.ReadCloser
	reads atomic.Int32
}

func (c *countingBody) Read(p []byte) (int, error) {
	c.reads.Add(1)
	return c.ReadCloser.Read(p)
}

// recordingLimiter allows n requests and records the keys asked about.
type recordingLimiter struct {
	n    int
	keys []string
}

func (l *recordingLimiter) Allow(key string) (bool, time.Duration) {
	l.keys = append(l.keys, key)
	if l.n <= 0 {
		return false, 42 * time.Second
	}
	l.n--
	return true, 0
}

// guardedImportRequest is a valid JSON-client upload (Accept:
// application/json) whose body reads are counted.
func guardedImportRequest(t *testing.T, lang string) (*http.Request, *countingBody) {
	t.Helper()
	r := newImportRequest(t, "products.csv", "", guardCSV)
	r.Header.Set("Accept", "application/json")
	if lang != "" {
		r.Header.Set("Accept-Language", lang)
	}
	body := &countingBody{ReadCloser: r.Body}
	r.Body = body
	return r, body
}

type errorEnvelope struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func decodeEnvelope(t *testing.T, w *httptest.ResponseRecorder) errorEnvelope {
	t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("want the JSON error envelope, got %q: %v", w.Body.String(), err)
	}
	return env
}

func TestImportJSONRefusesRequestWithoutStaffID(t *testing.T) {
	noID := &staff.Staff{Role: staff.RoleManager, IsActive: true}
	contexts := map[string]context.Context{
		"no staff":       context.Background(),
		"empty staff ID": staff.NewContextWithStaff(context.Background(), noID),
	}
	for name, ctx := range contexts {
		t.Run(name, func(t *testing.T) {
			// Arrange
			backend := newImportDeps()
			lim := &recordingLimiter{n: 100}
			guard := &importguard.Guard{Limiter: lim, Gate: importguard.NewGate(2, testGuardSlotWait)}
			r, body := guardedImportRequest(t, "")
			r = r.WithContext(ctx)
			w := httptest.NewRecorder()

			// Act
			apperr.Wrap(importProductsHandler(backend, guard))(w, r)

			// Assert
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", w.Code, w.Body.String())
			}
			if env := decodeEnvelope(t, w); env.Code != "forbidden" {
				t.Errorf("code = %q, want forbidden", env.Code)
			}
			if len(lim.keys) != 0 {
				t.Errorf("limiter asked about %v: an empty ID must not share one bucket", lim.keys)
			}
			if body.reads.Load() != 0 {
				t.Error("the body must not be read without a staff ID")
			}
			if backend.products != 0 || backend.dryRuns != 0 {
				t.Error("nothing may be imported without a staff ID")
			}
		})
	}
}

func TestImportJSONRateLimited(t *testing.T) {
	cases := map[string]struct{ lang, wantMsgLang string }{
		"ru": {"ru", "ru"},
		"ky": {"ky", "ky"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			// Arrange: no tokens left, a free slot.
			backend := newImportDeps()
			lim := &recordingLimiter{n: 0}
			guard := &importguard.Guard{Limiter: lim, Gate: importguard.NewGate(1, testGuardSlotWait)}
			r, body := guardedImportRequest(t, c.lang)
			w := httptest.NewRecorder()

			// Act
			apperr.Wrap(importProductsHandler(backend, guard))(w, r)

			// Assert
			if w.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429: %s", w.Code, w.Body.String())
			}
			if got := w.Header().Get("Retry-After"); got != "42" {
				t.Errorf("Retry-After = %q, want 42", got)
			}
			env := decodeEnvelope(t, w)
			if env.Code != "import_rate_limited" {
				t.Errorf("code = %q, want import_rate_limited", env.Code)
			}
			want := apperr.Translate(c.wantMsgLang, "err.import_rate_limited", nil)
			if env.Message != want || want == "" {
				t.Errorf("message = %q, want %q", env.Message, want)
			}
			if lim.keys[0] != testImportStaff.ID {
				t.Errorf("limited by %q, want the staff ID", lim.keys[0])
			}
			if body.reads.Load() != 0 {
				t.Error("the body must not be read when rate limited")
			}
			if guard.Gate.Held() != 0 {
				t.Error("a rate-limited request must not hold a slot")
			}
		})
	}
}

// TestImportJSONSharesGateWithHTMLPage: the HTML page takes its slots
// from the same importguard.Gate (cmd/server hands both the one Guard),
// so with both slots held there, the JSON endpoint is busy too.
func TestImportJSONSharesGateWithHTMLPage(t *testing.T) {
	// Arrange: two page imports hold both slots of the shared gate.
	backend := newImportDeps()
	guard := &importguard.Guard{Limiter: &recordingLimiter{n: 100}, Gate: importguard.NewGate(2, testGuardSlotWait)}
	for range 2 {
		release, ok := guard.Gate.Acquire(context.Background())
		if !ok {
			t.Fatal("setup: could not take a slot")
		}
		defer release()
	}
	r, body := guardedImportRequest(t, "ru")
	w := httptest.NewRecorder()

	// Act
	apperr.Wrap(importProductsHandler(backend, guard))(w, r)

	// Assert
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Retry-After"); got != importguard.RetryAfterSeconds(importguard.BusyRetryAfter) {
		t.Errorf("Retry-After = %q", got)
	}
	if env := decodeEnvelope(t, w); env.Code != "import_busy" {
		t.Errorf("code = %q, want import_busy", env.Code)
	}
	if body.reads.Load() != 0 {
		t.Error("the body must not be read before a slot is acquired")
	}
	if backend.dryRuns != 0 || backend.products != 0 {
		t.Error("nothing may run without a slot")
	}
}

// TestImportJSONSharesRateLimitWithHTMLPage: the page spends tokens from
// the same per-staff bucket (importguard.Guard.Begin), so a staff member
// who used up the burst there is refused on the JSON endpoint.
func TestImportJSONSharesRateLimitWithHTMLPage(t *testing.T) {
	// Arrange: the page used the whole burst for this staff member.
	guard := importguard.New()
	for i := range importguard.RateBurst {
		d := guard.Begin(context.Background(), testImportStaff.ID)
		if d.Outcome != importguard.Allowed {
			t.Fatalf("setup request %d: %v", i, d.Outcome)
		}
		d.Release()
	}
	r, body := guardedImportRequest(t, "ru")
	w := httptest.NewRecorder()

	// Act
	apperr.Wrap(importProductsHandler(newImportDeps(), guard))(w, r)

	// Assert
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429: %s", w.Code, w.Body.String())
	}
	if env := decodeEnvelope(t, w); env.Code != "import_rate_limited" {
		t.Errorf("code = %q, want import_rate_limited", env.Code)
	}
	if body.reads.Load() != 0 {
		t.Error("the body must not be read when rate limited")
	}
}

func TestImportJSONReleasesSlotAfterImport(t *testing.T) {
	// Arrange
	guard := &importguard.Guard{Limiter: &recordingLimiter{n: 100}, Gate: importguard.NewGate(1, testGuardSlotWait)}
	handler := apperr.Wrap(importProductsHandler(newImportDeps(), guard))

	for i := range 3 {
		r, _ := guardedImportRequest(t, "ru")
		w := httptest.NewRecorder()

		// Act
		handler(w, r)

		// Assert
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200: %s", i, w.Code, w.Body.String())
		}
		if guard.Gate.Held() != 0 {
			t.Fatalf("request %d left a slot held", i)
		}
	}
}
