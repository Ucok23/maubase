// Package audit is maubase's audit trail: a durable, append-only record of
// security-relevant actions, both owner-plane (/admin/* — logins,
// logouts, owner-account create/delete) and customer-plane (/api/auth/* —
// signup, login, logout, password reset, social sign-in, self-service
// account deletion). Both planes share one log and one admin-only
// `/admin/audit-log` view: an incident review or compliance audit rarely
// cares which plane an action happened on, only who did what and when,
// and a customer account is exactly the kind of thing an admin is asking
// "was this compromised?" about. See spec/owner-plane.md's "Audit log"
// section for the owner-plane scenarios this backs, and
// spec/cross-cutting.md's AUDIT-CUST-01 for the customer-plane ones.
//
// Entries are deliberately independent of internal/ownerauth's and
// internal/auth's own tables: an actor or target's identity (id + email)
// is copied into the entry at write time rather than referenced by a
// foreign key, so the entry stays meaningful — including naming exactly
// who did what to whom — even after that account is later deleted.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
)

// Event names. Kept as a closed vocabulary (like internal/oauth's scopes)
// rather than free-form strings, so a typo in a caller can't silently
// create an unrecognized event.
const (
	EventLogin       = "login"
	EventLoginFailed = "login_failed"
	// EventLoginRateLimited fires when the owner-plane login throttle
	// itself rejects a request (429), before any credential is even
	// checked — distinct from EventLoginFailed, which requires a
	// credential that was actually looked up and found wanting. Without
	// this, a sustained brute-force attempt that gets throttled produces
	// zero audit trail beyond the handful of attempts that slipped in
	// under the limit, the exact scenario an incident review most wants
	// visibility into.
	EventLoginRateLimited    = "login_rate_limited"
	EventLogout              = "logout"
	EventOwnerCreate         = "owner_create"
	EventOwnerDelete         = "owner_delete"
	EventSessionsPurged      = "sessions_purged"
	EventSQLExecuted         = "sql_executed"
	EventUserCreate          = "user_create"
	EventUserDelete          = "user_delete"
	EventUserSessionsRevoked = "user_sessions_revoked"

	// Customer-plane events (spec/cross-cutting.md AUDIT-CUST-01). Named
	// distinctly from their owner-plane counterparts above (customer_login
	// vs. login) even where the shape is identical, so an admin scanning
	// the shared log can always tell which plane produced a given entry
	// without inspecting actor/target.
	EventCustomerSignup                 = "customer_signup"
	EventCustomerLogin                  = "customer_login"
	EventCustomerLoginFailed            = "customer_login_failed"
	EventCustomerLogout                 = "customer_logout"
	EventCustomerPasswordResetRequested = "customer_password_reset_requested"
	EventCustomerPasswordResetCompleted = "customer_password_reset_completed"
	EventCustomerAccountDeleted         = "customer_account_deleted"
	// EventCustomerSocialSignIn covers every outcome
	// LoginOrCreateViaSocial can produce (a new account created, an
	// existing identity signing back in, or a provider linked to the
	// caller's already-signed-in account) — distinguished by this entry's
	// metadata (`new_account`, `already_signed_in`) rather than by three
	// separate event names, since "a social sign-in happened" is the fact
	// an incident review asks for first.
	EventCustomerSocialSignIn = "customer_social_sign_in"
)

// Actor identifies who performed an action. A failed login has no known
// actor (the credentials didn't resolve to anyone), so Actor is optional —
// pass the zero value for events without one.
type Actor struct {
	ID    string
	Email string
}

// Target identifies who/what an action was performed on, if anything.
type Target struct {
	ID    string
	Email string
}

// Log is the audit trail itself, backed by the owner_audit_log table.
type Log struct {
	db *sql.DB
}

func New(db *sql.DB) *Log {
	return &Log{db: db}
}

// Record appends one entry. metadata is optional, event-specific extra
// detail (e.g. the role an account was created or deleted with); pass nil
// when there's nothing to add. A write failure here is deliberately the
// caller's problem to decide how to handle — Record itself never silently
// drops an entry.
func (l *Log) Record(ctx context.Context, event string, actor Actor, target Target, metadata map[string]any) error {
	var metaJSON []byte
	if metadata != nil {
		b, err := json.Marshal(metadata)
		if err != nil {
			return fmt.Errorf("marshal audit metadata: %w", err)
		}
		metaJSON = b
	}
	_, err := l.db.ExecContext(ctx, `
		INSERT INTO owner_audit_log (id, event, actor_id, actor_email, target_id, target_email, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, uuid.NewString(), event,
		nullIfEmpty(actor.ID), nullIfEmpty(actor.Email),
		nullIfEmpty(target.ID), nullIfEmpty(target.Email),
		metaJSON)
	if err != nil {
		return fmt.Errorf("insert audit entry: %w", err)
	}
	return nil
}

// RecordLogged is Record for the common case: a caller for whom audit
// logging is best-effort and shouldn't fail (or complicate) the request
// it's attached to. It does NOT silently drop a failure the way every
// caller of Record used to (`_ = s.audit.Record(...)`, contradicting
// Record's own documented contract above) — a write failure is logged via
// the standard log package instead, so it stays operator-visible. Prefer
// Record directly wherever a caller has something more meaningful to do
// with the error than log it (none do today, but the option stays open).
func (l *Log) RecordLogged(ctx context.Context, event string, actor Actor, target Target, metadata map[string]any) {
	if err := l.Record(ctx, event, actor, target, metadata); err != nil {
		log.Printf("audit: record %s failed: %v", event, err)
	}
}

// Entry is one row as returned by List.
type Entry struct {
	ID          string
	Event       string
	ActorID     string
	ActorEmail  string
	TargetID    string
	TargetEmail string
	Metadata    map[string]any
	CreatedAt   time.Time
}

// List returns entries newest-first, paginated.
func (l *Log) List(ctx context.Context, limit, offset int) ([]Entry, error) {
	rows, err := l.db.QueryContext(ctx, `
		SELECT id, event, actor_id, actor_email, target_id, target_email, metadata, created_at
		FROM owner_audit_log ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?
	`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list audit entries: %w", err)
	}
	defer rows.Close()

	out := []Entry{}
	for rows.Next() {
		var e Entry
		var actorID, actorEmail, targetID, targetEmail sql.NullString
		var metaJSON []byte
		if err := rows.Scan(&e.ID, &e.Event, &actorID, &actorEmail, &targetID, &targetEmail, &metaJSON, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.ActorID, e.ActorEmail = actorID.String, actorEmail.String
		e.TargetID, e.TargetEmail = targetID.String, targetEmail.String
		if len(metaJSON) > 0 {
			if err := json.Unmarshal(metaJSON, &e.Metadata); err != nil {
				return nil, fmt.Errorf("unmarshal audit metadata: %w", err)
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
