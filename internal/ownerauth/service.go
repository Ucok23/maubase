package ownerauth

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

var emailRe = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

// normalizeEmail lowercases and trims email, mirroring
// internal/auth's identically-named, identically-motivated helper — see
// its doc comment. Kept as its own copy rather than shared code since
// the owner and customer planes are deliberately independent packages
// with no dependency between them (see this package's own doc comment).
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

type Service struct {
	db *sql.DB
}

func NewService(db *sql.DB) *Service {
	return &Service{db: db}
}

// Bootstrap creates the very first owner, with role owner, but only if no
// owner-plane account exists yet. This is the one place an owner account
// can be created without already being signed in as one — solving the
// chicken-and-egg problem of an admin surface nothing can yet administer.
// It's meant to be called once at startup from MAUBASE_BOOTSTRAP_OWNER_EMAIL
// / _PASSWORD, and is a safe no-op on every subsequent run.
func (s *Service) Bootstrap(ctx context.Context, email, password string) (*Owner, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM owner_users`).Scan(&count); err != nil {
		return nil, fmt.Errorf("count owners: %w", err)
	}
	if count > 0 {
		return nil, ErrAlreadyBootstrapped
	}
	return s.createOwner(ctx, email, password, RoleOwner)
}

// CreateOwner adds a new owner-plane account. Callers (HTTP handlers) are
// responsible for checking the caller has RoleOwner first — this method
// itself doesn't know who's asking, by design: it's also what Bootstrap
// uses, which by definition has no signed-in caller yet.
func (s *Service) CreateOwner(ctx context.Context, email, password string, role Role) (*Owner, error) {
	if !role.IsValid() {
		return nil, ErrInvalidRole
	}
	return s.createOwner(ctx, email, password, role)
}

func (s *Service) createOwner(ctx context.Context, email, password string, role Role) (*Owner, error) {
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
		INSERT INTO owner_users (id, email, password_hash, role) VALUES (?, ?, ?, ?)
	`, id, email, hash, string(role))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, ErrEmailTaken
		}
		return nil, fmt.Errorf("insert owner: %w", err)
	}
	return s.GetOwner(ctx, id)
}

func (s *Service) Login(ctx context.Context, email, password string) (*Session, error) {
	email = normalizeEmail(email)

	var id string
	var hash string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, password_hash FROM owner_users WHERE email = ?
	`, email).Scan(&id, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, fmt.Errorf("lookup owner: %w", err)
	}
	if !verifyPassword(hash, password) {
		return nil, ErrInvalidCredentials
	}
	return s.createSession(ctx, id)
}

func (s *Service) ValidateSession(ctx context.Context, rawToken string) (*Owner, error) {
	tokenHash := hashToken(rawToken)

	var o Owner
	var role string
	var expiresAt time.Time
	err := s.db.QueryRowContext(ctx, `
		SELECT o.id, o.email, o.role, o.created_at, o.updated_at, s.expires_at
		FROM owner_sessions s JOIN owner_users o ON o.id = s.owner_id
		WHERE s.token_hash = ?
	`, tokenHash).Scan(&o.ID, &o.Email, &role, &o.CreatedAt, &o.UpdatedAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lookup session: %w", err)
	}
	if time.Now().After(expiresAt) {
		return nil, ErrSessionNotFound
	}
	o.Role = Role(role)
	return &o, nil
}

func (s *Service) Logout(ctx context.Context, rawToken string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM owner_sessions WHERE token_hash = ?`, hashToken(rawToken))
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// PurgeExpiredSessions deletes every owner_sessions row whose expires_at
// has already passed, and reports how many were removed. See
// auth.Service.PurgeExpiredSessions's doc for why this is pure storage
// hygiene rather than user-visible behavior.
func (s *Service) PurgeExpiredSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM owner_sessions WHERE expires_at <= ?`, time.Now())
	if err != nil {
		return 0, fmt.Errorf("purge expired owner sessions: %w", err)
	}
	return res.RowsAffected()
}

func (s *Service) GetOwner(ctx context.Context, id string) (*Owner, error) {
	var o Owner
	var role string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, email, role, created_at, updated_at FROM owner_users WHERE id = ?
	`, id).Scan(&o.ID, &o.Email, &role, &o.CreatedAt, &o.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrOwnerNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lookup owner: %w", err)
	}
	o.Role = Role(role)
	return &o, nil
}

func (s *Service) ListOwners(ctx context.Context) ([]*Owner, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, email, role, created_at, updated_at FROM owner_users ORDER BY created_at
	`)
	if err != nil {
		return nil, fmt.Errorf("list owners: %w", err)
	}
	defer rows.Close()

	var out []*Owner
	for rows.Next() {
		var o Owner
		var role string
		if err := rows.Scan(&o.ID, &o.Email, &role, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		o.Role = Role(role)
		out = append(out, &o)
	}
	return out, rows.Err()
}

// DeleteOwner removes an owner-plane account, refusing if it's the last
// remaining RoleOwner — the team must always retain at least one account
// that can manage other accounts, or the deployment becomes
// unadministerable. On success, returns the account as it was just before
// deletion, so a caller (e.g. an audit log entry) can still name who was
// removed after the fact.
func (s *Service) DeleteOwner(ctx context.Context, id string) (*Owner, error) {
	// The lookup, the last-owner count, and the delete all have to run
	// inside one transaction: internal/db.Open pins SetMaxOpenConns(1),
	// so BeginTx already holds the connection exclusively until
	// Commit/Rollback — that exclusivity is what actually closes the
	// race where two concurrent deletes of two different owner accounts
	// (out of exactly two remaining) each observe ownerCount == 2, both
	// pass the "> 1" guard, and both proceed, leaving zero owners. The
	// previous version ran the count as a separate, standalone query
	// before a separate standalone DELETE, with no such exclusivity.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once Commit succeeds

	var target Owner
	var role string
	err = tx.QueryRowContext(ctx, `
		SELECT id, email, role, created_at, updated_at FROM owner_users WHERE id = ?
	`, id).Scan(&target.ID, &target.Email, &role, &target.CreatedAt, &target.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrOwnerNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lookup owner: %w", err)
	}
	target.Role = Role(role)

	if target.Role == RoleOwner {
		var ownerCount int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM owner_users WHERE role = ?
		`, string(RoleOwner)).Scan(&ownerCount); err != nil {
			return nil, fmt.Errorf("count owners: %w", err)
		}
		if ownerCount <= 1 {
			return nil, ErrLastOwner
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM owner_users WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("delete owner: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return &target, nil
}

func (s *Service) createSession(ctx context.Context, ownerID string) (*Session, error) {
	rawToken, err := randomToken(32)
	if err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}
	id := uuid.NewString()
	expiresAt := time.Now().Add(SessionTTL)

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO owner_sessions (id, owner_id, token_hash, expires_at) VALUES (?, ?, ?, ?)
	`, id, ownerID, hashToken(rawToken), expiresAt)
	if err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}
	return &Session{ID: id, OwnerID: ownerID, Token: rawToken, ExpiresAt: expiresAt}, nil
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
