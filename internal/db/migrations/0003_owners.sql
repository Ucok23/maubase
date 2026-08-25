-- Owner plane: the team running this backend, entirely separate from the
-- customer plane (0001_init.sql's users/sessions). Deliberately no shared
-- tables, no shared session cookie, no path an owner account could be
-- reached through the OAuth authorization server (0002_oauth.sql) — an
-- owner is never something a third-party app gets granted.

CREATE TABLE IF NOT EXISTS owner_users (
    id            TEXT PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    -- Fixed, linearly-ranked roles (owner > admin > developer > viewer),
    -- not general RBAC: see internal/ownerauth.Role. Right-sized for one
    -- small team running one deployment, not a permissions engine.
    role          TEXT NOT NULL,
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS owner_sessions (
    id         TEXT PRIMARY KEY,
    owner_id   TEXT NOT NULL REFERENCES owner_users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMP NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_owner_sessions_owner_id ON owner_sessions(owner_id);
CREATE INDEX IF NOT EXISTS idx_owner_sessions_token_hash ON owner_sessions(token_hash);
