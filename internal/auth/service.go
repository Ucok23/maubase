// Package auth is the identity layer: it answers "who is this human?" via
// email/password signup, login, and cookie-backed sessions. It is
// deliberately separate from (and sits underneath) the OAuth authorization
// server layer added later — the authorization server issues tokens to
// third-party apps on behalf of the identities this package manages.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SessionTTL is how long an issued session cookie stays valid.
const SessionTTL = 30 * 24 * time.Hour

// ResetTokenTTL is how long a password-reset token stays redeemable
// after CreateResetToken issues it.
const ResetTokenTTL = time.Hour

var emailRe = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

type Service struct {
	db *sql.DB
}

func NewService(db *sql.DB) *Service {
	return &Service{db: db}
}

// SignUp creates a new user. It does not log the user in; call Login
// afterward (or fold the two together at the HTTP handler level).
func (s *Service) SignUp(ctx context.Context, email, password string) (*User, error) {
	email = normalizeEmail(email)
	if !emailRe.MatchString(email) {
		return nil, ErrInvalidEmail
	}
	if len(password) < 8 {
		return nil, ErrWeakPassword
	}

	hash, err := hashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	id := uuid.NewString()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO users (id, email, password_hash) VALUES (?, ?, ?)
	`, id, email, hash)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return nil, ErrEmailTaken
		}
		return nil, fmt.Errorf("insert user: %w", err)
	}

	return s.getUserByID(ctx, id)
}

// Login verifies credentials and issues a new session. The returned
// Session.Token is the raw, only-ever-shown-once token to set as a cookie
// or return to an API client; only its hash is persisted.
func (s *Service) Login(ctx context.Context, email, password string) (*Session, error) {
	email = normalizeEmail(email)

	var u User
	var hash string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, email, password_hash, created_at, updated_at
		FROM users WHERE email = ?
	`, email).Scan(&u.ID, &u.Email, &hash, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, fmt.Errorf("lookup user: %w", err)
	}
	if !verifyPassword(hash, password) {
		return nil, ErrInvalidCredentials
	}

	return s.createSession(ctx, u.ID)
}

// ValidateSession resolves a raw session token (as received from a cookie
// or Authorization header) to the User it belongs to.
func (s *Service) ValidateSession(ctx context.Context, rawToken string) (*User, error) {
	tokenHash := hashToken(rawToken)

	var u User
	var expiresAt time.Time
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.email, u.created_at, u.updated_at, s.expires_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ?
	`, tokenHash).Scan(&u.ID, &u.Email, &u.CreatedAt, &u.UpdatedAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lookup session: %w", err)
	}
	if time.Now().After(expiresAt) {
		return nil, ErrSessionNotFound
	}
	return &u, nil
}

// Logout revokes a single session by its raw token.
func (s *Service) Logout(ctx context.Context, rawToken string) error {
	tokenHash := hashToken(rawToken)
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteUser permanently removes an account: the user row itself, and —
// via ON DELETE CASCADE — its sessions and OAuth consents. It does not
// touch already-issued OAuth access/refresh tokens for third-party
// clients, since those aren't keyed by user id in the schema; see the
// README's "Known limitation" note on this.
//
// This only deletes identity-layer state. A caller wanting full account
// erasure (e.g. the /api/auth/me DELETE handler) is also responsible for
// clearing the user's rows out of any owner-scoped auto-REST collections
// first — that's application data this package doesn't know about.
func (s *Service) DeleteUser(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return nil
}

// ListUsers returns customer accounts newest-first, paginated. Backs the
// embedded admin UI's Users panel (internal/adminui, spec/admin-ui.md
// ADMINUI-25) — never exposed on the customer-facing API.
func (s *Service) ListUsers(ctx context.Context, limit, offset int) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, email, created_at, updated_at FROM users
		ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?
	`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	out := []*User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &u)
	}
	return out, rows.Err()
}

// CountUsers returns the total number of customer accounts, for pagination.
func (s *Service) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

// GetUser fetches one customer account by id — exported for the admin UI's
// user-detail page (getUserByID stays unexported, used internally by the
// signup/login paths).
func (s *Service) GetUser(ctx context.Context, id string) (*User, error) {
	return s.getUserByID(ctx, id)
}

// CountActiveSessions reports how many non-expired sessions userID
// currently holds — shown on the admin UI's user-detail page (ADMINUI-26).
func (s *Service) CountActiveSessions(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sessions WHERE user_id = ? AND expires_at > ?
	`, userID, time.Now()).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count active sessions: %w", err)
	}
	return n, nil
}

// RevokeAllSessions deletes every session belonging to userID — "sign out
// everywhere" for one customer account, without deleting the account
// itself (spec/admin-ui.md ADMINUI-29). Returns how many were revoked.
func (s *Service) RevokeAllSessions(ctx context.Context, userID string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	if err != nil {
		return 0, fmt.Errorf("revoke sessions: %w", err)
	}
	return res.RowsAffected()
}

// PurgeExpiredSessions deletes every session row whose expires_at has
// already passed, and reports how many were removed. Expired sessions
// already fail ValidateSession, so this is purely storage hygiene (a
// long-lived deployment would otherwise accumulate one dead row per
// login forever) — not something that changes any externally observable
// behavior. Safe to call from a periodic background job or on demand.
func (s *Service) PurgeExpiredSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, time.Now())
	if err != nil {
		return 0, fmt.Errorf("purge expired sessions: %w", err)
	}
	return res.RowsAffected()
}

// PurgeExpiredResetTokens deletes every password_reset_tokens row that's
// either already expired or already redeemed, and reports how many were
// removed. Neither kind ever redeems again (ResetPassword itself rejects
// both — spec/password-reset.md PWRESET-05/06), so like
// PurgeExpiredSessions this is pure storage hygiene, not something that
// changes any externally observable behavior — but unlike sessions,
// nothing deleted this table at all before: every forgot-password
// request left a permanent row, holding a user_id and a token hash
// forever on a long-lived deployment.
func (s *Service) PurgeExpiredResetTokens(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM password_reset_tokens WHERE expires_at <= ? OR used_at IS NOT NULL`, time.Now())
	if err != nil {
		return 0, fmt.Errorf("purge expired reset tokens: %w", err)
	}
	return res.RowsAffected()
}

// CreateResetToken issues a password-reset token for the account with
// the given email, if one exists. ok is false (with no error) when it
// doesn't — a normal, expected outcome the HTTP handler uses to decide
// whether to actually send an email, while still returning the exact
// same response to the caller either way. Never revealing whether an
// email is registered is the whole point (see spec/password-reset.md
// PWRESET-02) — a caller that special-cased "no such account" into a
// different response would defeat that regardless of what this method
// itself does right.
//
// The returned raw token is only ever available here; only its hash is
// persisted, the same pattern createSession already uses for session
// tokens.
func (s *Service) CreateResetToken(ctx context.Context, email string) (rawToken, userID string, ok bool, err error) {
	email = normalizeEmail(email)
	err = s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, email).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("lookup user: %w", err)
	}

	raw, err := randomToken(32)
	if err != nil {
		return "", "", false, fmt.Errorf("generate token: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO password_reset_tokens (id, user_id, token_hash, expires_at) VALUES (?, ?, ?, ?)
	`, uuid.NewString(), userID, hashToken(raw), time.Now().Add(ResetTokenTTL))
	if err != nil {
		return "", "", false, fmt.Errorf("insert reset token: %w", err)
	}
	return raw, userID, true, nil
}

// ResetPassword redeems a raw reset token: rejects it (ErrResetTokenInvalid)
// if it doesn't exist, has expired, or has already been redeemed once
// (single-use — PWRESET-05), otherwise sets the new password, marks the
// token used, and revokes every session the account currently has. That
// last part isn't optional: a password reset is exactly the moment you
// want anyone already signed in — including, especially, whoever's
// access prompted this reset in the first place — signed out
// everywhere, the same guarantee ownerauth's forced-reauth-adjacent
// flows already lean on elsewhere in this codebase.
//
// Returns the account's user id on success so the caller can also revoke
// OAuth grants (internal/oauth.Server.RevokeAllForSubject) — that's the
// caller's responsibility, not this method's, since internal/auth has no
// dependency on internal/oauth, matching how account erasure already
// splits the same two concerns across internal/server's handler.
func (s *Service) ResetPassword(ctx context.Context, rawToken, newPassword string) (userID string, err error) {
	if len(newPassword) < 8 {
		return "", ErrWeakPassword
	}
	tokenHash := hashToken(rawToken)

	hash, err := hashPassword(newPassword)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once Commit succeeds

	// The lookup-then-check-then-write sequence below has to happen
	// inside this transaction, not before it: internal/db.Open pins
	// SetMaxOpenConns(1), so BeginTx already holds the connection
	// exclusively until Commit/Rollback — reading the token's
	// used_at/expires_at here (rather than in a standalone query before
	// BeginTx, as this used to) is what actually closes the race where
	// two concurrent redemptions of the same token both pass the check
	// before either commits its write. See PWRESET-05, PWRESET-10.
	var id string
	var expiresAt time.Time
	var usedAt sql.NullTime
	err = tx.QueryRowContext(ctx, `
		SELECT id, user_id, expires_at, used_at FROM password_reset_tokens WHERE token_hash = ?
	`, tokenHash).Scan(&id, &userID, &expiresAt, &usedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrResetTokenInvalid
	}
	if err != nil {
		return "", fmt.Errorf("lookup reset token: %w", err)
	}
	if usedAt.Valid || time.Now().After(expiresAt) {
		return "", ErrResetTokenInvalid
	}

	if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, hash, userID); err != nil {
		return "", fmt.Errorf("update password: %w", err)
	}
	// Marks every outstanding token for this user as used, not just the
	// one just redeemed: a second forgot-password request before the
	// first is ever used leaves two valid tokens outstanding, and an old
	// one resurfacing later (a mail archive, a previously-compromised-
	// then-resecured inbox) could otherwise still reset the password
	// within its own hour-long window, even after the account's owner
	// believes it's fully re-secured. See PWRESET-12.
	if _, err := tx.ExecContext(ctx, `
		UPDATE password_reset_tokens SET used_at = CURRENT_TIMESTAMP WHERE user_id = ? AND used_at IS NULL
	`, userID); err != nil {
		return "", fmt.Errorf("mark reset tokens used: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return "", fmt.Errorf("revoke sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return userID, nil
}

// LoginOrCreateViaSocial resolves a successful "Continue with <provider>"
// round trip (internal/social) to a session. currentUserID is the caller's
// already-signed-in identity-layer session, if any ("" if anonymous) —
// this is what decides which of two entirely different resolution
// strategies applies:
//
// Anonymous (currentUserID == ""), the original three-way resolution:
//  1. This exact (provider, providerUserID) pair has signed in before —
//     just start a new session for the user it's already linked to.
//  2. It hasn't, but email matches an existing account (however that
//     account originally signed up — password or a different provider)
//     — link this identity to it. This is the common "you already have
//     an account with that email" case, and avoids ending up with two
//     separate accounts for what's obviously the same person.
//  3. Neither — create a brand new account and link this identity to
//     it. It gets a random, never-revealed password (hashed, to satisfy
//     users.password_hash's NOT NULL — nobody can ever type it, so the
//     account is only reachable via this provider until/unless its
//     owner uses password reset to set a real one) and, if the provider
//     didn't supply a usable email at all (GitHub with no verified
//     public address), a synthetic placeholder that still satisfies the
//     UNIQUE constraint.
//
// Already signed in (currentUserID != ""): this is a "link a second
// sign-in method to my account" request, not an independent identity
// resolution, so email-matching never enters into it at all:
//  1. This (provider, providerUserID) pair is already linked to the
//     *current* account — no-op, just continue that session.
//  2. It's already linked to a *different* account —
//     ErrSocialIdentityLinkedElsewhere, not a silent switch. Before this,
//     completing a social flow while signed in as A, that resolved to an
//     unrelated account B, unconditionally overwrote the session cookie
//     with B's — a curious click while signed in could silently switch
//     the browser to someone else's account with no warning, and there
//     was no way to add a second sign-in method to an existing account
//     at all (a never-before-seen identity always resolved independently,
//     by email or by creating a new account, never by linking to whatever
//     session was already active).
//  3. It isn't linked to anyone — link it to the *current* account
//     directly. This is what actually enables "add Google as a second
//     sign-in method": the currently-authenticated identity is
//     authoritative, so a matching email on some other unrelated account
//     is irrelevant here (unlike the anonymous path's case 2).
func (s *Service) LoginOrCreateViaSocial(ctx context.Context, provider, providerUserID, email, currentUserID string) (*Session, error) {
	var linkedUserID string
	err := s.db.QueryRowContext(ctx, `
		SELECT user_id FROM social_identities WHERE provider = ? AND provider_user_id = ?
	`, provider, providerUserID).Scan(&linkedUserID)
	switch {
	case err == nil:
		if currentUserID != "" && linkedUserID != currentUserID {
			return nil, ErrSocialIdentityLinkedElsewhere
		}
		return s.createSession(ctx, linkedUserID)
	case !errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("lookup social identity: %w", err)
	}

	// Not linked to anyone yet.
	var userID string
	email = normalizeEmail(email)
	if currentUserID != "" {
		userID = currentUserID
	} else {
		if email != "" {
			err = s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, email).Scan(&userID)
		} else {
			err = sql.ErrNoRows
		}
		switch {
		case errors.Is(err, sql.ErrNoRows):
			userID, email, err = s.createUserForSocialSignIn(ctx, provider, providerUserID, email)
			if err != nil {
				return nil, err
			}
		case err != nil:
			return nil, fmt.Errorf("lookup user by email: %w", err)
		}
	}

	// email is never "" here when currentUserID == "": either the lookup
	// above only ran because it already wasn't, or createUserForSocialSignIn
	// resolved it to a real or placeholder address. When currentUserID !=
	// "", an empty email is stored as-is — it's just descriptive metadata
	// on this link, not a lookup key, so there's no placeholder to invent.
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO social_identities (id, user_id, provider, provider_user_id, email) VALUES (?, ?, ?, ?, ?)
	`, uuid.NewString(), userID, provider, providerUserID, email); err != nil {
		return nil, fmt.Errorf("link social identity: %w", err)
	}
	return s.createSession(ctx, userID)
}

// createUserForSocialSignIn handles LoginOrCreateViaSocial's case 3.
// Split out mainly to keep the unique-email race (two requests for a
// brand-new account landing here at once — unlikely, but a provider
// callback is exactly the kind of double-fired request a flaky network
// produces) contained to one place: losing that race just means the
// winner's row is the one to link to, not a real error.
func (s *Service) createUserForSocialSignIn(ctx context.Context, provider, providerUserID, email string) (userID, resolvedEmail string, err error) {
	if email == "" {
		// A guaranteed-unique placeholder — this account is only ever
		// reachable via this provider until its owner sets a real email
		// (not yet exposed) or a password via reset (which does work:
		// forgot-password's "always 204, don't reveal whether it's
		// real" behavior extends naturally to a synthetic address too).
		email = fmt.Sprintf("%s-%s@users.noreply.%s.invalid", provider, providerUserID, provider)
	}

	randPassword, err := randomToken(32)
	if err != nil {
		return "", "", fmt.Errorf("generate placeholder password: %w", err)
	}
	hash, err := hashPassword(randPassword)
	if err != nil {
		return "", "", fmt.Errorf("hash placeholder password: %w", err)
	}

	userID = uuid.NewString()
	_, err = s.db.ExecContext(ctx, `INSERT INTO users (id, email, password_hash) VALUES (?, ?, ?)`, userID, email, hash)
	if err == nil {
		return userID, email, nil
	}
	if !isUniqueConstraintErr(err) {
		return "", "", fmt.Errorf("insert user: %w", err)
	}
	// Lost a race against a concurrent sign-up/sign-in for the same
	// email — link to whoever won it instead of erroring.
	if lookupErr := s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, email).Scan(&userID); lookupErr != nil {
		return "", "", fmt.Errorf("lookup user after insert race: %w", lookupErr)
	}
	return userID, email, nil
}

func (s *Service) createSession(ctx context.Context, userID string) (*Session, error) {
	rawToken, err := randomToken(32)
	if err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}
	id := uuid.NewString()
	expiresAt := time.Now().Add(SessionTTL)

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO sessions (id, user_id, token_hash, expires_at) VALUES (?, ?, ?, ?)
	`, id, userID, hashToken(rawToken), expiresAt)
	if err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}

	return &Session{ID: id, UserID: userID, Token: rawToken, ExpiresAt: expiresAt}, nil
}

func (s *Service) getUserByID(ctx context.Context, id string) (*User, error) {
	var u User
	err := s.db.QueryRowContext(ctx, `
		SELECT id, email, created_at, updated_at FROM users WHERE id = ?
	`, id).Scan(&u.ID, &u.Email, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("lookup user: %w", err)
	}
	return &u, nil
}

// normalizeEmail lowercases and trims email so "User@Example.com" and
// "user@example.com " are treated as the same address everywhere: signup
// uniqueness, login lookup, reset-token lookup, and social-login account
// linking (a provider reporting a differently-cased email than the one a
// password account originally signed up with must still resolve to the
// same account — see spec/identity.md IDNT-14). Full RFC 5321 casing
// rules (a mailbox's local-part is technically case-sensitive per spec,
// though virtually no real provider treats it that way) aren't worth
// implementing here since this only needs consistent uniqueness, not
// mailbox routing. See migrations/0010_case_insensitive_email.sql for the
// matching schema-level defense in depth.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func isUniqueConstraintErr(err error) bool {
	// modernc.org/sqlite wraps the sqlite3 error string; matching on it is
	// brittle but there's no typed sentinel exposed. Good enough for v1 —
	// revisit if it causes false negatives in practice.
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
