package httpmw

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIPResolution(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("127.0.0.1/32")}

	cases := []struct {
		name   string
		remote string
		xff    []string
		want   string
	}{
		{"direct client, no header", "203.0.113.5:4000", nil, "203.0.113.5"},
		{"untrusted peer cannot spoof XFF", "203.0.113.5:4000", []string{"1.2.3.4"}, "203.0.113.5"},
		{"trusted proxy, single hop", "172.18.0.3:5555", []string{"198.51.100.7"}, "198.51.100.7"},
		{"client-forged left entries are ignored", "172.18.0.3:5555", []string{"1.2.3.4, 198.51.100.7"}, "198.51.100.7"},
		{"skip trusted hops from the right", "172.18.0.3:5555", []string{"1.2.3.4, 198.51.100.7, 172.20.0.9"}, "198.51.100.7"},
		{"multiple header lines", "172.18.0.3:5555", []string{"1.2.3.4", "198.51.100.7"}, "198.51.100.7"},
		{"trusted proxy without header", "172.18.0.3:5555", nil, "172.18.0.3"},
		{"garbage hop stops the walk", "172.18.0.3:5555", []string{"198.51.100.7, not-an-ip"}, "172.18.0.3"},
		{"ipv6 peer", "[2001:db8::1]:443", nil, "2001:db8::1"},
		{"ipv4-mapped ipv6 xff", "127.0.0.1:1", []string{"::ffff:198.51.100.7"}, "198.51.100.7"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got string
			h := ClientIP(trusted)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = ClientIPFromContext(r.Context())
			}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = c.remote
			for _, v := range c.xff {
				req.Header.Add("X-Forwarded-For", v)
			}
			h.ServeHTTP(httptest.NewRecorder(), req)
			if got != c.want {
				t.Errorf("client IP = %q, want %q", got, c.want)
			}
		})
	}
}

func TestClientIPNoTrustedProxiesIgnoresXFF(t *testing.T) {
	var got string
	h := ClientIP(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = ClientIPFromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("X-Forwarded-For", "198.51.100.7")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got != "127.0.0.1" {
		t.Errorf("client IP = %q, want RemoteAddr", got)
	}
}
