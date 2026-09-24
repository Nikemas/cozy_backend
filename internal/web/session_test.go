package web

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestSessionCookieSecureFlag(t *testing.T) {
	defer SetCookieSecure(true)

	for _, secure := range []bool{true, false} {
		SetCookieSecure(secure)

		rec := httptest.NewRecorder()
		setSessionCookie(rec, "tok", time.Minute)
		clearSessionCookie(rec)
		cookies := rec.Result().Cookies()
		if len(cookies) != 2 {
			t.Fatalf("got %d cookies", len(cookies))
		}
		for _, c := range cookies {
			if c.Secure != secure || !c.HttpOnly {
				t.Errorf("secure=%v: cookie %+v", secure, c)
			}
		}
	}
}
