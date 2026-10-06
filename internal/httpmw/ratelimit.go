package httpmw

import (
	"html"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

const (
	// DefaultRateLimitMaxClients caps the number of client buckets kept
	// in memory (~100 bytes each). Beyond it, after sweeping idle ones,
	// new clients are served untracked rather than growing the map.
	DefaultRateLimitMaxClients = 100_000

	// rateLimitSweepInterval is how often idle buckets are dropped during
	// normal traffic. The sweep runs inline (no goroutine to stop) and is
	// O(clients), so it is also throttled to once per
	// rateLimitMinSweepGap when the map is full.
	rateLimitSweepInterval = time.Minute
	rateLimitMinSweepGap   = time.Second

	// ipv6LimitPrefix groups IPv6 clients by /64: one subscriber usually
	// gets a whole /64, so per-address buckets would let a single host
	// rotate addresses to dodge the limit.
	ipv6LimitPrefix = 64
)

// RateLimitConfig configures RateLimit. RPS <= 0 disables it.
type RateLimitConfig struct {
	RPS   float64 // sustained requests per second per client
	Burst int     // bucket size: requests allowed at once
	// MaxClients caps tracked clients; 0 means DefaultRateLimitMaxClients.
	MaxClients int
}

// errRateLimited is the 429 every limited request gets. Message is the
// Russian source; apperr localizes it (err.rate_limited).
var errRateLimited = apperr.TooManyRequests("rate_limited", "слишком много запросов, попробуйте позже")

// RateLimit limits each client IP (as resolved by the ClientIP middleware,
// so X-Forwarded-For is only believed from trusted proxies) to a token
// bucket of cfg.Burst refilled at cfg.RPS. Requests for which exempt
// returns true are passed through without spending a token. Limited
// requests get 429 with Retry-After: the standard JSON error envelope on
// API paths, a tiny self-contained HTML page elsewhere (deliberately not
// the branded storefront page, so answering a flood stays cheap).
func RateLimit(cfg RateLimitConfig, exempt func(*http.Request) bool) func(http.Handler) http.Handler {
	if cfg.RPS <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	l := newLimiter(cfg)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if exempt != nil && exempt(r) {
				next.ServeHTTP(w, r)
				return
			}
			ok, retry := l.allow(rateLimitKey(clientIP(r)))
			if ok {
				next.ServeHTTP(w, r)
				return
			}
			writeRateLimited(w, r, retry)
		})
	}
}

func writeRateLimited(w http.ResponseWriter, r *http.Request, retry time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retry.Seconds()))))
	w.Header().Set("Cache-Control", "no-store")
	if apperr.IsAPIPath(r.URL.Path) {
		apperr.WriteError(w, r, errRateLimited)
		return
	}
	lang := apperr.LangFromRequest(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte(`<!doctype html><html lang="` + html.EscapeString(lang) + `"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width, initial-scale=1"><meta name="robots" content="noindex">` +
		`<title>429</title></head><body style="font-family:system-ui,sans-serif;padding:40px">` +
		`<h1>429</h1><p>` + html.EscapeString(apperr.Localize(lang, errRateLimited)) + `</p>` +
		`<p><a href="/">Cozy</a></p></body></html>`))
}

// rateLimitKey turns a client address into its bucket key: the IPv4
// address itself, or the /64 network of an IPv6 address. Anything
// unparsable is used verbatim.
func rateLimitKey(ip string) string {
	addr, ok := parseAddr(ip)
	if !ok {
		return ip
	}
	if addr.Is4() {
		return addr.String()
	}
	return netip.PrefixFrom(addr, ipv6LimitPrefix).Masked().String()
}

// bucket is one client's token bucket. tokens is the balance at last.
type bucket struct {
	tokens float64
	last   time.Time
}

// limiter is a map of token buckets with bounded size. A bucket that has
// refilled to capacity is indistinguishable from a fresh one, so dropping
// it loses nothing — that is what the sweep removes.
type limiter struct {
	rps        float64
	burst      float64
	maxClients int
	fullAfter  time.Duration // idle time after which a bucket is full again
	now        func() time.Time

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

func newLimiter(cfg RateLimitConfig) *limiter {
	maxClients := cfg.MaxClients
	if maxClients <= 0 {
		maxClients = DefaultRateLimitMaxClients
	}
	burst := float64(max(cfg.Burst, 1))
	return &limiter{
		rps:        cfg.RPS,
		burst:      burst,
		maxClients: maxClients,
		fullAfter:  time.Duration(burst / cfg.RPS * float64(time.Second)),
		now:        time.Now,
		buckets:    make(map[string]*bucket),
	}
}

// allow spends one token from key's bucket. When none is left it returns
// false and how long until the next token.
func (l *limiter) allow(key string) (bool, time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastSweep) >= rateLimitSweepInterval {
		l.sweep(now)
	}

	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= l.maxClients && now.Sub(l.lastSweep) >= rateLimitMinSweepGap {
			l.sweep(now)
		}
		if len(l.buckets) >= l.maxClients {
			// Still full of active clients: fail open for this one
			// rather than grow without bound or lock out newcomers.
			return true, 0
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}

	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rps)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / l.rps * float64(time.Second))
	return false, wait
}

// sweep drops every bucket that has been idle long enough to be full.
// Caller holds mu.
func (l *limiter) sweep(now time.Time) {
	for k, b := range l.buckets {
		if now.Sub(b.last) >= l.fullAfter {
			delete(l.buckets, k)
		}
	}
	l.lastSweep = now
}

func (l *limiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
