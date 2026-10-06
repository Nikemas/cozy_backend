package auth

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Nikemas/cozy_backend/internal/httpmw"
)

// reviewMaxFailures is how many wrong store-review codes (in total, across
// all OTP rows and client IPs, since process start) disable the review
// shortcut until the next restart. The review code is one fixed 4-digit
// value, so without this cap per-phone limits (~25 guesses/hour) would
// eventually brute-force it.
const reviewMaxFailures = 10

// reviewAlertTimeout bounds the background ops alert sent on lockout.
const reviewAlertTimeout = 10 * time.Second

// reviewGuard counts wrong review codes and trips a permanent (until
// restart) lockout. In-memory is enough: the backend runs as a single
// process, and a restart is the deliberate way to re-enable the account.
//
// After the lockout the review phone is treated as an ordinary phone:
// RequestOTP sends a real SMS through the provider and only the
// provider's random code is accepted, while any still-active review-marked
// row is rejected without comparing. This is the safer of the two options
// (vs. a hard error): no fixed secret stays in play, the normal per-phone
// and per-IP limits apply, and an attacker sees no distinct "locked"
// response that would confirm this is the review account.
type reviewGuard struct {
	mu       sync.Mutex
	failures int
	tripped  bool
}

// disabled reports whether the lockout has tripped.
func (g *reviewGuard) disabled() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.tripped
}

// check compares code with want and records a failure, atomically, so
// concurrent guesses can never exceed reviewMaxFailures comparisons. Once
// disabled it never compares. justTripped is true for exactly one call:
// the failure that reached the limit.
func (g *reviewGuard) check(code, want string) (ok, disabled, justTripped bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.tripped {
		return false, true, false
	}
	if subtle.ConstantTimeCompare([]byte(code), []byte(want)) == 1 {
		return true, false, false
	}
	g.failures++
	if g.failures >= reviewMaxFailures {
		g.tripped = true
		return false, true, true
	}
	return false, false, false
}

// reviewLockedOut logs the lockout and alerts ops once (called only for
// the failure that tripped the guard). The review code is never included.
func (s *Service) reviewLockedOut(ctx context.Context, phone string) {
	slog.ErrorContext(ctx, "auth: store-review login DISABLED until restart after too many wrong codes; the review phone now gets a real SMS",
		"phone", phone, "failures", reviewMaxFailures, "client_ip", httpmw.ClientIPFromContext(ctx))
	if s.opsAlerts == nil {
		return
	}
	text := fmt.Sprintf("⚠️ <b>Cozy: вход для ревью магазина отключён</b>\nНомер %s: %d неверных кодов. Возможен подбор кода. Сменить REVIEW_OTP_CODE и перезапустить сервер.",
		phone, reviewMaxFailures)
	go func() {
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reviewAlertTimeout)
		defer cancel()
		if err := s.opsAlerts.SendStaffMessage(actx, text); err != nil {
			slog.ErrorContext(actx, "auth: failed to send store-review lockout alert", "err", err)
		}
	}()
}
