package config

import (
	"strings"
	"testing"
)

func TestLoadReviewLogin(t *testing.T) {
	tests := []struct {
		name, phone, code string
		wantPhone         string
		wantErr           string // substring; "" = ok
	}{
		{name: "both unset = disabled"},
		{name: "local format normalized", phone: "0700999111", code: "7391", wantPhone: "+996700999111"},
		{name: "e164 accepted", phone: "+996 700 999 111", code: "7391", wantPhone: "+996700999111"},
		{name: "phone only", phone: "0700999111", wantErr: "together"},
		{name: "code only", code: "7391", wantErr: "together"},
		{name: "bad phone", phone: "12345", code: "7391", wantErr: "REVIEW_PHONE"},
		{name: "foreign phone", phone: "+79161234567", code: "7391", wantErr: "REVIEW_PHONE"},
		{name: "too short", phone: "0700999111", code: "739", wantErr: "exactly 4 digits"},
		{name: "too long", phone: "0700999111", code: "73915", wantErr: "exactly 4 digits"},
		{name: "non digits", phone: "0700999111", code: "73a1", wantErr: "exactly 4 digits"},
		{name: "all zero", phone: "0700999111", code: "0000", wantErr: "too easy"},
		{name: "all same", phone: "0700999111", code: "7777", wantErr: "too easy"},
		{name: "ascending", phone: "0700999111", code: "1234", wantErr: "too easy"},
		{name: "ascending 2", phone: "0700999111", code: "4567", wantErr: "too easy"},
		{name: "descending", phone: "0700999111", code: "4321", wantErr: "too easy"},
		{name: "repeated pair", phone: "0700999111", code: "1212", wantErr: "too easy"},
		{name: "common", phone: "0700999111", code: "2580", wantErr: "too easy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setDevEnv(t)
			t.Setenv("REVIEW_PHONE", tt.phone)
			t.Setenv("REVIEW_OTP_CODE", tt.code)
			cfg, err := Load()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				if tt.code != "" && strings.Contains(err.Error(), tt.code) {
					t.Errorf("error leaks the code: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Review.Phone != tt.wantPhone || cfg.Review.Enabled() != (tt.wantPhone != "") {
				t.Errorf("Review = %+v, want phone %q", cfg.Review, tt.wantPhone)
			}
			if tt.wantPhone != "" && cfg.Review.Code != tt.code {
				t.Errorf("code not carried through")
			}
		})
	}
}
