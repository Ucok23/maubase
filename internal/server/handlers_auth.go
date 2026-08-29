package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"maubase/internal/audit"
	"maubase/internal/auth"
)

const sessionCookieName = auth.SessionCookieName

type ctxKey string

const ctxKeyUser ctxKey = "user"

func (s *Server) handleSignUp(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	user, err := s.auth.SignUp(r.Context(), req.Email, req.Password)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	s.audit.RecordLogged(r.Context(), audit.EventCustomerSignup,
		audit.Actor{ID: user.ID, Email: user.Email}, audit.Target{ID: user.ID, Email: user.Email}, nil)

	session, err := s.auth.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		// Signup succeeded but the immediate login somehow failed; still a
		// 201, client can hit /login separately.
		writeJSON(w, http.StatusCreated, map[string]any{"user": userJSON(user)})
		return
	}

	setSessionCookie(w, session)
	writeJSON(w, http.StatusCreated, map[string]any{"user": userJSON(user)})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	session, err := s.auth.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		// Recorded whether or not req.Email belongs to a real account —
		// same as OWNR-12's owner-plane equivalent, a failed authentication
		// attempt is exactly the kind of event an audit trail exists to
		// capture, whether or not the account is real.
		s.audit.RecordLogged(r.Context(), audit.EventCustomerLoginFailed, audit.Actor{}, audit.Target{Email: req.Email}, nil)
		writeAuthError(w, err)
		return
	}
	// req.Email matches the account's stored email exactly (Login looked
	// it up by that value), so it's safe to use directly here without a
	// separate lookup — same as the owner-plane login handler.
	s.audit.RecordLogged(r.Context(), audit.EventCustomerLogin,
		audit.Actor{ID: session.UserID, Email: req.Email}, audit.Target{}, nil)

	setSessionCookie(w, session)
	writeJSON(w, http.StatusOK, map[string]any{"expires_at": session.ExpiresAt})
}

// handleForgotPassword always responds 204, whether or not req.Email
// belongs to a real account — spec/password-reset.md PWRESET-02, the
// same "never reveal whether an email is registered" posture
// OWNR-12/IDNT-05 already take elsewhere in this codebase. An email is
// only actually sent when the account exists; a delivery/configuration
// failure (an unset MAUBASE_RESEND_API_KEY, Resend itself erroring) is
// still surfaced as a real 500, since that happens identically
// regardless of which email was requested and so doesn't leak anything
// account-specific.
func (s *Server) handleForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	rawToken, userID, ok, err := s.auth.CreateResetToken(r.Context(), req.Email)
	if err != nil {
		log.Printf("forgot-password: create reset token: %v", err)
		w.WriteHeader(http.StatusNoContent) // see the two error cases below for why this stays 204
		return
	}
	if ok {
		// Recorded only for a real account — a request against an email
		// that doesn't exist has no account to attribute the entry to, and
		// (unlike a failed login) isn't itself suspicious in the way this
		// entry exists to help spot: "was this account's password reset
		// from an unusual pattern of requests?" only has an answer once
		// there's an account in the picture.
		s.audit.RecordLogged(r.Context(), audit.EventCustomerPasswordResetRequested,
			audit.Actor{}, audit.Target{ID: userID, Email: req.Email}, nil)
		link := s.passwordResetURL + "?token=" + url.QueryEscape(rawToken)
		html := fmt.Sprintf(
			`<p>Someone requested a password reset for this account.</p><p><a href="%s">Reset your password</a></p><p>This link expires in one hour. If you didn't request this, you can ignore this email.</p>`,
			link,
		)
		// A send failure — including the unconfigured-deployment default,
		// email.NoopSender, which always errors by design — must not
		// change the response: PWRESET-02 requires 204 regardless of
		// whether the account exists, and this response happens after
		// CreateResetToken already told us it does. Surfacing the error
		// here (as this used to) turned "no email configured yet" into
		// an account-enumeration oracle on every fresh deployment: a real
		// account got 500, a fake one got 204. Logged instead, so an
		// operator still finds out delivery isn't working.
		if err := s.email.Send(r.Context(), req.Email, "Reset your password", html); err != nil {
			log.Printf("forgot-password: send reset email to %s: %v", req.Email, err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	userID, err := s.auth.ResetPassword(r.Context(), req.Token, req.Password)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	s.audit.RecordLogged(r.Context(), audit.EventCustomerPasswordResetCompleted,
		audit.Actor{ID: userID}, audit.Target{ID: userID}, nil)
	// Sessions are already revoked (inside ResetPassword's own
	// transaction); an OAuth access/refresh token issued to a third-
	// party client while one of those sessions was active is a separate
	// grant this same "sign out everywhere" moment must also cover — see
	// spec/password-reset.md PWRESET-09. The password change already
	// committed by this point, so a failure here is logged rather than
	// turned into an error response: telling the caller their reset
	// failed, when it didn't, would be worse, and the token they'd retry
	// with is already spent (single-use) regardless.
	if err := s.oauth.RevokeAllForSubject(r.Context(), userID); err != nil {
		log.Printf("reset-password: revoke oauth grants for %s: %v", userID, err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	token := sessionTokenFromRequest(r)
	if token != "" {
		// Resolved before Logout invalidates the session, so there's still
		// a user identity to attribute the audit entry to — same pattern
		// as the owner-plane logout handler. A missing or already-invalid
		// token logs nothing, since there's no one to name.
		if user, err := s.auth.ValidateSession(r.Context(), token); err == nil {
			s.audit.RecordLogged(r.Context(), audit.EventCustomerLogout,
				audit.Actor{ID: user.ID, Email: user.Email}, audit.Target{}, nil)
		}
		_ = s.auth.Logout(r.Context(), token)
	}
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value(ctxKeyUser).(*auth.User)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	writeJSON(w, http.StatusOK, userJSON(user))
}

// handleExportAccount answers "give me all my data": the caller's profile,
// every row they own across every owner-scoped auto-REST collection, and
// the metadata (not raw bytes — download those individually) of every
// file they've uploaded. See spec/identity.md IDNT-09.
func (s *Server) handleExportAccount(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value(ctxKeyUser).(*auth.User)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	records, err := s.restapi.ExportOwned(r.Context(), user.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	files, err := s.storage.ExportOwned(r.Context(), user.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"profile": userJSON(user),
		"records": records,
		"files":   files,
	})
}

// handleDeleteAccount permanently erases the caller's account: their rows
// in every owner-scoped auto-REST collection, every file they've uploaded
// (bytes and metadata), every outstanding OAuth grant issued to a
// third-party client on their behalf, and finally the identity-layer
// record itself (which cascades to their sessions and OAuth consents).
// See spec/identity.md IDNT-10/11/13.
func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value(ctxKeyUser).(*auth.User)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	// Ordered so that if any step fails partway, what's already been
	// removed can't come back to life and what hasn't yet is still
	// retryable: application data, then OAuth grants, then the account
	// itself last. Not wrapped in a transaction, so this is a best-effort
	// ordering, not an atomicity guarantee — acceptable for a destructive,
	// low-frequency operation like this.
	if err := s.restapi.DeleteOwned(r.Context(), user.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if err := s.storage.DeleteOwned(r.Context(), user.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if err := s.oauth.RevokeAllForSubject(r.Context(), user.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if err := s.auth.DeleteUser(r.Context(), user.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	// Logged after DeleteUser succeeds, using the identity captured before
	// it ran — same "record the target's identity even though it's gone
	// by the time anyone reads the log" treatment OWNR-15/17 already give
	// owner_delete.
	s.audit.RecordLogged(r.Context(), audit.EventCustomerAccountDeleted,
		audit.Actor{ID: user.ID, Email: user.Email}, audit.Target{ID: user.ID, Email: user.Email}, nil)
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// handleListConsents answers "what have I authorized?": every third-party
// client the caller has a standing OAuth scope grant for, and exactly
// what scopes. See spec/oauth-authorize-and-consent.md AUTHZ-11.
func (s *Server) handleListConsents(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value(ctxKeyUser).(*auth.User)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	consents, err := s.oauth.ListConsents(r.Context(), user.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"consents": consents})
}

// handleRevokeConsent is the actual "undo" for a single client's access:
// deletes the standing consent record and revokes every outstanding
// token already issued to that client on the caller's behalf. Unlike
// handleDeleteAccount, this doesn't touch anything else — the account,
// its data, and its grants to every *other* client are untouched. See
// spec/oauth-authorize-and-consent.md AUTHZ-11.
func (s *Server) handleRevokeConsent(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value(ctxKeyUser).(*auth.User)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	clientID := chi.URLParam(r, "client_id")
	if err := s.oauth.RevokeConsent(r.Context(), user.ID, clientID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// requireAuth resolves a session from either the session cookie (browser
// flows) or an Authorization: Bearer header (API/service clients), and
// rejects the request if neither is valid.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := sessionTokenFromRequest(r)
		if token == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		user, err := s.auth.ValidateSession(r.Context(), token)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		ctx := context.WithValue(r.Context(), ctxKeyUser, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func sessionTokenFromRequest(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if c, err := r.Cookie(sessionCookieName); err == nil {
		return c.Value
	}
	return ""
}

func setSessionCookie(w http.ResponseWriter, session *auth.Session) {
	auth.SetCookie(w, session)
}

func clearSessionCookie(w http.ResponseWriter) {
	auth.ClearCookie(w)
}

func userJSON(u *auth.User) map[string]any {
	return map[string]any{
		"id":         u.ID,
		"email":      u.Email,
		"created_at": u.CreatedAt,
	}
}

func writeAuthError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, auth.ErrEmailTaken):
		status = http.StatusConflict
	case errors.Is(err, auth.ErrInvalidCredentials),
		errors.Is(err, auth.ErrSessionNotFound):
		status = http.StatusUnauthorized
	case errors.Is(err, auth.ErrWeakPassword),
		errors.Is(err, auth.ErrInvalidEmail),
		errors.Is(err, auth.ErrResetTokenInvalid):
		status = http.StatusBadRequest
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
