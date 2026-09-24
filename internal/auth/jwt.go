package auth

import (
	"errors"
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
	},
		// Pin the algorithm: never let the token header pick it (alg
		// confusion / "none"), and never accept a token that doesn't
		// expire — every token we issue carries exp.
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return "", err
	}
	if claims.CustomerID == "" {
		return "", errors.New("auth: access token has no customer_id")
	}
	return claims.CustomerID, nil
}
