package auth

import (
	"sync"
	"time"
)

// windowLimiter is an in-memory fixed-window counter per key (client IP).
// The backend runs as a single process, so process memory is enough for
// these secondary, per-IP limits; the per-phone OTP limits that guard SMS
// spend live in Postgres (see otpRepo.reserve). limit <= 0 disables it.
type windowLimiter struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	windows map[string]*limiterWindow
}

type limiterWindow struct {
	count   int
	resetAt time.Time
}

// limiterSweepThreshold bounds memory under an attacker cycling keys.
const limiterSweepThreshold = 10000

func newWindowLimiter(limit int, window time.Duration) *windowLimiter {
	return &windowLimiter{limit: limit, window: window, now: time.Now, windows: map[string]*limiterWindow{}}
}

func (l *windowLimiter) current(key string) *limiterWindow {
	now := l.now()
	w, ok := l.windows[key]
	if !ok || !now.Before(w.resetAt) {
		if len(l.windows) >= limiterSweepThreshold {
			for k, old := range l.windows {
				if !now.Before(old.resetAt) {
					delete(l.windows, k)
				}
			}
		}
		w = &limiterWindow{resetAt: now.Add(l.window)}
		l.windows[key] = w
	}
	return w
}

// allow counts one event for key and reports whether it is within the
// limit.
func (l *windowLimiter) allow(key string) bool {
	if l == nil || l.limit <= 0 || key == "" {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.current(key)
	if w.count >= l.limit {
		return false
	}
	w.count++
	return true
}

// blocked reports whether key has used up its budget, without counting.
func (l *windowLimiter) blocked(key string) bool {
	if l == nil || l.limit <= 0 || key == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.current(key).count >= l.limit
}

// hit counts one event for key (e.g. a failed attempt).
func (l *windowLimiter) hit(key string) {
	if l == nil || l.limit <= 0 || key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.current(key).count++
}
