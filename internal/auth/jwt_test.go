package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var testJWTSecret = []byte("0123456789abcdef0123456789abcdef")

func TestParseAccessTokenRoundTrip(t *testing.T) {
	tok, err := issueAccessToken(testJWTSecret, "cust-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	id, err := ParseAccessToken(testJWTSecret, tok)
	if err != nil || id != "cust-1" {
		t.Fatalf("ParseAccessToken = %q, %v", id, err)
	}
}

func TestParseAccessTokenRejectsOtherAlgorithms(t *testing.T) {
	claims := accessClaims{CustomerID: "cust-1", RegisteredClaims: jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
	}}

	// HS512 with the right secret: valid signature, wrong algorithm.
	hs512, err := jwt.NewWithClaims(jwt.SigningMethodHS512, claims).SignedString(testJWTSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAccessToken(testJWTSecret, hs512); err == nil {
		t.Error("HS512 token accepted, want rejection")
	}

	none, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAccessToken(testJWTSecret, none); err == nil {
		t.Error("alg=none token accepted, want rejection")
	}
}

func TestParseAccessTokenRequiresExpiry(t *testing.T) {
	noExp, err := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims{CustomerID: "cust-1"}).SignedString(testJWTSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAccessToken(testJWTSecret, noExp); err == nil {
		t.Error("token without exp accepted, want rejection")
	}

	expired, err := issueAccessToken(testJWTSecret, "cust-1", -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAccessToken(testJWTSecret, expired); err == nil {
		t.Error("expired token accepted, want rejection")
	}
}

func TestParseAccessTokenRequiresCustomerID(t *testing.T) {
	tok, err := issueAccessToken(testJWTSecret, "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAccessToken(testJWTSecret, tok); err == nil {
		t.Error("token without customer_id accepted")
	}
}
