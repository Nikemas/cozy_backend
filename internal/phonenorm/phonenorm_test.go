package phonenorm

import "testing"

func TestE164(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		// Existing accepted spellings (backward compatibility).
		{"canonical", "+996700123456", "+996700123456", true},
		{"intl without plus", "996700123456", "+996700123456", true},
		{"local with leading zero", "0700123456", "+996700123456", true},
		{"canonical with spaces", "+996 700 123 456", "+996700123456", true},
		{"local with spaces", "0700 111 222", "+996700111222", true},
		{"local with dashes and parens", "(0700) 111-222", "+996700111222", true},
		{"intl with dashes", "996-700-111-222", "+996700111222", true},
		{"surrounding whitespace", "  +996700111222 ", "+996700111222", true},

		// New: bare 9-digit national number.
		{"nine digits", "700111222", "+996700111222", true},
		{"nine digits spaced", "700 111 222", "+996700111222", true},
		{"nine digits dashed", "555-12-34-56", "+996555123456", true},
		{"nine digits parens", "(700) 111 222", "+996700111222", true},

		// Rejected.
		{"empty", "", "", false},
		{"too short", "123", "", false},
		{"eight digits", "70011122", "", false},
		{"nine digits starting with zero", "070011122", "", false},
		{"nine digits with plus is not KG", "+700111222", "", false},
		{"foreign E.164", "+1234567890", "", false},
		{"russian number", "+7 700 111 22 33", "", false},
		{"local one digit too many", "07001234567", "", false},
		{"intl one digit too many", "+9967001234567", "", false},
		{"eight-prefixed trunk", "8700111222", "", false},
		{"letters only", "abc", "", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := E164(c.in)
			if ok != c.wantOK || got != c.want {
				t.Errorf("E164(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.wantOK)
			}
		})
	}
}
