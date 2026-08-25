package oauth

import (
	"time"

	"github.com/ory/fosite"
	fosOAuth2 "github.com/ory/fosite/handler/oauth2"
	"github.com/ory/fosite/token/jwt"
)

// newSession builds the JWT session for a freshly authorized grant. Its
// fields end up as claims/header on every access token issued for this
// grant (and are re-hydrated from storage on refresh), so this is the one
// place that decides what a resource server sees in the token.
//
// kid is embedded in the JWT header explicitly: Fosite's own signer only
// ever verifies against the *current* signing key (see keys.go), but an
// external resource server reading our JWKS can and should pick the right
// key by kid, so a token stays verifiable there across a key rotation even
// though our own introspection endpoint's guarantee is narrower.
func newSession(kid, issuer, userID, email string) *fosOAuth2.JWTSession {
	return &fosOAuth2.JWTSession{
		Subject:  userID,
		Username: email,
		JWTClaims: &jwt.JWTClaims{
			Subject:  userID,
			Issuer:   issuer,
			IssuedAt: time.Now().UTC(),
			Extra: map[string]interface{}{
				"email": email,
			},
		},
		JWTHeader: &jwt.Headers{
			Extra: map[string]interface{}{
				"kid": kid,
			},
		},
		ExpiresAt: map[fosite.TokenType]time.Time{},
	}
}
