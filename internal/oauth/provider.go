// Package oauth is the authorization-server layer: it lets this server's
// users authorize third-party apps (including MCP clients) against this
// server's API, distinct from internal/auth's identity layer (who is this
// human?). It's built on Ory Fosite for the parts that are easy to get
// subtly wrong — PKCE, token rotation, grant-type edge cases.
package oauth

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"

	"maubase/internal/auth"
)

// Server holds everything the OAuth authorization server endpoints need:
// the Fosite provider itself plus the pieces Fosite doesn't own (identity
// checks, consent bookkeeping, key/JWKS serving, dynamic registration).
type Server struct {
	Provider fosite.OAuth2Provider
	Storage  *Storage
	Keys     *KeyStore
	Config   *fosite.Config
	Issuer   string

	auth *auth.Service
	db   *sql.DB
}

// NewServer wires the Fosite compositor with our SQLite-backed storage and
// a persisted RSA signing key, and returns it ready to mount into routes.
// issuer is this server's own public base URL (e.g. "https://example.com"),
// used both as the JWT "iss" claim and in discovery metadata.
func NewServer(ctx context.Context, db *sql.DB, authSvc *auth.Service, issuer string) (*Server, error) {
	storage := NewStorage(db)
	keys := NewKeyStore(db)

	hmacSecret, err := keys.EnsureHMACSecret(ctx)
	if err != nil {
		return nil, fmt.Errorf("ensure hmac secret: %w", err)
	}
	if _, _, err := keys.EnsureKey(ctx); err != nil {
		return nil, fmt.Errorf("ensure signing key: %w", err)
	}

	config := &fosite.Config{
		AccessTokenLifespan:      time.Hour,
		RefreshTokenLifespan:     30 * 24 * time.Hour,
		AuthorizeCodeLifespan:    5 * time.Minute,
		GlobalSecret:             hmacSecret,
		HashCost:                 12,
		ScopeStrategy:            fosite.ExactScopeStrategy,
		AudienceMatchingStrategy: fosite.DefaultAudienceMatchingStrategy,
		AccessTokenIssuer:        issuer,
		TokenURL:                 issuer + "/oauth/token",
		// OAuth 2.1 / MCP both require PKCE unconditionally, not just for
		// public clients.
		EnforcePKCE: true,
	}

	hmacStrategy := compose.NewOAuth2HMACStrategy(config)
	jwtStrategy := compose.NewOAuth2JWTStrategy(keys.KeyGetter, hmacStrategy, config)
	strategy := &compose.CommonStrategy{
		CoreStrategy: jwtStrategy,
	}

	provider := compose.Compose(
		config,
		storage,
		strategy,
		compose.OAuth2AuthorizeExplicitFactory,
		compose.OAuth2PKCEFactory,
		compose.OAuth2RefreshTokenGrantFactory,
		compose.OAuth2TokenRevocationFactory,
		compose.OAuth2TokenIntrospectionFactory,
	)

	return &Server{
		Provider: provider,
		Storage:  storage,
		Keys:     keys,
		Config:   config,
		Issuer:   issuer,
		auth:     authSvc,
		db:       db,
	}, nil
}

// RevokeAllForSubject deletes every outstanding OAuth grant — authorize
// code, access token, refresh token, PKCE request — issued to any
// third-party client on behalf of subject. Intended for account deletion:
// see internal/server's handleDeleteAccount.
func (s *Server) RevokeAllForSubject(ctx context.Context, subject string) error {
	return s.Storage.RevokeForSubject(ctx, subject)
}
