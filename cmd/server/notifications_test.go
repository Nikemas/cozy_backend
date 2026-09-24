package main

import "testing"

func TestSMSStatusFallbackFromEnv(t *testing.T) {
	env := func(v string, set bool) func(string) (string, bool) {
		return func(string) (string, bool) { return v, set }
	}
	if on, err := smsStatusFallbackFromEnv(env("", false)); on || err != nil {
		t.Errorf("unset: got %v, %v; want false, nil", on, err)
	}
	if on, err := smsStatusFallbackFromEnv(env("true", true)); !on || err != nil {
		t.Errorf("true: got %v, %v", on, err)
	}
	if _, err := smsStatusFallbackFromEnv(env("yes please", true)); err == nil {
		t.Error("garbage value: want error")
	}
}
