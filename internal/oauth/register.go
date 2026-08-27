package oauth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Registration payloads are inherently small (a handful of short strings
// and a few URIs) — these are fixed limits, not deployment-tunable config,
// same way a JWT header size or a UUID length wouldn't be.
const (
	maxRegisterBodyBytes = 16 * 1024
	maxRedirectURIs      = 20
	maxRedirectURILen    = 2048
	maxClientNameLen     = 200
)

// allowed values we support; anything else in a registration request is
// rejected rather than silently ignored, per RFC 7591 §3.2.2 error
// semantics (invalid_client_metadata).
var (
	allowedAuthMethods  = map[string]bool{"none": true, "client_secret_basic": true, "client_secret_post": true}
	allowedGrantTypes   = map[string]bool{"authorization_code": true, "refresh_token": true}
	allowedResponseType = map[string]bool{"code": true}
)

type registerRequest struct {
	RedirectURIs            []string `json:"redirect_uris"`
	ClientName              string   `json:"client_name"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	Scope                   string   `json:"scope"`
}

type registerResponse struct {
	ClientID                string   `json:"client_id"`
	ClientSecret            string   `json:"client_secret,omitempty"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at"`
	ClientSecretExpiresAt   int64    `json:"client_secret_expires_at"`
	ClientName              string   `json:"client_name,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	Scope                   string   `json:"scope"`
}

// HandleRegister implements RFC 7591 Dynamic Client Registration. MCP
// clients hit this once, unauthenticated, to obtain a client_id (and, for
// confidential clients, a client_secret) before ever talking to a human —
// that's the whole point: no admin has to pre-provision a client_id.
func (s *Server) HandleRegister(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRegisterBodyBytes)
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRegisterError(w, http.StatusBadRequest, "invalid_client_metadata", "malformed JSON body, or body too large")
		return
	}

	if len(req.ClientName) > maxClientNameLen {
		writeRegisterError(w, http.StatusBadRequest, "invalid_client_metadata",
			fmt.Sprintf("client_name too long (max %d characters)", maxClientNameLen))
		return
	}

	if len(req.RedirectURIs) == 0 {
		writeRegisterError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris is required")
		return
	}
	if len(req.RedirectURIs) > maxRedirectURIs {
		writeRegisterError(w, http.StatusBadRequest, "invalid_redirect_uri",
			fmt.Sprintf("too many redirect_uris (max %d)", maxRedirectURIs))
		return
	}
	for _, ru := range req.RedirectURIs {
		if len(ru) > maxRedirectURILen {
			writeRegisterError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uri too long")
			return
		}
		u, err := url.Parse(ru)
		if err != nil || !u.IsAbs() {
			writeRegisterError(w, http.StatusBadRequest, "invalid_redirect_uri",
				fmt.Sprintf("redirect_uri %q is not a well-formed absolute URI", ru))
			return
		}
		if u.Fragment != "" {
			// RFC 6749 §3.1.2: "The redirection endpoint URI MUST NOT
			// include a fragment component." A fragment is never sent to
			// the server anyway (it's stripped client-side before the
			// request goes out), so one here can only be a
			// misconfiguration worth catching at registration time
			// rather than as a confusing failure at /oauth/authorize.
			writeRegisterError(w, http.StatusBadRequest, "invalid_redirect_uri",
				fmt.Sprintf("redirect_uri %q must not include a fragment", ru))
			return
		}
	}

	authMethod := req.TokenEndpointAuthMethod
	if authMethod == "" {
		authMethod = "client_secret_basic" // RFC 7591 §2 default
	}
	if !allowedAuthMethods[authMethod] {
		writeRegisterError(w, http.StatusBadRequest, "invalid_client_metadata",
			"unsupported token_endpoint_auth_method (supported: none, client_secret_basic, client_secret_post)")
		return
	}

	grantTypes := req.GrantTypes
	if len(grantTypes) == 0 {
		grantTypes = []string{"authorization_code"}
	}
	for _, gt := range grantTypes {
		if !allowedGrantTypes[gt] {
			writeRegisterError(w, http.StatusBadRequest, "invalid_client_metadata",
				fmt.Sprintf("unsupported grant_type %q (supported: authorization_code, refresh_token)", gt))
			return
		}
	}

	responseTypes := req.ResponseTypes
	if len(responseTypes) == 0 {
		responseTypes = []string{"code"}
	}
	for _, rt := range responseTypes {
		if !allowedResponseType[rt] {
			writeRegisterError(w, http.StatusBadRequest, "invalid_client_metadata",
				fmt.Sprintf("unsupported response_type %q (supported: code)", rt))
			return
		}
	}

	scopes := AllowedScopes
	if strings.TrimSpace(req.Scope) != "" {
		scopes = strings.Fields(req.Scope)
		for _, sc := range scopes {
			if !isAllowedScope(sc) {
				writeRegisterError(w, http.StatusBadRequest, "invalid_client_metadata",
					fmt.Sprintf("unsupported scope %q", sc))
				return
			}
		}
	}

	isPublic := authMethod == "none"
	var plainSecret string
	var secretHash []byte
	if !isPublic {
		var err error
		plainSecret, err = randomSecret(32)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		secretHash, err = s.Config.GetSecretsHasher(r.Context()).Hash(r.Context(), []byte(plainSecret))
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	clientID := uuid.NewString()
	if err := s.insertClient(r.Context(), clientID, secretHash, isPublic, authMethod, req.ClientName, req.RedirectURIs, grantTypes, responseTypes, scopes); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	resp := registerResponse{
		ClientID:                clientID,
		ClientSecret:            plainSecret,
		ClientIDIssuedAt:        time.Now().Unix(),
		ClientSecretExpiresAt:   0, // never expires
		ClientName:              req.ClientName,
		RedirectURIs:            req.RedirectURIs,
		TokenEndpointAuthMethod: authMethod,
		GrantTypes:              grantTypes,
		ResponseTypes:           responseTypes,
		Scope:                   strings.Join(scopes, " "),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) insertClient(ctx context.Context, id string, secretHash []byte, isPublic bool, authMethod, name string, redirectURIs, grantTypes, responseTypes, scopes []string) error {
	redirectJSON, _ := json.Marshal(redirectURIs)
	grantJSON, _ := json.Marshal(grantTypes)
	responseJSON, _ := json.Marshal(responseTypes)
	scopesJSON, _ := json.Marshal(scopes)

	var secret sql.NullString
	if secretHash != nil {
		secret = sql.NullString{String: string(secretHash), Valid: true}
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO oauth_clients
			(id, secret_hash, client_name, redirect_uris, grant_types, response_types, scopes, token_endpoint_auth_method, is_public)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, nullableBytes(secret), name, string(redirectJSON), string(grantJSON), string(responseJSON), string(scopesJSON), authMethod, isPublic)
	return err
}

func nullableBytes(s sql.NullString) []byte {
	if !s.Valid {
		return nil
	}
	return []byte(s.String)
}

func randomSecret(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func writeRegisterError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":             code,
		"error_description": description,
	})
}
