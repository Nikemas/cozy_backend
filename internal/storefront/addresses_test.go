package storefront

import "testing"

// These exercise AddressInput.Validate, the only address-CRUD logic that
// doesn't need a live database — AddressRepo's methods are thin SQL
// wrappers around it and aren't covered here (no Postgres available in
// this environment; see the Task 4 handoff report).

func TestAddressInputValidateRequiresAddressText(t *testing.T) {
	in := AddressInput{AddressText: "   "}
	if err := in.Validate(); err == nil {
		t.Fatal("Validate() with blank address text: got nil error, want one")
	}
}

func TestAddressInputValidateTrimsAddressText(t *testing.T) {
	in := AddressInput{AddressText: "  ул. Чуй 123  "}
	if err := in.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	if in.AddressText != "ул. Чуй 123" {
		t.Fatalf("AddressText = %q, want trimmed value", in.AddressText)
	}
}

func TestAddressInputValidateBlankLabelBecomesNil(t *testing.T) {
	blank := "   "
	in := AddressInput{AddressText: "ул. Чуй 123", Label: &blank}
	if err := in.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	if in.Label != nil {
		t.Fatalf("Label = %q, want nil after trimming a blank label", *in.Label)
	}
}

func TestAddressInputValidateTrimsLabel(t *testing.T) {
	label := "  Дом  "
	in := AddressInput{AddressText: "ул. Чуй 123", Label: &label}
	if err := in.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	if in.Label == nil || *in.Label != "Дом" {
		t.Fatalf("Label = %v, want \"Дом\"", in.Label)
	}
}
