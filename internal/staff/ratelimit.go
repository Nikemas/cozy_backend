package staff

import (
	"sync"
	"time"
)

// loginAttemptLimit/loginAttemptWindow throttle staff login attempts per
// phone number: after this many attempts within the window, further ones
// are rejected until it rolls over. The admin panel runs as a single
// process, so in-memory state is enough — no external store needed.
const (
	loginAttemptLimit  = 5
	loginAttemptWindow = 15 * time.Minute

	// loginAttemptSweepThreshold bounds how large attempts can grow from an
	// attacker cycling through many phone numbers: once it's this big, the
	// next new key triggers a sweep of expired entries first.
	loginAttemptSweepThreshold = 1000
)

// loginWindow tracks one key's attempt count within the current window.
type loginWindow struct {
	count   int
	resetAt time.Time
}

// loginRateLimiter throttles login attempts per key (phone number, or
// client IP). Its zero value is ready to use with loginAttemptLimit, so it
// can be embedded in Service without NewService needing to initialize it;
// limit overrides that budget when > 0.
type loginRateLimiter struct {
	limit int

	mu       sync.Mutex
	attempts map[string]*loginWindow
}

func (l *loginRateLimiter) max() int {
	if l.limit > 0 {
		return l.limit
	}
	return loginAttemptLimit
}

// allow reports whether key still has attempts left in its current window
// and, if so, counts this call toward that budget. Called once per login
// attempt before the credential check, so an exhausted budget skips the
// bcrypt compare too.
func (l *loginRateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.attempts == nil {
		l.attempts = make(map[string]*loginWindow)
	}

	now := time.Now()
	w, ok := l.attempts[key]
	if !ok || now.After(w.resetAt) {
		if len(l.attempts) >= loginAttemptSweepThreshold {
			l.sweepLocked(now)
		}
		w = &loginWindow{resetAt: now.Add(loginAttemptWindow)}
		l.attempts[key] = w
	}

	if w.count >= l.max() {
		return false
	}
	w.count++
	return true
}

// reset clears key's attempt count. Called after a successful login so a
// staff member who mistyped their password a few times isn't penalized
// afterwards.
func (l *loginRateLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}

// sweepLocked removes expired windows. Callers must hold l.mu.
func (l *loginRateLimiter) sweepLocked(now time.Time) {
	for k, w := range l.attempts {
		if now.After(w.resetAt) {
			delete(l.attempts, k)
		}
	}
}
