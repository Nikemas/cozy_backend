package staff

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// newSessionToken returns a random session token to hand to the client (via
// an httpOnly cookie), and the hash of it that we store — so a stolen DB
// dump doesn't hand out usable staff sessions. Mirrors internal/auth/token.go.
func newSessionToken() (raw string, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	return raw, hashSessionToken(raw), nil
}

func hashSessionToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
