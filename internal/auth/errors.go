package auth

import "errors"

var (
	ErrEmailTaken         = errors.New("email already registered")
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrSessionNotFound    = errors.New("session not found or expired")
	ErrWeakPassword       = errors.New("password must be at least 8 characters")
	ErrInvalidEmail       = errors.New("invalid email address")
	ErrResetTokenInvalid  = errors.New("invalid or expired reset token")
	// ErrSocialIdentityLinkedElsewhere is returned by LoginOrCreateViaSocial
	// when the caller is already signed in as one account and the social
	// identity resolving the callback is already linked to a *different*
	// one — see that function's doc comment for why this is refused
	// rather than silently switching the session.
	ErrSocialIdentityLinkedElsewhere = errors.New("this provider account is already linked to a different account")
)
