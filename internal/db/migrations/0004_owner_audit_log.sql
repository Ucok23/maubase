-- Owner-plane audit log: every security-relevant action taken through
-- /admin/*, independent of the owner_users/owner_sessions tables above so
-- an entry survives deletion of the account it refers to (actor or
-- target) — see internal/audit's package doc.

CREATE TABLE IF NOT EXISTS owner_audit_log (
    id           TEXT PRIMARY KEY,
    event        TEXT NOT NULL,      -- e.g. "login", "login_failed", "logout", "owner_create", "owner_delete"

    -- Deliberately not foreign keys to owner_users: an audit entry must
    -- remain readable (including who/what it names) after the account it
    -- refers to is deleted, so actor/target identity is denormalized here
    -- rather than joined at read time.
    actor_id     TEXT,               -- owner_users.id of who performed it, "" if unauthenticated (e.g. a failed login)
    actor_email  TEXT,
    target_id    TEXT,               -- owner_users.id this event acted on, if any
    target_email TEXT,

    metadata     TEXT,               -- JSON object, event-specific (e.g. {"role":"developer"})
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_owner_audit_log_created_at ON owner_audit_log(created_at);
