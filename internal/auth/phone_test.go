package auth

import "testing"

func TestNormalizePhone(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"+996700123456", "+996700123456", false},
		{"996700123456", "+996700123456", false},
		{"0700123456", "+996700123456", false},
		{"+996 700 123 456", "+996700123456", false},
		{"123", "", true},
		{"+1234567890", "", true},
		{"07001234567", "", true}, // one digit too many
	}

	for _, c := range cases {
		got, err := NormalizePhone(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("NormalizePhone(%q) = %q, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizePhone(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("NormalizePhone(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestForNikitaStripsPlus(t *testing.T) {
	if got := forNikita("+996700123456"); got != "996700123456" {
		t.Errorf("forNikita() = %q, want %q", got, "996700123456")
	}
}
