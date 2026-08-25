package ownerauth

import "errors"

var (
	ErrEmailTaken          = errors.New("email already registered")
	ErrInvalidCredentials  = errors.New("invalid email or password")
	ErrSessionNotFound     = errors.New("session not found or expired")
	ErrWeakPassword        = errors.New("password must be at least 8 characters")
	ErrInvalidEmail        = errors.New("invalid email address")
	ErrInvalidRole         = errors.New("invalid role")
	ErrLastOwner           = errors.New("cannot remove or demote the last remaining owner")
	ErrAlreadyBootstrapped = errors.New("an owner already exists; bootstrap is a no-op")
)
