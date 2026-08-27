package oauth

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/ory/fosite"
	fosOAuth2 "github.com/ory/fosite/handler/oauth2"

	"maubase/internal/auth"
)

// HandleAuthorize serves both steps of the browser-facing side of the
// authorization code flow: GET starts it (and short-circuits straight to
// the redirect if the user is already signed in with standing consent),
// POST handles the login form and the consent decision.
func (s *Server) HandleAuthorize(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		s.handleAuthorizePOST(w, r)
		return
	}
	s.handleAuthorizeGET(w, r)
}

func (s *Server) handleAuthorizeGET(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ar, err := s.Provider.NewAuthorizeRequest(ctx, r)
	if err != nil {
		s.Provider.WriteAuthorizeError(ctx, w, ar, err)
		return
	}

	user := s.currentUser(r)
	if user == nil {
		s.renderLogin(w, ar, r.URL.RawQuery, "")
		return
	}
	s.continueAuthorize(w, r, ar, user)
}

func (s *Server) handleAuthorizePOST(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	rawQuery := r.PostForm.Get("oauth_request")

	ar, err := s.reparseAuthorize(r, rawQuery)
	if err != nil {
		s.Provider.WriteAuthorizeError(ctx, w, ar, err)
		return
	}

	switch r.PostForm.Get("step") {
	case "login":
		email := r.PostForm.Get("email")
		password := r.PostForm.Get("password")
		sess, err := s.auth.Login(ctx, email, password)
		if err != nil {
			s.renderLogin(w, ar, rawQuery, "invalid email or password")
			return
		}
		auth.SetCookie(w, sess)
		user, err := s.auth.ValidateSession(ctx, sess.Token)
		if err != nil {
			s.renderLogin(w, ar, rawQuery, "please try again")
			return
		}
		s.continueAuthorize(w, r, ar, user)

	case "consent":
		user := s.currentUser(r)
		if user == nil {
			s.renderLogin(w, ar, rawQuery, "your session expired, please sign in again")
			return
		}
		if r.PostForm.Get("decision") != "allow" {
			s.Provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrAccessDenied.WithHint("The user denied the request."))
			return
		}
		granted := grantedScopes(r.PostForm["granted"], ar.GetRequestedScopes())
		// Whatever the user left checked among their previously-granted,
		// not-currently-requested scopes (the "You previously granted..."
		// section renderConsent shows) — anything they unchecked there is
		// dropped from what's persisted, i.e. revoked. Filtered against
		// the same existing-minus-requested set the consent screen was
		// built from, not just isAllowedScope, so a forged extra "keep"
		// value can't resurrect a scope the user never actually had.
		existing, _ := s.getConsent(ctx, user.ID, ar.GetClient().GetID())
		keepable := scopesNotIn(existing, ar.GetRequestedScopes())
		kept := grantedScopes(r.PostForm["keep"], fosite.Arguments(keepable))
		persisted := append(append([]string{}, granted...), kept...)
		if err := s.saveConsent(ctx, user.ID, ar.GetClient().GetID(), persisted); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		s.finishAuthorize(w, r, ar, user, granted)

	default:
		http.Error(w, "invalid step", http.StatusBadRequest)
	}
}

// continueAuthorize is reached once we know who's signed in: skip straight
// to the redirect if they've already consented to everything this request
// asks for, otherwise show the consent screen.
func (s *Server) continueAuthorize(w http.ResponseWriter, r *http.Request, ar fosite.AuthorizeRequester, user *auth.User) {
	requested := ar.GetRequestedScopes()
	existing, err := s.getConsent(r.Context(), user.ID, ar.GetClient().GetID())
	if err == nil && scopeSetContainsAll(existing, requested) {
		s.finishAuthorize(w, r, ar, user, requested)
		return
	}
	s.renderConsent(w, ar, r.URL.RawQuery, user, requested, scopesNotIn(existing, requested))
}

// scopesNotIn returns the scopes in have that aren't also in exclude —
// used to find the subset of a user's existing consent for a client that
// isn't part of the current request, so the consent screen can show it
// (and let the user revoke it) separately from what's being requested
// right now.
func scopesNotIn(have []string, exclude fosite.Arguments) []string {
	excludeSet := make(map[string]bool, len(exclude))
	for _, sc := range exclude {
		excludeSet[sc] = true
	}
	var out []string
	for _, sc := range have {
		if !excludeSet[sc] {
			out = append(out, sc)
		}
	}
	return out
}

func (s *Server) finishAuthorize(w http.ResponseWriter, r *http.Request, ar fosite.AuthorizeRequester, user *auth.User, scopes []string) {
	ctx := r.Context()
	for _, sc := range scopes {
		ar.GrantScope(sc)
	}

	kid, _, err := s.Keys.EnsureKey(ctx)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	session := newSession(kid, s.Issuer, user.ID, user.Email)

	resp, err := s.Provider.NewAuthorizeResponse(ctx, ar, session)
	if err != nil {
		s.Provider.WriteAuthorizeError(ctx, w, ar, err)
		return
	}
	s.Provider.WriteAuthorizeResponse(ctx, w, ar, resp)
}

// HandleToken is the token endpoint: authorization_code exchange (with PKCE
// verification) and refresh_token grants both land here.
func (s *Server) HandleToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	session := new(fosOAuth2.JWTSession)

	ar, err := s.Provider.NewAccessRequest(ctx, r, session)
	if err != nil {
		s.Provider.WriteAccessError(ctx, w, ar, err)
		return
	}

	resp, err := s.Provider.NewAccessResponse(ctx, ar)
	if err != nil {
		s.Provider.WriteAccessError(ctx, w, ar, err)
		return
	}
	s.Provider.WriteAccessResponse(ctx, w, ar, resp)
}

// HandleRevoke implements RFC 7009 token revocation.
func (s *Server) HandleRevoke(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	err := s.Provider.NewRevocationRequest(ctx, r)
	s.Provider.WriteRevocationResponse(ctx, w, err)
}

// --- helpers ---------------------------------------------------------------

func (s *Server) currentUser(r *http.Request) *auth.User {
	c, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		return nil
	}
	user, err := s.auth.ValidateSession(r.Context(), c.Value)
	if err != nil {
		return nil
	}
	return user
}

// reparseAuthorize reconstructs an AuthorizeRequester from the original
// query string a POST'd login/consent form carried in a hidden field.
// Fosite's NewAuthorizeRequest reads r.Form (query merged with any POST
// body), so a synthetic GET request with that query string round-trips it.
func (s *Server) reparseAuthorize(r *http.Request, rawQuery string) (fosite.AuthorizeRequester, error) {
	u := *r.URL
	u.RawQuery = rawQuery
	synthetic := &http.Request{
		Method: http.MethodGet,
		URL:    &u,
		// Deliberately empty, not r.Header: the original POST's
		// Content-Type header (form-urlencoded) paired with a nil Body
		// makes Go's ParseMultipartForm fail with "missing form body"
		// instead of the harmless ErrNotMultipart it'd return with no
		// Content-Type at all. A GET authorize request has no body to
		// describe anyway.
		Header: http.Header{},
	}
	return s.Provider.NewAuthorizeRequest(r.Context(), synthetic)
}

func grantedScopes(checked []string, requested fosite.Arguments) []string {
	var out []string
	for _, sc := range checked {
		if !isAllowedScope(sc) {
			continue
		}
		for _, req := range requested {
			if req == sc {
				out = append(out, sc)
				break
			}
		}
	}
	return out
}

func scopeSetContainsAll(have []string, want fosite.Arguments) bool {
	set := make(map[string]bool, len(have))
	for _, s := range have {
		set[s] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

type loginView struct {
	ClientName   string
	OAuthRequest string
	Error        string
}

func (s *Server) renderLogin(w http.ResponseWriter, ar fosite.AuthorizeRequester, rawQuery, errMsg string) {
	name := ar.GetClient().GetID()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = loginTpl.Execute(w, loginView{
		ClientName:   name,
		OAuthRequest: rawQuery,
		Error:        errMsg,
	})
}

type consentView struct {
	ClientName        string
	Email             string
	OAuthRequest      string
	Scopes            []string
	PreviouslyGranted []string
}

// renderConsent shows both what this request is asking for (requested —
// always listed, checked) and, separately, any scope the user granted
// this client in the past but isn't part of this request (previouslyGranted
// — also listed, checked, but this is the control that lets a user
// actually revoke a standing grant: unchecking one here and submitting
// "Allow" drops it from what's persisted, via saveConsent below. Before
// this, previously-granted scopes weren't shown at all, so there was
// nothing to uncheck — saveConsent's old union-with-existing logic kept
// them regardless of anything the form could express.
func (s *Server) renderConsent(w http.ResponseWriter, ar fosite.AuthorizeRequester, rawQuery string, user *auth.User, requested, previouslyGranted fosite.Arguments) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = consentTpl.Execute(w, consentView{
		ClientName:        ar.GetClient().GetID(),
		Email:             user.Email,
		OAuthRequest:      rawQuery,
		Scopes:            []string(requested),
		PreviouslyGranted: []string(previouslyGranted),
	})
}

func (s *Server) getConsent(ctx context.Context, userID, clientID string) ([]string, error) {
	var scopesJSON string
	err := s.db.QueryRowContext(ctx, `
		SELECT scopes FROM oauth_consents WHERE user_id = ? AND client_id = ?
	`, userID, clientID).Scan(&scopesJSON)
	if err != nil {
		return nil, err
	}
	var scopes []string
	if err := json.Unmarshal([]byte(scopesJSON), &scopes); err != nil {
		return nil, err
	}
	return scopes, nil
}

// saveConsent replaces this user's consent for clientID with exactly
// scopes — the caller is responsible for computing the full set that
// should end up persisted (typically the current request's granted
// scopes plus whichever previously-granted ones the user left checked;
// see the "consent" case in handleAuthorizePOST). This deliberately
// isn't a union with whatever was there before: a union would make
// revocation impossible from this screen no matter what the user
// unchecked, which is exactly the bug this replaced.
func (s *Server) saveConsent(ctx context.Context, userID, clientID string, scopes []string) error {
	deduped := make(map[string]bool, len(scopes))
	for _, sc := range scopes {
		deduped[sc] = true
	}
	out := make([]string, 0, len(deduped))
	for sc := range deduped {
		out = append(out, sc)
	}
	scopesJSON, err := json.Marshal(out)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO oauth_consents (user_id, client_id, scopes) VALUES (?, ?, ?)
		ON CONFLICT (user_id, client_id) DO UPDATE SET scopes = excluded.scopes
	`, userID, clientID, string(scopesJSON))
	return err
}
