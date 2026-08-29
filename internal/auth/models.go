package auth

import "time"

// SessionCookieName is shared by every surface that authenticates a human
// via the identity layer's session cookie (the plain HTTP API and the
// OAuth authorization server's login/consent screens alike), so signing in
// once on either surface signs you in on both.
const SessionCookieName = "maubase_session"

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

// SocialIdentity is one linked social-login provider identity for a user
// (spec/social-login.md SOCIAL-09) — an alternate way into the account,
// which is exactly why the admin UI's user-detail page surfaces it
// (spec/admin-ui.md ADMINUI-26).
type SocialIdentity struct {
	Provider  string
	Email     string // as reported by the provider; may be empty
	CreatedAt time.Time
}
