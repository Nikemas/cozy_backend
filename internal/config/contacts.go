package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Contacts are the shop's public contact details shown on the storefront's
// info pages (/contacts, /privacy, /terms). Every field is optional: an
// empty value means the page leaves that line out entirely instead of
// showing a placeholder.
type Contacts struct {
	// Phone is the customer support number as it should be displayed,
	// e.g. "+996 555 123 456". SHOP_PHONE.
	Phone string
	// WhatsApp is a phone number with a WhatsApp account, e.g.
	// "+996 555 123 456"; linked as https://wa.me/<digits>. SHOP_WHATSAPP.
	WhatsApp string
	// Telegram is a public username, with or without "@"; linked as
	// https://t.me/<username>. SHOP_TELEGRAM.
	Telegram string
	// Email is the address for customer and personal-data requests.
	// SHOP_EMAIL.
	Email string
	// Hours is the support working hours as free text, e.g.
	// "10:00–20:00" (keep it language-neutral: it is shown on both the RU
	// and KY pages). SHOP_HOURS.
	Hours string
	// BankDetails is the seller's bank details for the public offer, free
	// text, e.g. "ОАО «Бакай Банк», р/с 1240..., БИК 124001". SHOP_BANK_DETAILS.
	BankDetails string
	// LegalName is the seller / app developer / personal-data operator as
	// it should appear on the footer, /contacts, /privacy, /terms and
	// /account-deletion, e.g. "ИП Фамилия Имя" (language-neutral: shown on
	// both RU and KY pages). Empty hides every legal-entity line, so no
	// legal name is ever hardcoded in templates. SHOP_LEGAL_NAME.
	LegalName string
	// TaxID is the seller's INN (10–14 digits). SHOP_INN.
	TaxID string
	// LegalAddress is the seller's registered address, free text.
	// SHOP_LEGAL_ADDRESS.
	LegalAddress string
}

var (
	contactPhonePattern    = regexp.MustCompile(`^\+?[0-9][0-9 ()-]{4,24}$`)
	contactTelegramPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{4,31}$`)
	contactEmailPattern    = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	contactTaxIDPattern    = regexp.MustCompile(`^[0-9]{10,14}$`)
	nonDigitPattern        = regexp.MustCompile(`[^0-9]`)
)

// maxContactTextLen caps the free-text fields (hours, bank details): they
// are rendered verbatim on public pages, so a runaway value is a typo.
const maxContactTextLen = 300

// loadContacts reads the SHOP_* variables. A malformed value is an error
// (the server refuses to start) so a typo never reaches the public pages.
func loadContacts() (Contacts, error) {
	c := Contacts{
		Phone:        strings.TrimSpace(os.Getenv("SHOP_PHONE")),
		WhatsApp:     strings.TrimSpace(os.Getenv("SHOP_WHATSAPP")),
		Telegram:     strings.TrimPrefix(strings.TrimSpace(os.Getenv("SHOP_TELEGRAM")), "@"),
		Email:        strings.TrimSpace(os.Getenv("SHOP_EMAIL")),
		Hours:        strings.TrimSpace(os.Getenv("SHOP_HOURS")),
		BankDetails:  strings.TrimSpace(os.Getenv("SHOP_BANK_DETAILS")),
		LegalName:    strings.TrimSpace(os.Getenv("SHOP_LEGAL_NAME")),
		TaxID:        strings.TrimSpace(os.Getenv("SHOP_INN")),
		LegalAddress: strings.TrimSpace(os.Getenv("SHOP_LEGAL_ADDRESS")),
	}
	if err := c.validate(); err != nil {
		return Contacts{}, err
	}
	return c, nil
}

func (c Contacts) validate() error {
	checks := []struct {
		key, value string
		ok         func(string) bool
		want       string
	}{
		{"SHOP_PHONE", c.Phone, contactPhonePattern.MatchString, `a phone number like "+996 555 123 456"`},
		{"SHOP_WHATSAPP", c.WhatsApp, contactPhonePattern.MatchString, `a phone number like "+996 555 123 456"`},
		{"SHOP_TELEGRAM", c.Telegram, contactTelegramPattern.MatchString, `a Telegram username like "@cozy_kg"`},
		{"SHOP_EMAIL", c.Email, contactEmailPattern.MatchString, `an e-mail address`},
		{"SHOP_HOURS", c.Hours, shortText, fmt.Sprintf("at most %d characters", maxContactTextLen)},
		{"SHOP_BANK_DETAILS", c.BankDetails, shortText, fmt.Sprintf("at most %d characters", maxContactTextLen)},
		{"SHOP_LEGAL_NAME", c.LegalName, shortText, fmt.Sprintf("at most %d characters", maxContactTextLen)},
		{"SHOP_INN", c.TaxID, contactTaxIDPattern.MatchString, "10 to 14 digits"},
		{"SHOP_LEGAL_ADDRESS", c.LegalAddress, shortText, fmt.Sprintf("at most %d characters", maxContactTextLen)},
	}
	for _, ch := range checks {
		if ch.value != "" && !ch.ok(ch.value) {
			return fmt.Errorf("%s must be %s, got %q", ch.key, ch.want, ch.value)
		}
	}
	return nil
}

func shortText(s string) bool { return len([]rune(s)) <= maxContactTextLen }

// PhoneDial is Phone reduced to what a tel: link takes — digits, keeping
// a leading "+" ("" when Phone is empty). Templates write
// href="tel:{{.PhoneDial}}": html/template only trusts the tel: scheme
// when it is literal template text, not part of the interpolated value.
func (c Contacts) PhoneDial() string {
	if c.Phone == "" {
		return ""
	}
	digits := nonDigitPattern.ReplaceAllString(c.Phone, "")
	if strings.HasPrefix(c.Phone, "+") {
		return "+" + digits
	}
	return digits
}

// WhatsAppURL is the wa.me chat link for WhatsApp ("" when empty).
func (c Contacts) WhatsAppURL() string {
	if c.WhatsApp == "" {
		return ""
	}
	return "https://wa.me/" + nonDigitPattern.ReplaceAllString(c.WhatsApp, "")
}

// TelegramURL is the t.me link for Telegram ("" when empty).
func (c Contacts) TelegramURL() string {
	if c.Telegram == "" {
		return ""
	}
	return "https://t.me/" + c.Telegram
}

// TelegramHandle is Telegram with the leading "@" for display.
func (c Contacts) TelegramHandle() string {
	if c.Telegram == "" {
		return ""
	}
	return "@" + c.Telegram
}

// HasAny reports whether at least one contact channel is configured, so a
// page can drop a whole contacts block instead of rendering it empty.
func (c Contacts) HasAny() bool {
	return c.Phone != "" || c.WhatsApp != "" || c.Telegram != "" || c.Email != "" || c.Hours != ""
}
