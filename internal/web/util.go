package web

import (
	"fmt"
	"math"
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

// formatPrice renders a base_price/order total as "3200 сом" — good
// enough for the MVP fav/profile/address screens; a locale-aware
// thousands separator can follow once a real currency formatter is
// needed elsewhere.
func formatPrice(v float64) string {
	return fmt.Sprintf("%d сом", int(math.Round(v)))
}
