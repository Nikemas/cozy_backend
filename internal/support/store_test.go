package support

import "testing"

func TestAdminArgsSearch(t *testing.T) {
	cases := []struct {
		q, digits string
		number    any
	}{
		{"0555 123 456", "555123456", nil},
		{"+996 555 123", "996555123", nil},
		{"#12", "", int64(12)},
		{"12", "", int64(12)},
		{"Айгерим", "", nil},
	}
	for _, c := range cases {
		args := adminArgs(ListFilter{Query: c.q})
		if args[3] != c.digits || args[4] != c.number {
			t.Errorf("%q: digits=%v number=%v, want %q %v", c.q, args[3], args[4], c.digits, c.number)
		}
	}
	if got := adminArgs(ListFilter{Query: "50%_off"})[2]; got != `50\%\_off` {
		t.Errorf("escape = %v", got)
	}
}
