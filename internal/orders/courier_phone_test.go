package orders

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

func TestNormalizeCourierPhone(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		delivery bool
		want     *string
		wantCode string
	}{
		{"delivery local form", "0700 123 456", true, strPtr("+996700123456"), ""},
		{"delivery intl form", "+996 (555) 12-34-56", true, strPtr("+996555123456"), ""},
		{"delivery blank keeps NULL", "   ", true, nil, ""},
		{"delivery invalid", "12345", true, nil, "invalid_courier_phone"},
		{"delivery foreign", "+7 700 123 45 67", true, nil, "invalid_courier_phone"},
		{"pickup ignores valid", "0700123456", false, nil, ""},
		{"pickup ignores garbage", "abc", false, nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := normalizeCourierPhone(c.raw, c.delivery)
			if c.wantCode != "" {
				var ae *apperr.AppError
				if !errors.As(err, &ae) || ae.Code != c.wantCode {
					t.Fatalf("err = %v, want apperr %q", err, c.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
				t.Errorf("got %v, want %v", deref(got), deref(c.want))
			}
		})
	}
}

// TestOrderCourierPhoneJSON pins the wire shape: "courier_phone" is always
// present, a string or null.
func TestOrderCourierPhoneJSON(t *testing.T) {
	for _, c := range []struct {
		phone *string
		want  string
	}{{nil, `null`}, {strPtr("+996700123456"), `"+996700123456"`}} {
		b, err := json.Marshal(Order{CourierPhone: c.phone})
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		raw, ok := got["courier_phone"]
		if !ok || string(raw) != c.want {
			t.Errorf("courier_phone = %s (present %v), want %s", raw, ok, c.want)
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
