package auth

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type accessClaims struct {
	CustomerID string `json:"customer_id"`
	jwt.RegisteredClaims
}

func issueAccessToken(secret []byte, customerID string, ttl time.Duration) (string, error) {
	claims := accessClaims{
		CustomerID: customerID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret)
}

// ParseAccessToken validates an access token and returns the customer ID
// it was issued for. Exported for the JWT-auth middleware other packages
// (orders, storefront, ...) will use once they need an authenticated
// customer.
func ParseAccessToken(secret []byte, tokenStr string) (customerID string, err error) {
	var claims accessClaims
	_, err = jwt.ParseWithClaims(tokenStr, &claims, func(t *jwt.Token) (interface{}, error) {
		return secret, nil
	})
	if err != nil {
		return "", err
	}
	return claims.CustomerID, nil
}
