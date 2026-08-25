package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ory/fosite"
	fosOAuth2 "github.com/ory/fosite/handler/oauth2"
)

type ctxKey string

const ctxKeySubject ctxKey = "oauth_subject"

// Authenticate validates a bearer access token and checks it carries
// scope, returning the subject (user id) it was issued for. It's the
// introspection logic behind RequireScope, pulled out so a caller that
// isn't a plain http.HandlerFunc — internal/realtime's WebSocket
// handshake, notably, which needs the same check before upgrading rather
// than before calling next(w, r) — can reuse it. Validates by calling
// into the same Fosite provider (stateful introspection against our own
// storage) rather than verifying the JWT locally — appropriate since
// this lives in the same binary as the authorization server. A separate
// resource server (an MCP server on another host, say) should instead
// verify the JWT itself against /.well-known/jwks.json, so it doesn't
// need network access back to this server on every request.
func (s *Server) Authenticate(ctx context.Context, token, scope string) (subject string, err error) {
	session := new(fosOAuth2.JWTSession)
	_, ar, err := s.Provider.IntrospectToken(ctx, token, fosite.AccessToken, session, scope)
	if err != nil {
		return "", fmt.Errorf("invalid or insufficient token: %w", err)
	}
	return ar.GetSession().GetSubject(), nil
}

// RequireScope protects a resource-server route with a bearer access
// token, checking it carries the given scope — see Authenticate for the
// validation itself.
func (s *Server) RequireScope(scope string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := fosite.AccessTokenFromRequest(r)
		if token == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "missing bearer token"})
			return
		}

		subject, err := s.Authenticate(r.Context(), token, scope)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid or insufficient token"})
			return
		}

		ctx := context.WithValue(r.Context(), ctxKeySubject, subject)
		next(w, r.WithContext(ctx))
	}
}

// SubjectFromContext returns the user ID an access token was issued for,
// as set by RequireScope.
func SubjectFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKeySubject).(string)
	return v, ok
}

// HandleWhoAmI is a minimal demo resource: it exists purely to prove the
// end-to-end loop (register client -> authorize -> token -> call API)
// works, ahead of the real auto-REST layer.
func (s *Server) HandleWhoAmI(w http.ResponseWriter, r *http.Request) {
	subject, _ := SubjectFromContext(r.Context())
	writeJSON(w, map[string]string{"subject": subject})
}
