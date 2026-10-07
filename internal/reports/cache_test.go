package reports

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSource counts calls per query and returns rows derived from the
// arguments, so a wrong cache hit (rows of another key) is visible.
type fakeSource struct {
	calls atomic.Int64
	err   error
	// gate, when set, blocks every load until closed (singleflight test).
	gate    chan struct{}
	started chan struct{}
	// panicMsg, when set, makes every load panic.
	panicMsg atomic.Value
	// inFlight / maxInFlight track concurrent loads (semaphore test).
	inFlight    atomic.Int64
	maxInFlight atomic.Int64
}

func (f *fakeSource) rows(label string, from, to time.Time) ([]Row, error) {
	f.calls.Add(1)
	n := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		m := f.maxInFlight.Load()
		if n <= m || f.maxInFlight.CompareAndSwap(m, n) {
			break
		}
	}
	if msg, ok := f.panicMsg.Load().(string); ok && msg != "" {
		panic(msg)
	}
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	if f.gate != nil {
		<-f.gate
	}
	if f.err != nil {
		return nil, f.err
	}
	return []Row{{Key: label + from.Format(dateLayout) + to.Format(dateLayout), OrderCount: 1}}, nil
}

func (f *fakeSource) Sales(_ context.Context, from, to time.Time, groupBy GroupBy, pointNames map[string]string) ([]Row, error) {
	label := "sales:" + string(groupBy) + ":"
	for id, name := range pointNames {
		label += id + "=" + name + ";"
	}
	return f.rows(label, from, to)
}

func (f *fakeSource) BrandSales(_ context.Context, from, to time.Time) ([]Row, error) {
	return f.rows("brand:", from, to)
}

func (f *fakeSource) CategorySales(_ context.Context, from, to time.Time) ([]Row, error) {
	return f.rows("category:", from, to)
}

func (f *fakeSource) PointNames(context.Context) (map[string]string, error) {
	f.calls.Add(1)
	return map[string]string{"p1": "ЦУМ"}, f.err
}

// fakeClock is a settable clock for expiry tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

var (
	cacheNow = time.Date(2026, 10, 7, 12, 0, 0, 0, Location)
	// A closed year (ends before cacheNow) and a range that includes today.
	closedFrom = time.Date(2025, 1, 1, 0, 0, 0, 0, Location)
	closedTo   = time.Date(2026, 1, 1, 0, 0, 0, 0, Location)
	openFrom   = time.Date(2026, 10, 1, 0, 0, 0, 0, Location)
	openTo     = time.Date(2026, 10, 8, 0, 0, 0, 0, Location)
)

func newTestCache(src Source, maxEntries int) (*CachedRepo, *fakeClock) {
	clock := &fakeClock{now: cacheNow}
	c := NewCachedRepo(src, CacheConfig{
		TTL:        5 * time.Minute,
		OpenTTL:    time.Minute,
		MaxEntries: maxEntries,
		Now:        clock.Now,
	})
	return c, clock
}

func TestCachedRepo_HitServesSecondCallFromCache(t *testing.T) {
	src := &fakeSource{}
	c, _ := newTestCache(src, 0)
	ctx := context.Background()

	first, err := c.Sales(ctx, closedFrom, closedTo, GroupByDay, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Sales(ctx, closedFrom, closedTo, GroupByDay, nil)
	if err != nil {
		t.Fatal(err)
	}

	if got := src.calls.Load(); got != 1 {
		t.Fatalf("source calls = %d, want 1", got)
	}
	if len(second) != 1 || second[0] != first[0] {
		t.Fatalf("cached rows = %+v, want %+v", second, first)
	}
}

func TestCachedRepo_ReturnsCopiesSoCallersCannotCorruptCache(t *testing.T) {
	src := &fakeSource{}
	c, _ := newTestCache(src, 0)
	ctx := context.Background()

	first, _ := c.BrandSales(ctx, closedFrom, closedTo)
	want := first[0]
	first[0].Revenue = 999

	second, _ := c.BrandSales(ctx, closedFrom, closedTo)
	if second[0] != want {
		t.Fatalf("cache entry mutated through returned slice: %+v", second[0])
	}
}

func TestCachedRepo_KeySeparatesEveryParameter(t *testing.T) {
	src := &fakeSource{}
	c, _ := newTestCache(src, 0)
	ctx := context.Background()
	other := closedTo.AddDate(0, 0, -1)

	calls := []func() ([]Row, error){
		func() ([]Row, error) { return c.Sales(ctx, closedFrom, closedTo, GroupByDay, nil) },
		func() ([]Row, error) { return c.Sales(ctx, closedFrom, closedTo, GroupByProduct, nil) },
		func() ([]Row, error) { return c.Sales(ctx, closedFrom, other, GroupByDay, nil) },
		func() ([]Row, error) { return c.Sales(ctx, closedFrom.AddDate(0, 0, 1), closedTo, GroupByDay, nil) },
		func() ([]Row, error) {
			return c.Sales(ctx, closedFrom, closedTo, GroupByPoint, map[string]string{"p1": "ЦУМ"})
		},
		func() ([]Row, error) {
			return c.Sales(ctx, closedFrom, closedTo, GroupByPoint, map[string]string{"p1": "Дордой"})
		},
		func() ([]Row, error) {
			return c.Sales(ctx, closedFrom, closedTo, GroupByPoint, map[string]string{"p1": "ЦУМ", "p2": "Дордой"})
		},
		func() ([]Row, error) { return c.BrandSales(ctx, closedFrom, closedTo) },
		func() ([]Row, error) { return c.CategorySales(ctx, closedFrom, closedTo) },
	}

	seen := map[string]bool{}
	for i, call := range calls {
		rows, err := call()
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if seen[rows[0].Key] {
			t.Fatalf("call %d returned rows of another key: %q", i, rows[0].Key)
		}
		seen[rows[0].Key] = true
	}
	if got, want := src.calls.Load(), int64(len(calls)); got != want {
		t.Fatalf("source calls = %d, want %d (one per distinct key)", got, want)
	}
}

func TestCachedRepo_SameInstantInAnotherZoneSharesEntry(t *testing.T) {
	src := &fakeSource{}
	c, _ := newTestCache(src, 0)
	ctx := context.Background()

	_, _ = c.CategorySales(ctx, closedFrom, closedTo)
	_, _ = c.CategorySales(ctx, closedFrom.UTC(), closedTo.UTC())

	if got := src.calls.Load(); got != 1 {
		t.Fatalf("source calls = %d, want 1 (same instants, same result)", got)
	}
}

func TestCachedRepo_ClosedRangeExpiresAfterTTL(t *testing.T) {
	src := &fakeSource{}
	c, clock := newTestCache(src, 0)
	ctx := context.Background()

	_, _ = c.Sales(ctx, closedFrom, closedTo, GroupByDay, nil)
	clock.Advance(5*time.Minute - time.Second)
	_, _ = c.Sales(ctx, closedFrom, closedTo, GroupByDay, nil)
	if got := src.calls.Load(); got != 1 {
		t.Fatalf("before TTL: source calls = %d, want 1", got)
	}

	clock.Advance(2 * time.Second)
	_, _ = c.Sales(ctx, closedFrom, closedTo, GroupByDay, nil)
	if got := src.calls.Load(); got != 2 {
		t.Fatalf("after TTL: source calls = %d, want 2", got)
	}
}

func TestCachedRepo_RangeIncludingTodayUsesShorterTTL(t *testing.T) {
	src := &fakeSource{}
	c, clock := newTestCache(src, 0)
	ctx := context.Background()

	_, _ = c.Sales(ctx, openFrom, openTo, GroupByDay, nil)
	clock.Advance(time.Minute - time.Second)
	_, _ = c.Sales(ctx, openFrom, openTo, GroupByDay, nil)
	if got := src.calls.Load(); got != 1 {
		t.Fatalf("before open TTL: source calls = %d, want 1", got)
	}

	clock.Advance(2 * time.Second)
	_, _ = c.Sales(ctx, openFrom, openTo, GroupByDay, nil)
	if got := src.calls.Load(); got != 2 {
		t.Fatalf("after open TTL: source calls = %d, want 2", got)
	}
}

func TestCachedRepo_ErrorsAreNotCached(t *testing.T) {
	boom := errors.New("db down")
	src := &fakeSource{err: boom}
	c, _ := newTestCache(src, 0)
	ctx := context.Background()

	if _, err := c.BrandSales(ctx, closedFrom, closedTo); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	src.err = nil
	rows, err := c.BrandSales(ctx, closedFrom, closedTo)
	if err != nil || len(rows) != 1 {
		t.Fatalf("after recovery: rows=%v err=%v, want a fresh load", rows, err)
	}
	if got := src.calls.Load(); got != 2 {
		t.Fatalf("source calls = %d, want 2 (error result must not be cached)", got)
	}
}

func TestCachedRepo_ConcurrentIdenticalRequestsRunQueryOnce(t *testing.T) {
	const workers = 20
	src := &fakeSource{gate: make(chan struct{}), started: make(chan struct{}, 1)}
	c, _ := newTestCache(src, 0)
	ctx := context.Background()

	var wg sync.WaitGroup
	results := make([][]Row, workers)
	errs := make([]error, workers)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = c.Sales(ctx, closedFrom, closedTo, GroupByProduct, nil)
		}()
	}

	<-src.started // the one load is in flight; give the rest time to join it
	time.Sleep(50 * time.Millisecond)
	close(src.gate)
	wg.Wait()

	if got := src.calls.Load(); got != 1 {
		t.Fatalf("source calls = %d, want 1", got)
	}
	for i := range workers {
		if errs[i] != nil || len(results[i]) != 1 {
			t.Fatalf("worker %d: rows=%v err=%v", i, results[i], errs[i])
		}
	}
}

func TestCachedRepo_WaiterHonoursOwnContext(t *testing.T) {
	src := &fakeSource{gate: make(chan struct{}), started: make(chan struct{}, 1)}
	c, _ := newTestCache(src, 0)

	done := make(chan error, 1)
	go func() {
		_, err := c.BrandSales(context.Background(), closedFrom, closedTo)
		done <- err
	}()
	<-src.started

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.BrandSales(ctx, closedFrom, closedTo); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter err = %v, want context.Canceled", err)
	}

	close(src.gate)
	if err := <-done; err != nil {
		t.Fatalf("leader err = %v", err)
	}
}

func TestCachedRepo_BoundedSize(t *testing.T) {
	const maxEntries = 3
	src := &fakeSource{}
	c, _ := newTestCache(src, maxEntries)
	ctx := context.Background()

	for i := range 10 {
		from := closedFrom.AddDate(0, 0, i)
		if _, err := c.CategorySales(ctx, from, closedTo); err != nil {
			t.Fatal(err)
		}
	}
	if got := c.Len(); got > maxEntries {
		t.Fatalf("cache entries = %d, want <= %d", got, maxEntries)
	}
	// The newest entry survives eviction.
	before := src.calls.Load()
	_, _ = c.CategorySales(ctx, closedFrom.AddDate(0, 0, 9), closedTo)
	if src.calls.Load() != before {
		t.Fatal("most recent entry was evicted")
	}
}

func TestCachedRepo_PointNamesPassesThrough(t *testing.T) {
	src := &fakeSource{}
	c, _ := newTestCache(src, 0)

	_, _ = c.PointNames(context.Background())
	_, _ = c.PointNames(context.Background())
	if got := src.calls.Load(); got != 2 {
		t.Fatalf("source calls = %d, want 2 (point names are not cached)", got)
	}
}

func TestNewCachedRepo_Defaults(t *testing.T) {
	c := NewCachedRepo(&fakeSource{}, CacheConfig{})
	if c.cfg.TTL != DefaultCacheTTL || c.cfg.OpenTTL != DefaultOpenCacheTTL || c.cfg.MaxEntries != DefaultCacheMaxEntries ||
		c.cfg.MaxConcurrentLoads != DefaultCacheMaxConcurrentLoads || c.cfg.Now == nil {
		t.Fatalf("defaults not applied: %+v", c.cfg)
	}
	if DefaultCacheTTL > 5*time.Minute || DefaultOpenCacheTTL > DefaultCacheTTL {
		t.Fatalf("TTLs too long: closed %v, open %v", DefaultCacheTTL, DefaultOpenCacheTTL)
	}
}

// waitFor polls cond until it holds or the test times out.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCachedRepo_PanicInQueryBecomesErrorAndIsNotCached(t *testing.T) {
	src := &fakeSource{}
	src.panicMsg.Store("boom")
	c, _ := newTestCache(src, 0)
	ctx := context.Background()

	_, err := c.CategorySales(ctx, closedFrom, closedTo)
	if !errors.Is(err, ErrQueryPanicked) {
		t.Fatalf("err = %v, want ErrQueryPanicked", err)
	}

	src.panicMsg.Store("")
	rows, err := c.CategorySales(ctx, closedFrom, closedTo)
	if err != nil || len(rows) != 1 {
		t.Fatalf("after panic: rows=%v err=%v, want a fresh load", rows, err)
	}
}

func TestCachedRepo_CancelledContextStartsNoQuery(t *testing.T) {
	src := &fakeSource{}
	c, _ := newTestCache(src, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := c.BrandSales(ctx, closedFrom, closedTo); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := src.calls.Load(); got != 0 {
		t.Fatalf("source calls = %d, want 0", got)
	}
}

func TestCachedRepo_CapsConcurrentDistinctLoads(t *testing.T) {
	const limit, keys = 2, 6
	src := &fakeSource{gate: make(chan struct{})}
	c := NewCachedRepo(src, CacheConfig{MaxConcurrentLoads: limit, Now: func() time.Time { return cacheNow }})
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make([]error, keys)
	for i := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = c.CategorySales(ctx, closedFrom.AddDate(0, 0, i), closedTo)
		}()
	}
	waitFor(t, "loads to start", func() bool { return src.inFlight.Load() == limit })
	time.Sleep(50 * time.Millisecond) // the others are queued, not running
	if got := src.maxInFlight.Load(); got != limit {
		t.Fatalf("concurrent loads = %d, want %d", got, limit)
	}
	close(src.gate)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("key %d: %v", i, err)
		}
	}
	if got := src.maxInFlight.Load(); got > limit {
		t.Fatalf("concurrent loads peaked at %d, want <= %d", got, limit)
	}
}

func TestCachedRepo_WaitForLoadSlotHonoursCallerContext(t *testing.T) {
	src := &fakeSource{gate: make(chan struct{})}
	c := NewCachedRepo(src, CacheConfig{MaxConcurrentLoads: 1, Now: func() time.Time { return cacheNow }})

	done := make(chan error, 1)
	go func() {
		_, err := c.BrandSales(context.Background(), closedFrom, closedTo)
		done <- err
	}()
	waitFor(t, "slot holder to start", func() bool { return src.inFlight.Load() == 1 })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.CategorySales(ctx, closedFrom, closedTo); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued caller err = %v, want context.DeadlineExceeded", err)
	}
	if got := src.calls.Load(); got != 1 {
		t.Fatalf("source calls = %d, want 1 (queued query never ran)", got)
	}

	close(src.gate)
	if err := <-done; err != nil {
		t.Fatalf("slot holder err = %v", err)
	}
}

func TestCachedRepo_CancelledLeaderDoesNotFailOtherWaiters(t *testing.T) {
	src := &fakeSource{gate: make(chan struct{}), started: make(chan struct{}, 1)}
	c, _ := newTestCache(src, 0)

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() {
		_, err := c.Sales(leaderCtx, closedFrom, closedTo, GroupByDay, nil)
		leaderDone <- err
	}()
	<-src.started

	waiterDone := make(chan error, 1)
	go func() {
		rows, err := c.Sales(context.Background(), closedFrom, closedTo, GroupByDay, nil)
		if err == nil && len(rows) != 1 {
			err = errors.New("no rows")
		}
		waiterDone <- err
	}()
	time.Sleep(50 * time.Millisecond) // waiter joins the flight

	cancelLeader()
	if err := <-leaderDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader err = %v, want context.Canceled", err)
	}
	close(src.gate)
	if err := <-waiterDone; err != nil {
		t.Fatalf("waiter failed because the leader went away: %v", err)
	}
	if got := src.calls.Load(); got != 1 {
		t.Fatalf("source calls = %d, want 1", got)
	}
}

func TestCachedRepo_LeaderCancelledWhileQueuedDoesNotFailWaiters(t *testing.T) {
	src := &fakeSource{gate: make(chan struct{})}
	c := NewCachedRepo(src, CacheConfig{MaxConcurrentLoads: 1, Now: func() time.Time { return cacheNow }})

	// Occupy the only load slot.
	holderDone := make(chan error, 1)
	go func() {
		_, err := c.BrandSales(context.Background(), closedFrom, closedTo)
		holderDone <- err
	}()
	waitFor(t, "slot holder to start", func() bool { return src.inFlight.Load() == 1 })

	// A leader queues for the slot; a waiter joins its flight.
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() {
		_, err := c.CategorySales(leaderCtx, closedFrom, closedTo)
		leaderDone <- err
	}()
	time.Sleep(20 * time.Millisecond)
	waiterDone := make(chan error, 1)
	go func() {
		rows, err := c.CategorySales(context.Background(), closedFrom, closedTo)
		if err == nil && len(rows) != 1 {
			err = errors.New("no rows")
		}
		waiterDone <- err
	}()
	time.Sleep(20 * time.Millisecond)

	cancelLeader()
	if err := <-leaderDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader err = %v, want context.Canceled", err)
	}
	close(src.gate)
	if err := <-waiterDone; err != nil {
		t.Fatalf("waiter failed because the queued leader went away: %v", err)
	}
	if err := <-holderDone; err != nil {
		t.Fatalf("slot holder err = %v", err)
	}
}

func TestCachedRepo_KeyHandlesYearsOutsideUnixNanoRange(t *testing.T) {
	src := &fakeSource{}
	c, _ := newTestCache(src, 0)
	ctx := context.Background()
	to := time.Date(3000, 1, 1, 0, 0, 0, 0, Location)

	// UnixNano is undefined outside ~1678..2262; distinct far years must
	// still be distinct keys.
	for _, year := range []int{1000, 1001, 1600} {
		if _, err := c.CategorySales(ctx, time.Date(year, 1, 1, 0, 0, 0, 0, Location), to); err != nil {
			t.Fatal(err)
		}
	}
	if got := src.calls.Load(); got != 3 {
		t.Fatalf("source calls = %d, want 3 (one per distinct range)", got)
	}
}
