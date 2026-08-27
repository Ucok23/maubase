-- Mirrors oauth_authorize_codes' active column: rotating a refresh token
-- now marks the old row inactive instead of hard-deleting it, so a stolen
-- refresh token replayed after rotation can be told apart from one that
-- never existed at all (ErrInactiveToken vs. ErrNotFound) — see
-- internal/oauth/storage.go's RotateRefreshToken/GetRefreshTokenSession.
-- That distinction is what lets Fosite's reuse-detection path run instead
-- of silently no-op'ing on a plain "not found".
ALTER TABLE oauth_refresh_tokens ADD COLUMN active INTEGER NOT NULL DEFAULT 1;
