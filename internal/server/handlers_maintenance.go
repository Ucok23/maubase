package server

import (
	"net/http"

	"baas/internal/audit"
	"baas/internal/ownerauth"
)

// handlePurgeSessions deletes every expired row from the customer-plane
// sessions table and the owner-plane owner_sessions table, and reports
// how many were removed. A background janitor (see cmd/baas) already does
// this periodically; this endpoint exists for an operator to trigger it
// on demand and see the result. Requires admin+ — see
// spec/maintenance.md MAINT-01.
func (s *Server) handlePurgeSessions(w http.ResponseWriter, r *http.Request) {
	owner, _ := r.Context().Value(ctxKeyOwner).(*ownerauth.Owner)

	sessionsPurged, err := s.auth.PurgeExpiredSessions(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	ownerSessionsPurged, err := s.ownerAuth.PurgeExpiredSessions(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	var actor audit.Actor
	if owner != nil {
		actor = audit.Actor{ID: owner.ID, Email: owner.Email}
	}
	_ = s.audit.Record(r.Context(), audit.EventSessionsPurged, actor, audit.Target{}, map[string]any{
		"sessions":       sessionsPurged,
		"owner_sessions": ownerSessionsPurged,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"sessions_purged":       sessionsPurged,
		"owner_sessions_purged": ownerSessionsPurged,
	})
}
