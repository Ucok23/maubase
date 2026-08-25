package oauth

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/ory/fosite"
	fosOAuth2 "github.com/ory/fosite/handler/oauth2"
)

type ctxKey string

const ctxKeySubject ctxKey = "oauth_subject"

// RequireScope protects a resource-server route with a bearer access
// token, checking it carries the given scope. It validates the token by
// calling into the same Fosite provider (stateful introspection against
// our own storage) rather than verifying the JWT locally — appropriate
// since this demo endpoint lives in the same binary as the authorization
// server. A separate resource server (an MCP server on another host, say)
// should instead verify the JWT itself against /.well-known/jwks.json, so
// it doesn't need network access back to this server on every request.
func (s *Server) RequireScope(scope string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := fosite.AccessTokenFromRequest(r)
		if token == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "missing bearer token"})
			return
		}

		session := new(fosOAuth2.JWTSession)
		_, ar, err := s.Provider.IntrospectToken(r.Context(), token, fosite.AccessToken, session, scope)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid or insufficient token"})
			return
		}

		ctx := context.WithValue(r.Context(), ctxKeySubject, ar.GetSession().GetSubject())
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
