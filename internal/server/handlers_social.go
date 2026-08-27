package server

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"maubase/internal/auth"
)

// socialStateCookieName carries the CSRF state token across the redirect
// to the provider and back — short-lived (5 minutes is generous for a
// human to complete a login), cleared as soon as the callback reads it
// regardless of outcome.
const socialStateCookieName = "maubase_social_state"

// handleSocialStart begins "Continue with <provider>": generates a
// random state, stashes it in a cookie, and redirects to the provider's
// own authorization page. A provider name that isn't configured (missing
// from Server.socialProviders — its client id/secret weren't set) 404s,
// the same as a provider name that doesn't exist at all — see
// spec/social-login.md SOCIAL-05.
func (s *Server) handleSocialStart(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.socialProviders[chi.URLParam(r, "provider")]
	if !ok {
		http.NotFound(w, r)
		return
	}

	state, err := randomURLSafeString(24)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: socialStateCookieName, Value: state, Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: 300,
	})
	http.Redirect(w, r, provider.AuthCodeURL(state), http.StatusSeeOther)
}

// handleSocialCallback finishes the round trip: verifies state against
// the cookie handleSocialStart set (rejecting a mismatch as a likely CSRF
// attempt, per spec/social-login.md SOCIAL-04), exchanges the code,
// fetches the provider's profile, resolves it to a session via
// auth.Service.LoginOrCreateViaSocial, and redirects to
// Server.socialLoginRedirect with the session cookie already set.
func (s *Server) handleSocialCallback(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.socialProviders[chi.URLParam(r, "provider")]
	if !ok {
		http.NotFound(w, r)
		return
	}

	cookie, cookieErr := r.Cookie(socialStateCookieName)
	clearSocialStateCookie(w)
	if cookieErr != nil || r.URL.Query().Get("state") == "" || r.URL.Query().Get("state") != cookie.Value {
		http.Error(w, "invalid or missing state", http.StatusBadRequest)
		return
	}

	if errParam := r.URL.Query().Get("error"); errParam != "" {
		http.Error(w, "authorization denied: "+errParam, http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	tok, err := provider.Exchange(r.Context(), code)
	if err != nil {
		http.Error(w, "token exchange failed", http.StatusBadGateway)
		return
	}
	identity, err := provider.FetchIdentity(r.Context(), tok)
	if err != nil || identity.ProviderUserID == "" {
		http.Error(w, "failed to fetch profile", http.StatusBadGateway)
		return
	}

	// If the caller is already signed in, this becomes a "link a second
	// sign-in method to my account" request rather than an independent
	// identity resolution — see LoginOrCreateViaSocial's doc comment.
	// Tolerates a missing/expired/invalid session as simply anonymous,
	// same as requireAuth's "no credential" case elsewhere — this is the
	// one social route that must keep working with no session at all.
	var currentUserID string
	if token := sessionTokenFromRequest(r); token != "" {
		if u, err := s.auth.ValidateSession(r.Context(), token); err == nil {
			currentUserID = u.ID
		}
	}

	session, err := s.auth.LoginOrCreateViaSocial(r.Context(), provider.Name, identity.ProviderUserID, identity.Email, currentUserID)
	if err != nil {
		if errors.Is(err, auth.ErrSocialIdentityLinkedElsewhere) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	setSessionCookie(w, session)
	http.Redirect(w, r, s.socialLoginRedirect, http.StatusSeeOther)
}

func clearSocialStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: socialStateCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

func randomURLSafeString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
