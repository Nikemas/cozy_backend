package web

import "strings"

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
