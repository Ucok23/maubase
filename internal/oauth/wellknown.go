package oauth

import (
	"encoding/json"
	"net/http"
)

// authServerMetadata is the RFC 8414 shape. MCP clients (and any other
// OAuth 2.1 client doing discovery) fetch this first to find every other
// endpoint below, rather than having them hardcoded or configured by hand.
type authServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint"`
	RevocationEndpoint                string   `json:"revocation_endpoint"`
	JWKSURI                           string   `json:"jwks_uri"`
	ScopesSupported                   []string `json:"scopes_supported"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	GrantTypesSupported               []string `json:"grant_types_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
}

// HandleAuthServerMetadata serves /.well-known/oauth-authorization-server.
func (s *Server) HandleAuthServerMetadata(w http.ResponseWriter, r *http.Request) {
	meta := authServerMetadata{
		Issuer:                            s.Issuer,
		AuthorizationEndpoint:             s.Issuer + "/oauth/authorize",
		TokenEndpoint:                     s.Issuer + "/oauth/token",
		RegistrationEndpoint:              s.Issuer + "/oauth/register",
		RevocationEndpoint:                s.Issuer + "/oauth/revoke",
		JWKSURI:                           s.Issuer + "/.well-known/jwks.json",
		ScopesSupported:                   AllowedScopes,
		ResponseTypesSupported:            []string{"code"},
		GrantTypesSupported:               []string{"authorization_code", "refresh_token"},
		TokenEndpointAuthMethodsSupported: []string{"none", "client_secret_basic", "client_secret_post"},
		CodeChallengeMethodsSupported:     []string{"S256"},
	}
	writeJSON(w, meta)
}

// HandleJWKS serves /.well-known/jwks.json: the public half of every
// signing key we've ever used, so a resource server (an MCP server,
// notably) can verify access tokens locally without calling back to us.
func (s *Server) HandleJWKS(w http.ResponseWriter, r *http.Request) {
	set, err := s.Keys.JWKS(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, set)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
