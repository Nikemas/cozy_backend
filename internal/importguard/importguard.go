// Package importguard bounds bulk product imports: a per-staff rate
// limit and a process-wide cap on concurrent imports. cmd/server builds
// one Guard and hands it to both import surfaces — the admin HTML page
// (internal/admin) and the JSON endpoint (internal/httpapi) — so a staff
// member can't dodge the limit by switching endpoints, and the total
// number of uploads parsed at once stays at MaxConcurrent across both.
package importguard

import (
	"context"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/Nikemas/cozy_backend/internal/httpmw"
)

// Per-staff limit on import requests (one shared bucket for check, apply
// and the JSON endpoint): a burst of RateBurst, refilled at
// RatePerMinute.
const (
	RateBurst        = 10
	RatePerMinute    = 10
	secondsPerMinute = 60
)

// At most MaxConcurrent imports parse and run an upload at once per
// process (each can hold a 20 MB file plus the parsed sheet in memory); a
// request waits up to SlotWait for a slot and is then refused, told to
// retry after BusyRetryAfter.
const (
	MaxConcurrent  = 2
	SlotWait       = 2 * time.Second
	BusyRetryAfter = 5 * time.Second
)

// Limiter limits import requests per staff member;
// *httpmw.KeyedLimiter implements it.
type Limiter interface {
	Allow(key string) (ok bool, retryAfter time.Duration)
}

// Gate is a counting semaphore bounding concurrent imports. A nil *Gate
// doesn't limit. Safe for concurrent use.
type Gate struct {
	slots chan struct{}
	wait  time.Duration
}

// NewGate returns a gate of size slots; Acquire waits up to wait.
func NewGate(size int, wait time.Duration) *Gate {
	return &Gate{slots: make(chan struct{}, size), wait: wait}
}

// Acquire takes a slot, waiting up to the gate's wait or until ctx ends.
// On success it returns an idempotent release func the caller must defer.
func (g *Gate) Acquire(ctx context.Context) (release func(), ok bool) {
	if g == nil {
		return func() {}, true
	}
	timer := time.NewTimer(g.wait)
	defer timer.Stop()
	select {
	case g.slots <- struct{}{}:
	case <-timer.C:
		return nil, false
	case <-ctx.Done():
		return nil, false
	}
	var once sync.Once
	return func() { once.Do(func() { <-g.slots }) }, true
}

// Cap is the gate's number of slots (0 for a nil gate).
func (g *Gate) Cap() int {
	if g == nil {
		return 0
	}
	return cap(g.slots)
}

// Held is how many slots are taken right now (0 for a nil gate).
func (g *Gate) Held() int {
	if g == nil {
		return 0
	}
	return len(g.slots)
}

// Guard is the pair every import surface checks before reading an
// upload. A nil Guard, or a nil part, doesn't limit.
type Guard struct {
	Limiter Limiter
	Gate    *Gate
}

// New returns the production Guard: RateBurst / RatePerMinute per staff
// member (the same token-bucket limiter the global per-IP middleware
// uses) and MaxConcurrent slots waited on for SlotWait.
func New() *Guard {
	return &Guard{
		Limiter: httpmw.NewKeyedLimiter(httpmw.RateLimitConfig{
			RPS: float64(RatePerMinute) / secondsPerMinute, Burst: RateBurst,
		}),
		Gate: NewGate(MaxConcurrent, SlotWait),
	}
}

// Outcome is Begin's verdict.
type Outcome int

// Begin's possible outcomes.
const (
	Allowed     Outcome = iota // go ahead; call Decision.Release when done
	RateLimited                // the staff member is out of tokens
	Busy                       // no import slot freed up in time
)

func (o Outcome) String() string {
	switch o {
	case Allowed:
		return "allowed"
	case RateLimited:
		return "rate_limited"
	case Busy:
		return "busy"
	}
	return "unknown"
}

// Decision is Begin's result. Release is always safe to call (a no-op on
// a refusal).
type Decision struct {
	Outcome    Outcome
	RetryAfter time.Duration // for a refusal: when to try again
	Release    func()
}

func noop() {}

// Begin spends one of staffID's tokens, then takes an import slot —
// rate limit first, so a limited request never holds a slot. Callers
// must refuse a request without a staff ID themselves (403) before
// calling: an enabled limiter refuses an empty key anyway, but as
// "rate limited", which would be misleading.
func (g *Guard) Begin(ctx context.Context, staffID string) Decision {
	if g == nil {
		return Decision{Outcome: Allowed, Release: noop}
	}
	if g.Limiter != nil {
		if ok, retry := g.Limiter.Allow(staffID); !ok {
			return Decision{Outcome: RateLimited, RetryAfter: retry, Release: noop}
		}
	}
	release, ok := g.Gate.Acquire(ctx)
	if !ok {
		return Decision{Outcome: Busy, RetryAfter: BusyRetryAfter, Release: noop}
	}
	return Decision{Outcome: Allowed, Release: release}
}

// RetryAfterSeconds formats d for a Retry-After header: whole seconds,
// rounded up, at least 1.
func RetryAfterSeconds(d time.Duration) string {
	return strconv.Itoa(max(1, int(math.Ceil(d.Seconds()))))
}
