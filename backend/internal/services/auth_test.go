package services

import (
	"testing"

	"baremetal-platform/backend/internal/config"

	"github.com/golang-jwt/jwt/v5"
)

func TestAuthParseRejectsUnexpectedSigningMethod(t *testing.T) {
	token := jwt.NewWithClaims(jwt.SigningMethodNone, Claims{Email: "admin@example.com"})
	signed, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	_, err = (AuthService{cfg: config.Config{JWTSecret: "test-secret"}}).Parse(signed)
	if err == nil {
		t.Fatalf("expected non-HMAC token to be rejected")
	}
}
