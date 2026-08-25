-- OAuth 2.1 authorization server tables (Fosite-backed). This app plays
-- two roles: identity provider (users/sessions, see 0001) and, separately,
-- authorization server issuing tokens to third-party apps (MCP clients
-- included) on behalf of those identities.

CREATE TABLE IF NOT EXISTS oauth_clients (
    id                        TEXT PRIMARY KEY,
    secret_hash               BLOB,              -- NULL for public clients
    client_name               TEXT NOT NULL DEFAULT '',
    redirect_uris             TEXT NOT NULL,      -- JSON array
    grant_types               TEXT NOT NULL,      -- JSON array
    response_types            TEXT NOT NULL,      -- JSON array
    scopes                    TEXT NOT NULL,      -- JSON array
    token_endpoint_auth_method TEXT NOT NULL,
    is_public                 INTEGER NOT NULL,   -- 0/1
    created_at                TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS oauth_client_jti (
    jti        TEXT PRIMARY KEY,
    expires_at TIMESTAMP NOT NULL
);

-- Fosite calls these "sessions" (one row per issued code/token). Each holds
-- the serialized fosite.Request (client id, scopes, form, audience) plus
-- the serialized session (subject, JWT claims) needed to reconstruct or
-- re-verify the grant later.
CREATE TABLE IF NOT EXISTS oauth_authorize_codes (
    signature  TEXT PRIMARY KEY,
    request_id TEXT NOT NULL,
    client_id  TEXT NOT NULL REFERENCES oauth_clients(id) ON DELETE CASCADE,
    requester  BLOB NOT NULL,
    session    BLOB NOT NULL,
    active     INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS oauth_access_tokens (
    signature  TEXT PRIMARY KEY,
    request_id TEXT NOT NULL,
    client_id  TEXT NOT NULL REFERENCES oauth_clients(id) ON DELETE CASCADE,
    requester  BLOB NOT NULL,
    session    BLOB NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_oauth_access_tokens_request_id ON oauth_access_tokens(request_id);

CREATE TABLE IF NOT EXISTS oauth_refresh_tokens (
    signature  TEXT PRIMARY KEY,
    request_id TEXT NOT NULL,
    client_id  TEXT NOT NULL REFERENCES oauth_clients(id) ON DELETE CASCADE,
    requester  BLOB NOT NULL,
    session    BLOB NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_oauth_refresh_tokens_request_id ON oauth_refresh_tokens(request_id);

CREATE TABLE IF NOT EXISTS oauth_pkce_requests (
    signature  TEXT PRIMARY KEY,
    request_id TEXT NOT NULL,
    client_id  TEXT NOT NULL REFERENCES oauth_clients(id) ON DELETE CASCADE,
    requester  BLOB NOT NULL,
    session    BLOB NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Remembers which scopes a user has already approved for a client, so
-- repeat authorizations can skip the consent screen.
CREATE TABLE IF NOT EXISTS oauth_consents (
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    client_id  TEXT NOT NULL REFERENCES oauth_clients(id) ON DELETE CASCADE,
    scopes     TEXT NOT NULL, -- JSON array
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, client_id)
);

-- Persists the authorization server's JWT signing keypair (RSA, PKCS#8 DER)
-- so it survives restarts. "current" is the active signing key; older rows
-- are kept and published in the JWKS for verifying already-issued tokens
-- until they're pruned by `maubase keys rotate --prune`.
CREATE TABLE IF NOT EXISTS oauth_signing_keys (
    kid         TEXT PRIMARY KEY,
    private_der BLOB NOT NULL,
    is_current  INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- The HMAC secret used to sign authorize codes and refresh tokens (access
-- tokens are signed with oauth_signing_keys instead, since those are JWTs).
-- Single row, generated on first run.
CREATE TABLE IF NOT EXISTS oauth_hmac_secret (
    id     INTEGER PRIMARY KEY CHECK (id = 1),
    secret BLOB NOT NULL
);
