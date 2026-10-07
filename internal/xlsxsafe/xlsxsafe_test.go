package xlsxsafe

import "testing"

func TestTextEscapesFormulaTriggers(t *testing.T) {
	cases := []struct{ in, want string }{
		{`=HYPERLINK("http://x","y")`, `'=HYPERLINK("http://x","y")`},
		{"+1", "'+1"},
		{"-2+3", "'-2+3"},
		{"@SUM(A1)", "'@SUM(A1)"},
		{"\t=1+1", "'\t=1+1"},
		{"\r=1+1", "'\r=1+1"},
		{"'=already quoted", "''=already quoted"},
		{"Кроссовки Cozy Run", "Кроссовки Cozy Run"},
		{"CZ-1001-39", "CZ-1001-39"},
		{"2026-01-05", "2026-01-05"},
		{"'plain apostrophe", "'plain apostrophe"},
		{"", ""},
	}
	for _, c := range cases {
		if got := Text(c.in); got != c.want {
			t.Errorf("Text(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestUnescapeRoundTripsText(t *testing.T) {
	values := []string{
		`=HYPERLINK("http://x","y")`, "+1", "-2+3", "@SUM(A1)", "\t=x", "\r=x",
		"'=x", "''-1", "'plain", "plain", "", "Белый",
	}
	for _, v := range values {
		if got := Unescape(Text(v)); got != v {
			t.Errorf("Unescape(Text(%q)) = %q", v, got)
		}
	}
}

func TestUnescapeLeavesOrdinaryApostrophesAlone(t *testing.T) {
	for _, v := range []string{"'plain", "'", "O'Neill", "''"} {
		if got := Unescape(v); got != v {
			t.Errorf("Unescape(%q) = %q, want unchanged", v, got)
		}
	}
}

func TestCellEscapesOnlyStrings(t *testing.T) {
	if got := Cell("=1+1"); got != "'=1+1" {
		t.Errorf("Cell(string) = %#v", got)
	}
	if got := Cell(-5); got != -5 {
		t.Errorf("Cell(int) = %#v, want numeric -5", got)
	}
	if got := Cell(-2.5); got != -2.5 {
		t.Errorf("Cell(float) = %#v, want numeric -2.5", got)
	}
	if got := Cell(nil); got != nil {
		t.Errorf("Cell(nil) = %#v", got)
	}
}

func TestRowDoesNotMutateInput(t *testing.T) {
	in := []any{"=x", 1}
	out := Row(in)
	if in[0] != "=x" {
		t.Errorf("input mutated: %#v", in)
	}
	if out[0] != "'=x" || out[1] != 1 {
		t.Errorf("Row = %#v", out)
	}
}
