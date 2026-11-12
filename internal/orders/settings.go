package orders

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Settings are the order-lifecycle knobs read from the environment. They
// live here rather than in internal/config so every place that constructs
// an orders.Service (web, admin, httpapi, payments) sees the same values
// through one process default — the same pattern as SetDefaultNotifier.
type Settings struct {
	// DeliveryFee is the flat delivery charge (som) added to the total of
	// every delivery order (not self-pickup). DELIVERY_FEE_SOM, default 200.
	DeliveryFee float64
	// MaxOpenOrders caps a customer's non-final orders (placed/confirmed/
	// courier_assigned); 0 disables the cap. MAX_OPEN_ORDERS_PER_CUSTOMER,
	// default 5.
	MaxOpenOrders int
	// PaymentPendingTTL is how long an online payment may stay pending
	// before the expiry job cancels its order and returns the stock.
	// PAYMENT_PENDING_TTL, default 30m.
	PaymentPendingTTL time.Duration
	// PickupDisabled hides self-pickup at a point of sale: no checkout
	// option, no pickup texts on the site, and PlaceOrder rejects a pickup
	// point (pickup_disabled). The contract has no self-pickup, so the
	// environment default is disabled; PICKUP_ENABLED=true turns it back on.
	// The zero value (and DefaultSettings) keeps pickup available, so code
	// that never reads the environment (tests, tools) behaves as before.
	PickupDisabled bool
}

// Defaults for Settings.
const (
	DefaultDeliveryFee       = 200
	DefaultMaxOpenOrders     = 5
	DefaultPaymentPendingTTL = 30 * time.Minute

	// MaxCartQty caps one cart line / one order line. Shoes are bought by
	// the pair; anything above this is a typo or abuse, not a real order.
	MaxCartQty = 20
	// MaxCommentLen caps orders.comment (runes).
	MaxCommentLen = 500
	// IdempotencyWindow is how long an Idempotency-Key is honoured.
	IdempotencyWindow = 24 * time.Hour
	// MaxIdempotencyKeyLen mirrors orders_idempotency_key_len_chk.
	MaxIdempotencyKeyLen = 64
)

// DefaultSettings returns the built-in defaults.
func DefaultSettings() Settings {
	return Settings{
		DeliveryFee:       DefaultDeliveryFee,
		MaxOpenOrders:     DefaultMaxOpenOrders,
		PaymentPendingTTL: DefaultPaymentPendingTTL,
	}
}

// SettingsFromEnv reads Settings from the environment, falling back to
// DefaultSettings for unset variables. A malformed value is an error (the
// server refuses to start) rather than a silent default: a typo in the
// delivery fee would otherwise mis-charge every order.
func SettingsFromEnv() (Settings, error) {
	return settingsFromLookup(os.LookupEnv)
}

func settingsFromLookup(lookup func(string) (string, bool)) (Settings, error) {
	s := DefaultSettings()
	if v, ok := lookup("DELIVERY_FEE_SOM"); ok && strings.TrimSpace(v) != "" {
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || f < 0 || math.IsNaN(f) || math.IsInf(f, 0) || f > 100000 {
			return s, fmt.Errorf("DELIVERY_FEE_SOM must be a non-negative number of som, got %q", v)
		}
		s.DeliveryFee = math.Round(f*100) / 100
	}
	if v, ok := lookup("MAX_OPEN_ORDERS_PER_CUSTOMER"); ok && strings.TrimSpace(v) != "" {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 0 {
			return s, fmt.Errorf("MAX_OPEN_ORDERS_PER_CUSTOMER must be a non-negative integer (0 = unlimited), got %q", v)
		}
		s.MaxOpenOrders = n
	}
	if v, ok := lookup("PAYMENT_PENDING_TTL"); ok && strings.TrimSpace(v) != "" {
		d, err := time.ParseDuration(strings.TrimSpace(v))
		if err != nil || d < time.Minute {
			return s, fmt.Errorf("PAYMENT_PENDING_TTL must be a duration of at least 1m (e.g. 30m), got %q", v)
		}
		s.PaymentPendingTTL = d
	}
	pickup, err := parsePickupEnabled(lookup)
	if err != nil {
		return s, err
	}
	s.PickupDisabled = !pickup
	return s, nil
}

// parsePickupEnabled reads PICKUP_ENABLED; unset or empty means off.
func parsePickupEnabled(lookup func(string) (string, bool)) (bool, error) {
	v, ok := lookup("PICKUP_ENABLED")
	if !ok || strings.TrimSpace(v) == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return false, fmt.Errorf("PICKUP_ENABLED must be true or false, got %q", v)
	}
	return b, nil
}

var defaultSettings atomic.Value // Settings

// SetDefaultSettings installs the process-wide Settings. Called once from
// cmd/server/main.go before routes are registered.
func SetDefaultSettings(s Settings) {
	defaultSettings.Store(s)
}

// CurrentSettings returns the process-wide Settings (DefaultSettings until
// SetDefaultSettings is called).
func CurrentSettings() Settings {
	if s, ok := defaultSettings.Load().(Settings); ok {
		return s
	}
	return DefaultSettings()
}

// WithSettings overrides the process default for one Service (tests).
func (s *Service) WithSettings(st Settings) *Service {
	s.settings = &st
	return s
}

func (s *Service) currentSettings() Settings {
	if s.settings != nil {
		return *s.settings
	}
	return CurrentSettings()
}

// PickupEnabled reports whether customers may choose self-pickup.
func (st Settings) PickupEnabled() bool { return !st.PickupDisabled }

// DeliveryFeeFor is the delivery charge for an order with (or without) a
// delivery address.
func (st Settings) DeliveryFeeFor(isDelivery bool) float64 {
	if !isDelivery {
		return 0
	}
	return st.DeliveryFee
}
