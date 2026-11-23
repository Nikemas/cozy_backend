package reports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Source is the report query layer CachedRepo wraps — *Repo in production.
type Source interface {
	Sales(ctx context.Context, from, to time.Time, groupBy GroupBy, pointNames map[string]string) ([]Row, error)
	BrandSales(ctx context.Context, from, to time.Time) ([]Row, error)
	CategorySales(ctx context.Context, from, to time.Time) ([]Row, error)
	PointNames(ctx context.Context) (map[string]string, error)

	// Detail reports (detail.go).
	CategoryProductSales(ctx context.Context, categoryID string, from, to time.Time) ([]ProductSalesRow, error)
	BrandProductSales(ctx context.Context, brand string, from, to time.Time) ([]ProductSalesRow, error)
	ProductCodeSales(ctx context.Context, productID string, from, to time.Time, pointNames map[string]string) (ProductCodeSales, error)
	ProductByCode(ctx context.Context, code string) (*ProductRef, error)
	ProductStock(ctx context.Context, productID string) ([]StockCell, error)
}

// Cache defaults. A closed range (ending before now) still changes when an
// old order is cancelled or paid, so even it is only kept a few minutes; a
// range that reaches today gets new orders all the time and is kept for one.
const (
	DefaultCacheTTL        = 5 * time.Minute
	DefaultOpenCacheTTL    = time.Minute
	DefaultCacheMaxEntries = 256
	// DefaultCacheMaxConcurrentLoads caps distinct report queries running
	// at once (identical ones already share a flight): loads are detached
	// from their requests, so without a cap many distinct ranges could pile
	// up queries no request is waiting for any more.
	DefaultCacheMaxConcurrentLoads = 4

	// cacheLoadTimeout bounds a shared load: it runs detached from the
	// request that started it (see CachedRepo.load), so a client hanging up
	// doesn't fail the other requests waiting on the same result.
	cacheLoadTimeout = 30 * time.Second
)

// CacheConfig tunes CachedRepo; zero fields take the defaults above.
type CacheConfig struct {
	TTL        time.Duration // ranges that end before now
	OpenTTL    time.Duration // ranges that include now (today)
	MaxEntries int           // bound on cached results
	// MaxConcurrentLoads caps distinct queries running at once.
	MaxConcurrentLoads int
	Now                func() time.Time // clock, injectable for tests
}

func (c CacheConfig) withDefaults() CacheConfig {
	if c.TTL <= 0 {
		c.TTL = DefaultCacheTTL
	}
	if c.OpenTTL <= 0 {
		c.OpenTTL = DefaultOpenCacheTTL
	}
	if c.MaxEntries <= 0 {
		c.MaxEntries = DefaultCacheMaxEntries
	}
	if c.MaxConcurrentLoads <= 0 {
		c.MaxConcurrentLoads = DefaultCacheMaxConcurrentLoads
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// CachedRepo is Source with an in-process TTL cache in front of the
// aggregate queries (Sales, BrandSales, CategorySales and the detail
// reports CategoryProductSales, BrandProductSales, ProductCodeSales): a year-long report
// costs Postgres a scan of every order row in the year, and 20 concurrent
// identical requests used to run it 20 times. Concurrent requests for the
// same key share one query (singleflight); distinct queries run at most
// MaxConcurrentLoads at a time; results are kept briefly (CacheConfig) and
// only when the query succeeded.
//
// The cache key is the query name plus every argument that reaches SQL
// or shapes the rows: from, to, groupBy, the pointNames map and a detail
// report's subject (Arg). That is
// everything the result depends on — the queries take no staff, point or
// language argument: the report is shop-wide and served only to owner and
// manager (point_staff gets 403 at the route), and rows carry raw keys
// that the handlers translate after the cache. A future point-scoped
// variant would take the point as an argument and so get its own key.
//
// PointNames passes through uncached: it reads a handful of rows, and a
// renamed point should show up at once.
type CachedRepo struct {
	src   Source
	cfg   CacheConfig
	group singleflight.Group
	// loadSlots is a semaphore of MaxConcurrentLoads slots.
	loadSlots chan struct{}

	mu      sync.Mutex
	entries map[string]cacheEntry
}

// cacheEntry holds one query's result: []Row, []ProductSalesRow or
// ProductCodeSales, by query (the typed wrappers below clone it out).
type cacheEntry struct {
	val     any
	expires time.Time
}

// NewCachedRepo wraps src with a result cache configured by cfg.
func NewCachedRepo(src Source, cfg CacheConfig) *CachedRepo {
	cfg = cfg.withDefaults()
	return &CachedRepo{
		src:       src,
		cfg:       cfg,
		loadSlots: make(chan struct{}, cfg.MaxConcurrentLoads),
		entries:   make(map[string]cacheEntry),
	}
}

// ErrQueryPanicked is returned (wrapped) when a report query panics: the
// panic is recovered inside the shared load — singleflight would re-panic
// it on a fresh goroutine, killing the process — and becomes an error.
var ErrQueryPanicked = errors.New("reports: report query panicked")

// errLoadAbandoned is a flight's result when its leader's context ended
// while it was still queued for a load slot: no query ran, so the waiters
// that joined the flight retry instead of failing.
var errLoadAbandoned = errors.New("reports: load abandoned before it started")

// cacheKey identifies one query result. Times are stored as UTC RFC 3339
// text: the same instant in another *time.Location shares the entry, and
// unlike UnixNano it is exact for any year the handlers accept (0000-9999).
type cacheKey struct {
	Query      string            `json:"q"`
	From       string            `json:"f"`
	To         string            `json:"t"`
	GroupBy    GroupBy           `json:"g,omitempty"`
	PointNames map[string]string `json:"p,omitempty"`
	// Arg is a detail report's subject: category id, brand or product id.
	Arg string `json:"a,omitempty"`
}

// String encodes the key unambiguously (JSON: quoted strings, map keys
// sorted), whatever the point names contain.
func (k cacheKey) String() (string, error) {
	b, err := json.Marshal(k)
	return string(b), err
}

func keyTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func (c *CachedRepo) Sales(ctx context.Context, from, to time.Time, groupBy GroupBy, pointNames map[string]string) ([]Row, error) {
	key := cacheKey{Query: "sales", From: keyTime(from), To: keyTime(to), GroupBy: groupBy, PointNames: pointNames}
	return loadSlice(c, ctx, key, to, func(ctx context.Context) ([]Row, error) {
		return c.src.Sales(ctx, from, to, groupBy, pointNames)
	})
}

func (c *CachedRepo) BrandSales(ctx context.Context, from, to time.Time) ([]Row, error) {
	key := cacheKey{Query: "brand", From: keyTime(from), To: keyTime(to)}
	return loadSlice(c, ctx, key, to, func(ctx context.Context) ([]Row, error) {
		return c.src.BrandSales(ctx, from, to)
	})
}

func (c *CachedRepo) CategorySales(ctx context.Context, from, to time.Time) ([]Row, error) {
	key := cacheKey{Query: "category", From: keyTime(from), To: keyTime(to)}
	return loadSlice(c, ctx, key, to, func(ctx context.Context) ([]Row, error) {
		return c.src.CategorySales(ctx, from, to)
	})
}

func (c *CachedRepo) CategoryProductSales(ctx context.Context, categoryID string, from, to time.Time) ([]ProductSalesRow, error) {
	key := cacheKey{Query: "category_products", From: keyTime(from), To: keyTime(to), Arg: categoryID}
	return loadSlice(c, ctx, key, to, func(ctx context.Context) ([]ProductSalesRow, error) {
		return c.src.CategoryProductSales(ctx, categoryID, from, to)
	})
}

func (c *CachedRepo) BrandProductSales(ctx context.Context, brand string, from, to time.Time) ([]ProductSalesRow, error) {
	key := cacheKey{Query: "brand_products", From: keyTime(from), To: keyTime(to), Arg: brand}
	return loadSlice(c, ctx, key, to, func(ctx context.Context) ([]ProductSalesRow, error) {
		return c.src.BrandProductSales(ctx, brand, from, to)
	})
}

func (c *CachedRepo) ProductCodeSales(ctx context.Context, productID string, from, to time.Time, pointNames map[string]string) (ProductCodeSales, error) {
	key := cacheKey{Query: "product_code", From: keyTime(from), To: keyTime(to), PointNames: pointNames, Arg: productID}
	v, err := c.load(ctx, key, to, func(ctx context.Context) (any, error) {
		return c.src.ProductCodeSales(ctx, productID, from, to, pointNames)
	})
	if err != nil {
		return ProductCodeSales{}, err
	}
	res, _ := v.(ProductCodeSales)
	return ProductCodeSales{Variants: slices.Clone(res.Variants), Points: slices.Clone(res.Points)}, nil
}

// ProductByCode and ProductStock pass through uncached: an index lookup
// and a product's handful of stock rows — and stock must be current.
func (c *CachedRepo) ProductByCode(ctx context.Context, code string) (*ProductRef, error) {
	return c.src.ProductByCode(ctx, code)
}

func (c *CachedRepo) ProductStock(ctx context.Context, productID string) ([]StockCell, error) {
	return c.src.ProductStock(ctx, productID)
}

// loadSlice is CachedRepo.load for a slice result: every caller gets its
// own copy.
func loadSlice[T any](c *CachedRepo, ctx context.Context, key cacheKey, to time.Time, query func(context.Context) ([]T, error)) ([]T, error) {
	v, err := c.load(ctx, key, to, func(ctx context.Context) (any, error) {
		return query(ctx)
	})
	if err != nil {
		return nil, err
	}
	rows, _ := v.([]T)
	return slices.Clone(rows), nil
}

func (c *CachedRepo) PointNames(ctx context.Context) (map[string]string, error) {
	return c.src.PointNames(ctx)
}

// Len reports the number of cached results (expired ones included until
// they are evicted).
func (c *CachedRepo) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// load returns the cached rows for key, or runs query once for all
// concurrent callers and caches a successful result. to is the range's
// exclusive end: a range ending after now includes today and gets OpenTTL.
// Callers copy the value out (loadSlice) before handing it on.
func (c *CachedRepo) load(ctx context.Context, key cacheKey, to time.Time, query func(context.Context) (any, error)) (any, error) {
	k, err := key.String()
	if err != nil {
		return nil, err
	}
	// Loops only when the flight this caller joined was abandoned by its
	// leader before any query ran; each pass either finds the result,
	// joins another flight or leads one itself, and ends with ctx.
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if val, ok := c.lookup(k); ok {
			return val, nil
		}

		ch := c.group.DoChan(k, func() (any, error) {
			return c.flight(ctx, k, to, query)
		})
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case res := <-ch:
			if errors.Is(res.Err, errLoadAbandoned) {
				continue
			}
			if res.Err != nil {
				return nil, res.Err
			}
			return res.Val, nil
		}
	}
}

// flight is the body of one shared load. It waits for a load slot while
// its leader (ctx) is still interested, then runs query detached from ctx
// — the result is shared with other waiters, who must not fail because
// the first requester went away — bounded by cacheLoadTimeout. A panic in
// query is recovered and returned as ErrQueryPanicked.
func (c *CachedRepo) flight(ctx context.Context, k string, to time.Time, query func(context.Context) (any, error)) (val any, err error) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("reports: report query panicked", "key", k, "panic", p, "stack", string(debug.Stack()))
			val, err = nil, fmt.Errorf("%w: %v", ErrQueryPanicked, p)
		}
	}()

	if cached, ok := c.lookup(k); ok { // a flight finished since the miss
		return cached, nil
	}
	select {
	case c.loadSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, errLoadAbandoned
	}
	defer func() { <-c.loadSlots }()

	loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cacheLoadTimeout)
	defer cancel()
	result, err := query(loadCtx)
	if err != nil {
		return nil, err
	}
	c.store(k, result, to)
	return result, nil
}

func (c *CachedRepo) lookup(k string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[k]
	if !ok || !c.cfg.Now().Before(e.expires) {
		return nil, false
	}
	return e.val, true
}

func (c *CachedRepo) store(k string, val any, to time.Time) {
	now := c.cfg.Now()
	ttl := c.cfg.TTL
	if to.After(now) {
		ttl = c.cfg.OpenTTL
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[k]; !exists {
		c.makeRoom(now)
	}
	c.entries[k] = cacheEntry{val: val, expires: now.Add(ttl)}
}

// makeRoom evicts until one more entry fits: expired entries first, then
// the one closest to expiry. O(MaxEntries), which is small. Caller holds mu.
func (c *CachedRepo) makeRoom(now time.Time) {
	if len(c.entries) < c.cfg.MaxEntries {
		return
	}
	for k, e := range c.entries {
		if !now.Before(e.expires) {
			delete(c.entries, k)
		}
	}
	for len(c.entries) >= c.cfg.MaxEntries {
		var oldestKey string
		var oldest time.Time
		for k, e := range c.entries {
			if oldestKey == "" || e.expires.Before(oldest) {
				oldestKey, oldest = k, e.expires
			}
		}
		delete(c.entries, oldestKey)
	}
}
