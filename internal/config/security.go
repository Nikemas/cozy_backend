package config

import (
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

// defaultTrustedProxies covers loopback and the private ranges Docker
// networks are allocated from: in the compose deployment Caddy is the only
// thing that can reach the backend, and it connects from one of these.
const defaultTrustedProxies = "127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7"

// Security groups the request-hardening settings.
type Security struct {
	// CookieSecure sets the Secure attribute on the storefront session
	// cookie. COOKIE_SECURE, default true everywhere except APP_ENV=dev
	// (local dev is plain http://localhost).
	CookieSecure bool

	// TrustedProxies are the peers whose X-Forwarded-For header is
	// believed when resolving the client IP (rate limits, access log).
	// TRUSTED_PROXY_CIDRS, comma-separated CIDRs or bare IPs; empty
	// string disables XFF entirely (RemoteAddr only).
	TrustedProxies []netip.Prefix

	// MaxBodyBytes caps every non-multipart request body (JSON, forms).
	// MAX_BODY_BYTES, default 1 MiB.
	MaxBodyBytes int64
	// MaxUploadBytes caps multipart bodies; upload handlers apply their
	// own tighter per-file limits on top. MAX_UPLOAD_BYTES, default 25 MiB.
	MaxUploadBytes int64

	Auth AuthLimits
}

// AuthLimits are the abuse limits of the customer OTP login and staff
// login. A zero per-IP/global limit disables that particular check.
type AuthLimits struct {
	// OTPPerIPPerHour — OTP SMS requests per client IP per rolling hour
	// (OTP_MAX_PER_IP_PER_HOUR, default 30; mobile carriers NAT many
	// customers behind one IP, so this is deliberately generous).
	OTPPerIPPerHour int
	// OTPPerDay — OTP SMS requests across ALL phones per rolling 24h
	// (OTP_MAX_PER_DAY, default 1000): a hard ceiling on SMS spend.
	OTPPerDay int
	// OTPVerifyMaxAttempts — verification attempts per sent code before
	// it is burned (OTP_VERIFY_MAX_ATTEMPTS, default 5, min 1).
	OTPVerifyMaxAttempts int
	// OTPVerifyFailsPerIPPerHour — wrong codes per client IP per hour
	// (OTP_VERIFY_MAX_FAILS_PER_IP_PER_HOUR, default 30).
	OTPVerifyFailsPerIPPerHour int
	// RefreshPerIPPerMinute — POST /api/v1/auth/refresh calls per client
	// IP per minute (AUTH_REFRESH_MAX_PER_IP_PER_MINUTE, default 120).
	RefreshPerIPPerMinute int
	// StaffLoginPerIP — admin login attempts per client IP per 15 minutes,
	// on top of the per-phone limit (STAFF_LOGIN_MAX_PER_IP, default 20).
	StaffLoginPerIP int
}

func loadSecurity(env string) (Security, error) {
	s := Security{CookieSecure: env != EnvDev}
	if v := strings.TrimSpace(os.Getenv("COOKIE_SECURE")); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return s, fmt.Errorf("COOKIE_SECURE must be true or false, got %q", v)
		}
		s.CookieSecure = b
	}

	proxies, ok := os.LookupEnv("TRUSTED_PROXY_CIDRS")
	if !ok {
		proxies = defaultTrustedProxies
	}
	var err error
	if s.TrustedProxies, err = parsePrefixes(proxies); err != nil {
		return s, err
	}

	maxBody, err := getEnvInt("MAX_BODY_BYTES", 1<<20)
	if err != nil {
		return s, err
	}
	maxUpload, err := getEnvInt("MAX_UPLOAD_BYTES", 25<<20)
	if err != nil {
		return s, err
	}
	if maxBody == 0 || maxUpload == 0 {
		return s, fmt.Errorf("MAX_BODY_BYTES and MAX_UPLOAD_BYTES must be positive")
	}
	s.MaxBodyBytes, s.MaxUploadBytes = int64(maxBody), int64(maxUpload)

	a := &s.Auth
	for _, f := range []struct {
		key      string
		def      int
		dst      *int
		positive bool
	}{
		{"OTP_MAX_PER_IP_PER_HOUR", 30, &a.OTPPerIPPerHour, false},
		{"OTP_MAX_PER_DAY", 1000, &a.OTPPerDay, false},
		{"OTP_VERIFY_MAX_ATTEMPTS", 5, &a.OTPVerifyMaxAttempts, true},
		{"OTP_VERIFY_MAX_FAILS_PER_IP_PER_HOUR", 30, &a.OTPVerifyFailsPerIPPerHour, false},
		{"AUTH_REFRESH_MAX_PER_IP_PER_MINUTE", 120, &a.RefreshPerIPPerMinute, false},
		{"STAFF_LOGIN_MAX_PER_IP", 20, &a.StaffLoginPerIP, false},
	} {
		n, err := getEnvInt(f.key, f.def)
		if err != nil {
			return s, err
		}
		if f.positive && n == 0 {
			return s, fmt.Errorf("%s must be at least 1", f.key)
		}
		*f.dst = n
	}
	return s, nil
}

// parsePrefixes parses a comma-separated list of CIDRs or bare IPs.
func parsePrefixes(list string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, item := range strings.Split(list, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if strings.Contains(item, "/") {
			p, err := netip.ParsePrefix(item)
			if err != nil {
				return nil, fmt.Errorf("TRUSTED_PROXY_CIDRS: invalid CIDR %q", item)
			}
			out = append(out, p.Masked())
			continue
		}
		addr, err := netip.ParseAddr(item)
		if err != nil {
			return nil, fmt.Errorf("TRUSTED_PROXY_CIDRS: invalid IP %q", item)
		}
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out, nil
}
