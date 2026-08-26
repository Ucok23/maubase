// Owner-plane HTTP surface: login/session for the team running this
// deployment, and basic owner-account management. Entirely separate from
// handlers_auth.go's customer-plane routes — different cookie, different
// table, different session validation, no path between the two.
package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"maubase/internal/audit"
	"maubase/internal/ownerauth"
)

type ownerCtxKey string

const ctxKeyOwner ownerCtxKey = "owner"

func (s *Server) handleOwnerLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	session, err := s.ownerAuth.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		// req.Email is recorded as-is: it's exactly what was attempted,
		// whether or not it corresponds to a real account, which is the
		// point of a failed-login audit entry.
		s.audit.RecordLogged(r.Context(), audit.EventLoginFailed, audit.Actor{}, audit.Target{Email: req.Email}, nil)
		writeOwnerAuthError(w, err)
		return
	}
	// req.Email matches the account's stored email exactly (Login looked
	// it up by that value), so it's safe to use directly here without a
	// separate lookup.
	s.audit.RecordLogged(r.Context(), audit.EventLogin, audit.Actor{ID: session.OwnerID, Email: req.Email}, audit.Target{}, nil)
	ownerauth.SetCookie(w, session)
	writeJSON(w, http.StatusOK, map[string]any{"expires_at": session.ExpiresAt})
}

func (s *Server) handleOwnerLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(ownerauth.SessionCookieName); err == nil {
		// Resolved before Logout invalidates the session, so there's still
		// an owner identity to attribute the audit entry to. A missing or
		// already-invalid cookie logs nothing — there's no one to name.
		if owner, err := s.ownerAuth.ValidateSession(r.Context(), c.Value); err == nil {
			s.audit.RecordLogged(r.Context(), audit.EventLogout, audit.Actor{ID: owner.ID, Email: owner.Email}, audit.Target{}, nil)
		}
		_ = s.ownerAuth.Logout(r.Context(), c.Value)
	}
	ownerauth.ClearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleOwnerMe(w http.ResponseWriter, r *http.Request) {
	owner := ownerFromContext(r.Context())
	writeJSON(w, http.StatusOK, ownerJSON(owner))
}

func (s *Server) handleListOwners(w http.ResponseWriter, r *http.Request) {
	owners, err := s.ownerAuth.ListOwners(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(owners))
	for _, o := range owners {
		out = append(out, ownerJSON(o))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateOwner(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	owner, err := s.ownerAuth.CreateOwner(r.Context(), req.Email, req.Password, ownerauth.Role(req.Role))
	if err != nil {
		writeOwnerAuthError(w, err)
		return
	}
	actor := ownerFromContext(r.Context())
	s.audit.RecordLogged(r.Context(), audit.EventOwnerCreate,
		audit.Actor{ID: actor.ID, Email: actor.Email},
		audit.Target{ID: owner.ID, Email: owner.Email},
		map[string]any{"role": string(owner.Role)},
	)
	writeJSON(w, http.StatusCreated, ownerJSON(owner))
}

func (s *Server) handleDeleteOwner(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	deleted, err := s.ownerAuth.DeleteOwner(r.Context(), id)
	if err != nil {
		writeOwnerAuthError(w, err)
		return
	}
	actor := ownerFromContext(r.Context())
	s.audit.RecordLogged(r.Context(), audit.EventOwnerDelete,
		audit.Actor{ID: actor.ID, Email: actor.Email},
		audit.Target{ID: deleted.ID, Email: deleted.Email},
		map[string]any{"role": string(deleted.Role)},
	)
	w.WriteHeader(http.StatusNoContent)
}

// handleListAuditLog returns audit entries newest-first, paginated.
func (s *Server) handleListAuditLog(w http.ResponseWriter, r *http.Request) {
	limit, offset := parseAuditPagination(r)
	entries, err := s.audit.List(r.Context(), limit, offset)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]any{
			"id":           e.ID,
			"event":        e.Event,
			"actor_id":     e.ActorID,
			"actor_email":  e.ActorEmail,
			"target_id":    e.TargetID,
			"target_email": e.TargetEmail,
			"metadata":     e.Metadata,
			"created_at":   e.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

const (
	auditDefaultLimit = 50
	auditMaxLimit     = 200
)

func parseAuditPagination(r *http.Request) (limit, offset int) {
	limit, offset = auditDefaultLimit, 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= auditMaxLimit {
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return limit, offset
}

// requireOwnerRole authenticates the owner-plane session cookie and
// rejects the request unless the signed-in owner's role meets min in the
// role hierarchy (ownerauth.Role.AtLeast).
func (s *Server) requireOwnerRole(min ownerauth.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(ownerauth.SessionCookieName)
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			owner, err := s.ownerAuth.ValidateSession(r.Context(), c.Value)
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			if !owner.Role.AtLeast(min) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient role"})
				return
			}
			ctx := context.WithValue(r.Context(), ctxKeyOwner, owner)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func ownerFromContext(ctx context.Context) *ownerauth.Owner {
	o, _ := ctx.Value(ctxKeyOwner).(*ownerauth.Owner)
	return o
}

func ownerJSON(o *ownerauth.Owner) map[string]any {
	return map[string]any{
		"id":         o.ID,
		"email":      o.Email,
		"role":       string(o.Role),
		"created_at": o.CreatedAt,
	}
}

func writeOwnerAuthError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, ownerauth.ErrEmailTaken):
		status = http.StatusConflict
	case errors.Is(err, ownerauth.ErrInvalidCredentials),
		errors.Is(err, ownerauth.ErrSessionNotFound):
		status = http.StatusUnauthorized
	case errors.Is(err, ownerauth.ErrWeakPassword),
		errors.Is(err, ownerauth.ErrInvalidEmail),
		errors.Is(err, ownerauth.ErrInvalidRole):
		status = http.StatusBadRequest
	case errors.Is(err, ownerauth.ErrLastOwner):
		status = http.StatusConflict
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
