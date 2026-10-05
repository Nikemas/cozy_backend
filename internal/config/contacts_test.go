package config

import (
	"strings"
	"testing"
)

func TestLoad_ContactsDefaultToEmpty(t *testing.T) {
	setDevEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Contacts != (Contacts{}) {
		t.Fatalf("Contacts = %+v, want all empty", cfg.Contacts)
	}
	if cfg.Contacts.HasAny() {
		t.Fatal("HasAny() = true for empty contacts")
	}
}

func TestLoad_ContactsFromEnv(t *testing.T) {
	setDevEnv(t)
	t.Setenv("SHOP_PHONE", " +996 (555) 123-456 ")
	t.Setenv("SHOP_WHATSAPP", "+996 700 000 001")
	t.Setenv("SHOP_TELEGRAM", "@cozy_kg")
	t.Setenv("SHOP_EMAIL", "help@cozy.kg")
	t.Setenv("SHOP_HOURS", "10:00–20:00")
	t.Setenv("SHOP_BANK_DETAILS", "ОАО «Банк», р/с 1240000000000000, БИК 124001")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	c := cfg.Contacts
	if c.Phone != "+996 (555) 123-456" || c.Telegram != "cozy_kg" || c.Email != "help@cozy.kg" {
		t.Errorf("Contacts = %+v", c)
	}
	if got, want := c.PhoneDial(), "+996555123456"; got != want {
		t.Errorf("PhoneDial = %q, want %q", got, want)
	}
	if got, want := c.WhatsAppURL(), "https://wa.me/996700000001"; got != want {
		t.Errorf("WhatsAppURL = %q, want %q", got, want)
	}
	if got, want := c.TelegramURL(), "https://t.me/cozy_kg"; got != want {
		t.Errorf("TelegramURL = %q, want %q", got, want)
	}
	if got, want := c.TelegramHandle(), "@cozy_kg"; got != want {
		t.Errorf("TelegramHandle = %q, want %q", got, want)
	}
	if !c.HasAny() {
		t.Error("HasAny() = false with contacts set")
	}
}

func TestLoad_ContactsRejectMalformedValues(t *testing.T) {
	cases := map[string]string{
		"SHOP_PHONE":        "call us",
		"SHOP_WHATSAPP":     "wa.me/123",
		"SHOP_TELEGRAM":     "t.me/cozy",
		"SHOP_EMAIL":        "not-an-email",
		"SHOP_HOURS":        strings.Repeat("x", maxContactTextLen+1),
		"SHOP_BANK_DETAILS": strings.Repeat("x", maxContactTextLen+1),
	}
	for key, value := range cases {
		t.Run(key, func(t *testing.T) {
			setDevEnv(t)
			t.Setenv(key, value)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("Load with %s=%q: err = %v, want an error naming %s", key, value, err, key)
			}
		})
	}
}

func TestContactsLinksEmptyWhenUnset(t *testing.T) {
	var c Contacts
	if c.PhoneDial() != "" || c.WhatsAppURL() != "" || c.TelegramURL() != "" || c.TelegramHandle() != "" {
		t.Fatal("links of empty contacts must be empty")
	}
}

func TestContactsPhoneDialKeepsLocalFormatWithoutPlus(t *testing.T) {
	c := Contacts{Phone: "0555 123 456"}
	if got, want := c.PhoneDial(), "0555123456"; got != want {
		t.Fatalf("PhoneDial = %q, want %q", got, want)
	}
}
