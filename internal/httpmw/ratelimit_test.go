package httpmw

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Nikemas/cozy_backend/internal/reqid"
)

// fakeClock is a manually advanced clock for the limiter.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestLimiter(rps float64, burst, maxClients int) (*limiter, *fakeClock) {
	clock := &fakeClock{t: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	l := newLimiter(RateLimitConfig{RPS: rps, Burst: burst, MaxClients: maxClients})
	l.now = clock.now
	return l, clock
}

func TestLimiterAllowsBurstThenRejects(t *testing.T) {
	l, _ := newTestLimiter(1, 3, 100)
	for i := range 3 {
		if ok, _ := l.allow("a"); !ok {
			t.Fatalf("request %d within burst rejected", i+1)
		}
	}
	ok, retry := l.allow("a")
	if ok {
		t.Fatal("request beyond burst allowed")
	}
	if retry != time.Second {
		t.Errorf("retry after = %v, want 1s", retry)
	}
}

func TestLimiterRefillsOverTime(t *testing.T) {
	l, clock := newTestLimiter(2, 2, 100)
	l.allow("a")
	l.allow("a")
	if ok, _ := l.allow("a"); ok {
		t.Fatal("bucket should be empty")
	}
	clock.advance(500 * time.Millisecond) // 2 rps -> one token back
	if ok, _ := l.allow("a"); !ok {
		t.Fatal("token should have refilled after 500ms")
	}
	if ok, _ := l.allow("a"); ok {
		t.Fatal("only one token should have refilled")
	}
	clock.advance(time.Hour) // refill is capped at burst
	for i := range 2 {
		if ok, _ := l.allow("a"); !ok {
			t.Fatalf("request %d after long idle rejected", i+1)
		}
	}
	if ok, _ := l.allow("a"); ok {
		t.Fatal("refill must not exceed burst")
	}
}

func TestLimiterKeysAreIndependent(t *testing.T) {
	l, _ := newTestLimiter(1, 1, 100)
	if ok, _ := l.allow("a"); !ok {
		t.Fatal("a rejected")
	}
	if ok, _ := l.allow("b"); !ok {
		t.Fatal("b must not share a's bucket")
	}
}

func TestLimiterEvictsIdleClientsAndCapsMemory(t *testing.T) {
	l, clock := newTestLimiter(1, 2, 3)
	for _, k := range []string{"a", "b", "c"} {
		l.allow(k)
	}
	// Map is full and every entry is still refilling: a new client is
	// let through untracked (fail open) rather than growing the map.
	if ok, _ := l.allow("d"); !ok {
		t.Fatal("new client at capacity should be allowed (fail open)")
	}
	if got := l.size(); got != 3 {
		t.Fatalf("size = %d, want capped at 3", got)
	}

	// Once the buckets have refilled they carry no state worth keeping
	// and are swept to make room.
	clock.advance(time.Minute)
	l.allow("e")
	if got := l.size(); got != 1 {
		t.Errorf("size after sweep = %d, want 1 (only e)", got)
	}
}

func TestLimiterPeriodicSweepDropsIdleEntries(t *testing.T) {
	l, clock := newTestLimiter(10, 5, 1000)
	for i := range 50 {
		l.allow(fmt.Sprintf("ip-%d", i))
	}
	clock.advance(rateLimitSweepInterval + time.Second)
	l.allow("fresh")
	if got := l.size(); got != 1 {
		t.Errorf("size = %d, want idle entries swept", got)
	}
}

func TestRateLimitKey(t *testing.T) {
	cases := map[string]string{
		"198.51.100.7":           "198.51.100.7",
		"2001:db8:1:2:aaaa::1":   "2001:db8:1:2::/64",
		"2001:db8:1:2:bbbb::9":   "2001:db8:1:2::/64",
		"2001:db8:1:3::1":        "2001:db8:1:3::/64",
		"not-an-ip":              "not-an-ip",
		"198.51.100.7:4000":      "198.51.100.7",
		"[2001:db8:1:2::5]:4000": "2001:db8:1:2::/64",
	}
	for in, want := range cases {
		if got := rateLimitKey(in); got != want {
			t.Errorf("rateLimitKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func serveFrom(h http.Handler, method, target, remote string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = remote
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRateLimitJSONErrorForAPI(t *testing.T) {
	h := Chain(okHandler(), RequestID, RateLimit(RateLimitConfig{RPS: 1, Burst: 2}, nil))
	for i := range 2 {
		if rec := serveFrom(h, http.MethodGet, "/api/v1/products", "203.0.113.5:1", nil); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d", i+1, rec.Code)
		}
	}
	rec := serveFrom(h, http.MethodGet, "/api/v1/products", "203.0.113.5:1", nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want 1", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var body struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "rate_limited" || body.Message == "" || body.RequestID != rec.Header().Get(reqid.Header) {
		t.Errorf("body = %+v", body)
	}
}

func TestRateLimitJSONErrorIsLocalized(t *testing.T) {
	h := RateLimit(RateLimitConfig{RPS: 1, Burst: 1}, nil)(okHandler())
	serveFrom(h, http.MethodGet, "/api/v1/products", "203.0.113.5:1", nil)
	rec := serveFrom(h, http.MethodGet, "/api/v1/products?lang=ky", "203.0.113.5:1", nil)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Content-Language") != "ky" {
		t.Errorf("status %d, Content-Language %q, want 429 ky", rec.Code, rec.Header().Get("Content-Language"))
	}
}

func TestRateLimitHTMLPageForSite(t *testing.T) {
	h := RateLimit(RateLimitConfig{RPS: 0.5, Burst: 1}, nil)(okHandler())
	serveFrom(h, http.MethodGet, "/catalog", "203.0.113.5:1", nil)
	rec := serveFrom(h, http.MethodGet, "/catalog", "203.0.113.5:1", map[string]string{"Accept-Language": "ky"})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Errorf("Retry-After = %q, want 2 (0.5 rps)", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<!doctype html>") || !strings.Contains(body, `lang="ky"`) || !strings.Contains(body, "429") {
		t.Errorf("body = %s", body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", rec.Header().Get("Cache-Control"))
	}
}

func TestRateLimitExemptRequestsBypass(t *testing.T) {
	exempt := func(r *http.Request) bool { return r.URL.Path == "/healthz" }
	h := RateLimit(RateLimitConfig{RPS: 1, Burst: 1}, exempt)(okHandler())
	for i := range 5 {
		if rec := serveFrom(h, http.MethodGet, "/healthz", "203.0.113.5:1", nil); rec.Code != http.StatusOK {
			t.Fatalf("exempt request %d: status %d", i+1, rec.Code)
		}
	}
	// Exempt requests don't consume the client's tokens.
	if rec := serveFrom(h, http.MethodGet, "/", "203.0.113.5:1", nil); rec.Code != http.StatusOK {
		t.Errorf("first non-exempt request: status %d", rec.Code)
	}
}

func TestRateLimitDisabledWhenRPSZero(t *testing.T) {
	h := RateLimit(RateLimitConfig{RPS: 0, Burst: 0}, nil)(okHandler())
	for i := range 100 {
		if rec := serveFrom(h, http.MethodGet, "/", "203.0.113.5:1", nil); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d", i+1, rec.Code)
		}
	}
}

// With the real ClientIP middleware in front, a client talking to the
// backend directly cannot dodge the limit by rotating X-Forwarded-For,
// while distinct clients behind the trusted proxy get separate buckets.
func TestRateLimitUsesTrustedClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12")}
	h := Chain(okHandler(), ClientIP(trusted), RateLimit(RateLimitConfig{RPS: 1, Burst: 1}, nil))

	serveFrom(h, http.MethodGet, "/", "203.0.113.5:1", map[string]string{"X-Forwarded-For": "1.1.1.1"})
	rec := serveFrom(h, http.MethodGet, "/", "203.0.113.5:1", map[string]string{"X-Forwarded-For": "2.2.2.2"})
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("spoofed XFF from untrusted peer: status %d, want 429", rec.Code)
	}

	for _, client := range []string{"198.51.100.1", "198.51.100.2"} {
		rec := serveFrom(h, http.MethodGet, "/", "172.18.0.3:5555", map[string]string{"X-Forwarded-For": client})
		if rec.Code != http.StatusOK {
			t.Errorf("client %s behind proxy: status %d, want 200", client, rec.Code)
		}
	}
	rec = serveFrom(h, http.MethodGet, "/", "172.18.0.3:5555", map[string]string{"X-Forwarded-For": "198.51.100.1"})
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("repeat client behind proxy: status %d, want 429", rec.Code)
	}
}
