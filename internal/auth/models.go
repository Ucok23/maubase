package auth

import "time"

// SessionCookieName is shared by every surface that authenticates a human
// via the identity layer's session cookie (the plain HTTP API and the
// OAuth authorization server's login/consent screens alike), so signing in
// once on either surface signs you in on both.
const SessionCookieName = "baas_session"

type User struct {
	ID        string
	Email     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Session struct {
	ID        string
	UserID    string
	Token     string // raw token, only ever populated right after creation
	ExpiresAt time.Time
}
