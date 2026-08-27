package oauth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/ory/fosite"
	fosOAuth2 "github.com/ory/fosite/handler/oauth2"
	"github.com/ory/fosite/handler/pkce"
)

// Storage backs Fosite's ClientManager, CoreStorage (authorize code, access
// token, refresh token), TokenRevocationStorage and PKCERequestStorage
// interfaces with SQLite tables (see migrations/0002_oauth.sql).
//
// Every "session" Fosite asks us to persist is really two JSON blobs: the
// fosite.Requester (client id, scopes, form, audience) and the
// fosite.Session (subject, JWT claims/header) that was in effect when the
// code/token was issued. On read, the session blob is unmarshaled directly
// into the *session parameter Fosite passes us — that parameter is always a
// pointer to the concrete session type the caller configured (here,
// *oauth2.JWTSession), so a plain json.Unmarshal round-trips it correctly.
type Storage struct {
	db *sql.DB
}

func NewStorage(db *sql.DB) *Storage {
	return &Storage{db: db}
}

var (
	_ fosite.ClientManager             = (*Storage)(nil)
	_ fosOAuth2.CoreStorage            = (*Storage)(nil)
	_ fosOAuth2.TokenRevocationStorage = (*Storage)(nil)
	_ pkce.PKCERequestStorage          = (*Storage)(nil)
)

// --- fosite.ClientManager -----------------------------------------------

func (s *Storage) GetClient(ctx context.Context, id string) (fosite.Client, error) {
	var c dbClient
	var redirectURIs, grantTypes, responseTypes, scopes string
	var secretHash []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT id, secret_hash, redirect_uris, grant_types, response_types, scopes, is_public
		FROM oauth_clients WHERE id = ?
	`, id).Scan(&c.ID, &secretHash, &redirectURIs, &grantTypes, &responseTypes, &scopes, &c.Public)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fosite.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lookup oauth client %s: %w", id, err)
	}
	c.Secret = secretHash
	if err := json.Unmarshal([]byte(redirectURIs), &c.RedirectURIs); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(grantTypes), &c.GrantTypes); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(responseTypes), &c.ResponseTypes); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(scopes), &c.Scopes); err != nil {
		return nil, err
	}
	return c.toFosite(), nil
}

// ClientAssertionJWTValid / SetClientAssertionJWT back the private_key_jwt
// client authentication method (RFC 7523). We don't offer that auth method
// to registered clients today (see register.go), so these are exercised
// only if that changes later; implemented for real against a table now so
// nothing needs revisiting when it does.
func (s *Storage) ClientAssertionJWTValid(ctx context.Context, jti string) error {
	var expiresAt time.Time
	err := s.db.QueryRowContext(ctx, `SELECT expires_at FROM oauth_client_jti WHERE jti = ?`, jti).Scan(&expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // not known => valid (not replayed)
	}
	if err != nil {
		return err
	}
	if time.Now().After(expiresAt) {
		return nil // expired entries can't be replayed
	}
	return fosite.ErrJTIKnown
}

func (s *Storage) SetClientAssertionJWT(ctx context.Context, jti string, exp time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM oauth_client_jti WHERE expires_at < ?`, time.Now()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO oauth_client_jti (jti, expires_at) VALUES (?, ?)`, jti, exp)
	return err
}

// --- authorize codes -----------------------------------------------------

func (s *Storage) CreateAuthorizeCodeSession(ctx context.Context, code string, r fosite.Requester) error {
	return s.insert(ctx, "oauth_authorize_codes", code, r)
}

func (s *Storage) GetAuthorizeCodeSession(ctx context.Context, code string, session fosite.Session) (fosite.Requester, error) {
	var clientID string
	var reqBytes, sessBytes []byte
	var active bool
	err := s.db.QueryRowContext(ctx, `
		SELECT client_id, requester, session, active FROM oauth_authorize_codes WHERE signature = ?
	`, code).Scan(&clientID, &reqBytes, &sessBytes, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fosite.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	requester, err := s.hydrate(ctx, clientID, reqBytes, sessBytes, session)
	if err != nil {
		return nil, err
	}
	if !active {
		return requester, fosite.ErrInvalidatedAuthorizeCode
	}
	return requester, nil
}

func (s *Storage) InvalidateAuthorizeCodeSession(ctx context.Context, code string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE oauth_authorize_codes SET active = 0 WHERE signature = ?`, code)
	return err
}

// --- access tokens ---------------------------------------------------------

func (s *Storage) CreateAccessTokenSession(ctx context.Context, signature string, r fosite.Requester) error {
	return s.insert(ctx, "oauth_access_tokens", signature, r)
}

func (s *Storage) GetAccessTokenSession(ctx context.Context, signature string, session fosite.Session) (fosite.Requester, error) {
	return s.getByTable(ctx, "oauth_access_tokens", signature, session)
}

func (s *Storage) DeleteAccessTokenSession(ctx context.Context, signature string) error {
	return s.deleteByTable(ctx, "oauth_access_tokens", "signature", signature)
}

// --- refresh tokens ----------------------------------------------------

func (s *Storage) CreateRefreshTokenSession(ctx context.Context, signature string, _ string, r fosite.Requester) error {
	return s.insert(ctx, "oauth_refresh_tokens", signature, r)
}

// GetRefreshTokenSession distinguishes a token that never existed
// (fosite.ErrNotFound) from one that existed but was already rotated away
// (fosite.ErrInactiveToken, alongside the hydrated requester) — the same
// active-flag pattern GetAuthorizeCodeSession uses. That distinction is
// what lets Fosite's refresh-grant handler recognize token reuse (a
// rotated-away refresh token being replayed, e.g. because it was stolen)
// and respond by revoking the entire grant chain, instead of the reuse
// looking identical to an unrelated bad request.
func (s *Storage) GetRefreshTokenSession(ctx context.Context, signature string, session fosite.Session) (fosite.Requester, error) {
	var clientID string
	var reqBytes, sessBytes []byte
	var active bool
	err := s.db.QueryRowContext(ctx, `
		SELECT client_id, requester, session, active FROM oauth_refresh_tokens WHERE signature = ?
	`, signature).Scan(&clientID, &reqBytes, &sessBytes, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fosite.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	requester, err := s.hydrate(ctx, clientID, reqBytes, sessBytes, session)
	if err != nil {
		return nil, err
	}
	if !active {
		return requester, fosite.ErrInactiveToken
	}
	return requester, nil
}

func (s *Storage) DeleteRefreshTokenSession(ctx context.Context, signature string) error {
	return s.deleteByTable(ctx, "oauth_refresh_tokens", "signature", signature)
}

// RotateRefreshToken marks the previous refresh token inactive (not
// deleted — see GetRefreshTokenSession) when a client redeems it for a new
// access/refresh token pair, so a stolen-but-already-used refresh token
// replayed later is recognized as reuse rather than merely "not found".
func (s *Storage) RotateRefreshToken(ctx context.Context, requestID string, refreshTokenSignature string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE oauth_refresh_tokens SET active = 0 WHERE signature = ? AND request_id = ?
	`, refreshTokenSignature, requestID)
	return err
}

// --- revocation (RFC 7009) ----------------------------------------------

func (s *Storage) RevokeRefreshToken(ctx context.Context, requestID string) error {
	return s.deleteByTable(ctx, "oauth_refresh_tokens", "request_id", requestID)
}

func (s *Storage) RevokeAccessToken(ctx context.Context, requestID string) error {
	return s.deleteByTable(ctx, "oauth_access_tokens", "request_id", requestID)
}

// --- PKCE ----------------------------------------------------------------

func (s *Storage) CreatePKCERequestSession(ctx context.Context, signature string, r fosite.Requester) error {
	return s.insert(ctx, "oauth_pkce_requests", signature, r)
}

func (s *Storage) GetPKCERequestSession(ctx context.Context, signature string, session fosite.Session) (fosite.Requester, error) {
	return s.getByTable(ctx, "oauth_pkce_requests", signature, session)
}

func (s *Storage) DeletePKCERequestSession(ctx context.Context, signature string) error {
	return s.deleteByTable(ctx, "oauth_pkce_requests", "signature", signature)
}

// --- account erasure -------------------------------------------------------

// oauthGrantTables are every table that can hold a live grant tied to a
// user, i.e. everything with a session BLOB carrying a Subject — not
// oauth_consents (revoked via ON DELETE CASCADE on the users FK instead,
// see migrations/0002_oauth.sql) and not oauth_clients (clients aren't
// owned by a customer user at all).
var oauthGrantTables = []string{
	"oauth_authorize_codes", "oauth_access_tokens", "oauth_refresh_tokens", "oauth_pkce_requests",
}

// RevokeForSubject deletes every outstanding authorize code, access token,
// refresh token, and PKCE request whose session belongs to subject. Used
// when an account is deleted, so a deleted user's already-issued grants to
// third-party clients stop working immediately rather than lingering until
// they naturally expire.
//
// The session BLOB is the only place a subject lives in these tables (see
// storedRequest's doc comment above: the user id is inside the serialized
// fosite.Session, not a queryable column), so this matches on it via
// SQLite's JSON1 extension rather than an indexed column. Fine at the
// account-deletion call rate this exists for; revisit if it ever needs a
// hot path.
func (s *Storage) RevokeForSubject(ctx context.Context, subject string) error {
	for _, table := range oauthGrantTables {
		_, err := s.db.ExecContext(ctx, fmt.Sprintf(
			`DELETE FROM %s WHERE json_extract(session, '$.Subject') = ?`, table,
		), subject)
		if err != nil {
			return fmt.Errorf("revoke %s for subject: %w", table, err)
		}
	}
	return nil
}

// --- shared marshal/unmarshal helpers -------------------------------------

// storedRequest is the JSON-serializable subset of fosite.Requester. The
// client is stored by ID (its own column) and re-resolved via GetClient on
// read, rather than serialized, since clients are mutable and we always
// want the current client record, not a snapshot.
type storedRequest struct {
	ID                string           `json:"id"`
	RequestedAt       time.Time        `json:"requested_at"`
	RequestedScope    fosite.Arguments `json:"requested_scope"`
	GrantedScope      fosite.Arguments `json:"granted_scope"`
	Form              url.Values       `json:"form"`
	RequestedAudience fosite.Arguments `json:"requested_audience"`
	GrantedAudience   fosite.Arguments `json:"granted_audience"`
}

const insertSQL = `INSERT INTO %s (signature, request_id, client_id, requester, session) VALUES (?, ?, ?, ?, ?)`

func (s *Storage) insert(ctx context.Context, table, signature string, r fosite.Requester) error {
	reqBytes, err := json.Marshal(storedRequest{
		ID:                r.GetID(),
		RequestedAt:       r.GetRequestedAt(),
		RequestedScope:    r.GetRequestedScopes(),
		GrantedScope:      r.GetGrantedScopes(),
		Form:              r.GetRequestForm(),
		RequestedAudience: r.GetRequestedAudience(),
		GrantedAudience:   r.GetGrantedAudience(),
	})
	if err != nil {
		return fmt.Errorf("marshal requester: %w", err)
	}
	sessBytes, err := json.Marshal(r.GetSession())
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}
	_, err = s.db.ExecContext(ctx, fmt.Sprintf(insertSQL, table),
		signature, r.GetID(), r.GetClient().GetID(), reqBytes, sessBytes)
	if err != nil {
		return fmt.Errorf("insert into %s: %w", table, err)
	}
	return nil
}

func (s *Storage) getByTable(ctx context.Context, table, signature string, session fosite.Session) (fosite.Requester, error) {
	var clientID string
	var reqBytes, sessBytes []byte
	err := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT client_id, requester, session FROM %s WHERE signature = ?
	`, table), signature).Scan(&clientID, &reqBytes, &sessBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fosite.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.hydrate(ctx, clientID, reqBytes, sessBytes, session)
}

func (s *Storage) deleteByTable(ctx context.Context, table, column, value string) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s = ?`, table, column), value)
	return err
}

func (s *Storage) hydrate(ctx context.Context, clientID string, reqBytes, sessBytes []byte, session fosite.Session) (fosite.Requester, error) {
	var sr storedRequest
	if err := json.Unmarshal(reqBytes, &sr); err != nil {
		return nil, fmt.Errorf("unmarshal requester: %w", err)
	}
	if session != nil && len(sessBytes) > 0 {
		if err := json.Unmarshal(sessBytes, session); err != nil {
			return nil, fmt.Errorf("unmarshal session: %w", err)
		}
	}
	client, err := s.GetClient(ctx, clientID)
	if err != nil {
		return nil, err
	}
	return &fosite.Request{
		ID:                sr.ID,
		RequestedAt:       sr.RequestedAt,
		Client:            client,
		RequestedScope:    sr.RequestedScope,
		GrantedScope:      sr.GrantedScope,
		Form:              sr.Form,
		Session:           session,
		RequestedAudience: sr.RequestedAudience,
		GrantedAudience:   sr.GrantedAudience,
	}, nil
}
