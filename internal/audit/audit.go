// Package audit is the owner plane's audit trail: a durable, append-only
// record of security-relevant actions taken through /admin/* — logins
// (successful and failed), logouts, and owner-account create/delete.
//
// Entries are deliberately independent of internal/ownerauth's tables: an
// actor or target's identity (id + email) is copied into the entry at
// write time rather than referenced by a foreign key, so the entry stays
// meaningful — including naming exactly who did what to whom — even after
// that account is later deleted. See spec/owner-plane.md's "Audit log"
// section for the exact scenarios this backs.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Event names. Kept as a closed vocabulary (like internal/oauth's scopes)
// rather than free-form strings, so a typo in a caller can't silently
// create an unrecognized event.
const (
	EventLogin          = "login"
	EventLoginFailed    = "login_failed"
	EventLogout         = "logout"
	EventOwnerCreate    = "owner_create"
	EventOwnerDelete    = "owner_delete"
	EventSessionsPurged = "sessions_purged"
	EventSQLExecuted    = "sql_executed"
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
