package web

import (
	"math"
	"strconv"
	"strings"
)

// maskPhone renders a phone like "+996700123456" as "+996700 ••• 456" for
// display in the header/aside account card and the profile screen — good
// enough for Foundation; Task 4 can swap in a locale-aware formatter.
func maskPhone(phone string) string {
	digits := strings.TrimPrefix(phone, "+")
	if len(digits) < 7 {
		return phone
	}
	head := digits[:6]
	tail := digits[len(digits)-3:]
	return "+" + head + " ••• " + tail
}

// formatAmount renders a KGS amount the way the design's prototype does —
// thousands grouped with a space, no decimals, then unit (the
// "common.currency" locale string: "сом" in both RU and KY). Prices are
// NUMERIC(10,2) but soms aren't split into tyiyns in practice, so this
// rounds to the nearest whole som. The single money formatter for the
// site: Go code calls it with the "common.currency" string, templates
// call {{money}}.
func formatAmount(v float64, unit string) string {
	n := int64(math.Round(v))
	neg := n < 0
	if neg {
		n = -n
	}
	digits := strconv.FormatInt(n, 10)

	var grouped []byte
	for i := 0; i < len(digits); i++ {
		if i > 0 && (len(digits)-i)%3 == 0 {
			grouped = append(grouped, ' ')
		}
		grouped = append(grouped, digits[i])
	}
	out := string(grouped)
	if neg {
		out = "-" + out
	}
	if unit == "" {
		return out
	}
	return out + " " + unit
}
