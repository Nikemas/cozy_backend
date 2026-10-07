package admin

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// importEndpoints are the two handlers that read and run an upload.
var importEndpoints = map[string]struct {
	path string
	fn   func(*handlers, http.ResponseWriter, *http.Request)
}{
	"check": {importCheckPath, (*handlers).productImportCheck},
	"apply": {importApplyPath, (*handlers).productImportApply},
}

func TestImportRefusesRequestWithoutStaffID(t *testing.T) {
	noID := &staff.Staff{Role: staff.RoleManager, IsActive: true}
	contexts := map[string]context.Context{
		"no staff":       context.Background(),
		"empty staff ID": staff.NewContextWithStaff(context.Background(), noID),
	}
	for epName, ep := range importEndpoints {
		for ctxName, ctx := range contexts {
			t.Run(epName+"/"+ctxName, func(t *testing.T) {
				// Arrange
				imp := &fakeImporter{result: cleanImportResult()}
				h := newImportHandlers(t, imp)
				lim := &fakeImportLimiter{n: 100}
				h.importLimiter = lim
				r := importRequest(t, ep.path, "A", "pt1", "", true)
				r = r.WithContext(ctx)
				w := httptest.NewRecorder()

				// Act
				ep.fn(h, w, r)

				// Assert
				if w.Code != http.StatusForbidden {
					t.Errorf("status = %d, want 403", w.Code)
				}
				if len(lim.keys) != 0 {
					t.Errorf("limiter asked about %v: an empty ID must not share one bucket", lim.keys)
				}
				if len(imp.calls) != 0 {
					t.Errorf("importer called without a staff ID: %+v", imp.calls)
				}
				if !strings.Contains(w.Body.String(), ruTr.T("admin.err.generic")) {
					t.Errorf("want the generic error:\n%s", w.Body.String())
				}
			})
		}
	}
}

// blockingImporter holds every Import until release is closed, so tests
// can keep import slots busy; with panicMsg set it then panics. Safe for
// concurrent use.
type blockingImporter struct {
	entered  chan struct{}
	release  chan struct{}
	panicMsg string
	calls    atomic.Int32
}

func newBlockingImporter() *blockingImporter {
	return &blockingImporter{entered: make(chan struct{}, 8), release: make(chan struct{})}
}

func (b *blockingImporter) Import(_ context.Context, r io.Reader, _ catalog.ImportFormat, opts catalog.ImportOptions) (*catalog.ImportResult, error) {
	_, _ = io.ReadAll(r)
	b.calls.Add(1)
	b.entered <- struct{}{}
	<-b.release
	if b.panicMsg != "" {
		panic(b.panicMsg)
	}
	res := *cleanImportResult()
	res.DryRun = opts.DryRun
	return &res, nil
}

func (b *blockingImporter) ActivePoints(context.Context) ([]catalog.ImportPoint, error) {
	return nil, nil
}

// countingBody records whether the handler read the request body.
type countingBody struct {
	io.ReadCloser
	reads atomic.Int32
}

func (c *countingBody) Read(p []byte) (int, error) {
	c.reads.Add(1)
	return c.ReadCloser.Read(p)
}

const testSlotWait = 20 * time.Millisecond

func TestImportGateLimitsConcurrentHolders(t *testing.T) {
	// Arrange
	g := newImportGate(2, testSlotWait)
	ctx := context.Background()

	// Act
	rel1, ok1 := g.acquire(ctx)
	rel2, ok2 := g.acquire(ctx)
	_, ok3 := g.acquire(ctx)

	// Assert
	if !ok1 || !ok2 {
		t.Fatal("the first two holders must get a slot")
	}
	if ok3 {
		t.Fatal("a third concurrent holder must be refused")
	}
	rel1()
	rel1() // releasing twice must not free a second slot
	rel4, ok4 := g.acquire(ctx)
	if !ok4 {
		t.Fatal("a released slot must be reusable")
	}
	if _, ok := g.acquire(ctx); ok {
		t.Fatal("a double release must not free an extra slot")
	}
	rel2()
	rel4()
	if n := len(g.slots); n != 0 {
		t.Errorf("slots held after all releases = %d, want 0", n)
	}
}

func TestImportGateGivesUpWhenRequestEnds(t *testing.T) {
	// Arrange: the only slot is taken and the wait is long.
	g := newImportGate(1, time.Hour)
	rel, _ := g.acquire(context.Background())
	defer rel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Act
	start := time.Now()
	_, ok := g.acquire(ctx)

	// Assert
	if ok || time.Since(start) > time.Second {
		t.Fatalf("acquire on a cancelled request = %v after %v, want an immediate refusal", ok, time.Since(start))
	}
}

func TestNilImportGateIsUnlimited(t *testing.T) {
	var g *importGate
	rel, ok := g.acquire(context.Background())
	if !ok {
		t.Fatal("a nil gate must not limit")
	}
	rel()
}

func TestImportRefusesThirdConcurrentRequest(t *testing.T) {
	for name, ep := range importEndpoints {
		t.Run(name, func(t *testing.T) {
			// Arrange: two requests hold both slots inside the importer.
			imp := newBlockingImporter()
			h := &handlers{render: newTestRenderer(t), importer: imp, importTokenKey: []byte("test-key")}
			h.importGate = newImportGate(2, testSlotWait)
			holders := make([]*httptest.ResponseRecorder, 2)
			var wg sync.WaitGroup
			for i := range holders {
				holders[i] = httptest.NewRecorder()
				r := importRequest(t, importCheckPath, "A", "pt1", "", true)
				wg.Add(1)
				go func() {
					defer wg.Done()
					h.productImportCheck(holders[i], r)
				}()
			}
			for range holders {
				<-imp.entered
			}
			third := importRequest(t, ep.path, "A", "pt1", "tok", true)
			body := &countingBody{ReadCloser: third.Body}
			third.Body = body
			w := httptest.NewRecorder()

			// Act
			ep.fn(h, w, third)

			// Assert
			if w.Code != http.StatusTooManyRequests {
				t.Errorf("status = %d, want 429", w.Code)
			}
			if w.Header().Get("Retry-After") == "" {
				t.Error("a busy refusal must say when to retry")
			}
			if !strings.Contains(w.Body.String(), ruTr.T("admin.import.err_busy")) {
				t.Errorf("want the localized busy message:\n%s", w.Body.String())
			}
			if body.reads.Load() != 0 {
				t.Error("the body must not be read before a slot is acquired")
			}
			if n := imp.calls.Load(); n != 2 {
				t.Errorf("importer calls = %d, want only the two holders", n)
			}

			close(imp.release)
			wg.Wait()
			for i, hw := range holders {
				if hw.Code != http.StatusOK {
					t.Errorf("holder %d status = %d, want 200", i, hw.Code)
				}
			}
			next := httptest.NewRecorder()
			h.productImportCheck(next, importRequest(t, importCheckPath, "A", "pt1", "", true))
			<-imp.entered
			if next.Code != http.StatusOK {
				t.Errorf("after the holders finished, status = %d, want 200", next.Code)
			}
			if n := len(h.importGate.slots); n != 0 {
				t.Errorf("slots still held = %d, want 0", n)
			}
		})
	}
}

func TestImportSlotReleasedOnPanic(t *testing.T) {
	// Arrange: a single slot and an importer that panics.
	imp := newBlockingImporter()
	imp.panicMsg = "boom"
	close(imp.release)
	h := &handlers{render: newTestRenderer(t), importer: imp, importTokenKey: []byte("test-key")}
	h.importGate = newImportGate(1, testSlotWait)

	// Act
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the importer's panic should propagate")
			}
		}()
		h.productImportCheck(httptest.NewRecorder(), importRequest(t, importCheckPath, "A", "pt1", "", true))
	}()

	// Assert
	if n := len(h.importGate.slots); n != 0 {
		t.Fatalf("slots held after a panic = %d, want 0", n)
	}
	h.importer = &fakeImporter{result: cleanImportResult()}
	w := httptest.NewRecorder()
	h.productImportCheck(w, importRequest(t, importCheckPath, "A", "pt1", "", true))
	if w.Code != http.StatusOK {
		t.Errorf("request after a panic: status = %d, want 200", w.Code)
	}
}

func TestImportRateLimitCheckedBeforeSlot(t *testing.T) {
	// Arrange: no rate-limit tokens left, one free slot.
	h := newImportHandlers(t, &fakeImporter{result: cleanImportResult()})
	h.importLimiter = &fakeImportLimiter{n: 0}
	h.importGate = newImportGate(1, testSlotWait)

	// Act
	w := httptest.NewRecorder()
	h.productImportCheck(w, importRequest(t, importCheckPath, "A", "pt1", "", true))

	// Assert
	if !strings.Contains(w.Body.String(), ruTr.T("admin.import.err_rate_limited")) {
		t.Errorf("want the rate-limit message, not busy:\n%s", w.Body.String())
	}
	if n := len(h.importGate.slots); n != 0 {
		t.Errorf("a rate-limited request held a slot: %d", n)
	}
}
