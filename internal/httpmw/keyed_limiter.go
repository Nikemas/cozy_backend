package httpmw

import "time"

// KeyedLimiter is RateLimit's token-bucket limiter for handlers that
// limit by their own key — a staff member's ID, say — rather than by
// client IP. It shares RateLimit's bounded, self-sweeping bucket map.
// Safe for concurrent use.
type KeyedLimiter struct {
	l *limiter // nil: limiting disabled
}

// NewKeyedLimiter returns a limiter giving each key a bucket of cfg.Burst
// refilled at cfg.RPS. RPS <= 0 disables it (Allow always succeeds).
func NewKeyedLimiter(cfg RateLimitConfig) *KeyedLimiter {
	if cfg.RPS <= 0 {
		return &KeyedLimiter{}
	}
	return &KeyedLimiter{l: newLimiter(cfg)}
}

// Allow spends one of key's tokens. When none is left it returns false
// and how long until the next one.
func (k *KeyedLimiter) Allow(key string) (bool, time.Duration) {
	if k == nil || k.l == nil {
		return true, 0
	}
	return k.l.allow(key)
}
