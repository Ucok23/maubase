package oauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"

	josejwk "github.com/go-jose/go-jose/v3"
	"github.com/google/uuid"
)

// keyBits is the RSA key size for the JWT signing keypair. 2048 is the
// widely-accepted minimum for RS256 in 2026; not worth the CPU cost of 4096
// on a small VPS for a token-signing key with routine rotation.
const keyBits = 2048

// KeyStore persists the authorization server's RSA signing keypair(s) in
// SQLite and serves them as a JWKS. Every issued token is signed with the
// "current" key; older keys stay published (until pruned) so tokens signed
// before a rotation still verify.
type KeyStore struct {
	db *sql.DB
}

func NewKeyStore(db *sql.DB) *KeyStore {
	return &KeyStore{db: db}
}

// EnsureKey returns the current signing key, generating and persisting a
// fresh RSA keypair on first run.
func (k *KeyStore) EnsureKey(ctx context.Context) (kid string, key *rsa.PrivateKey, err error) {
	kid, key, err = k.currentKey(ctx)
	if err == nil {
		return kid, key, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", nil, err
	}
	return k.rotate(ctx)
}

func (k *KeyStore) currentKey(ctx context.Context) (string, *rsa.PrivateKey, error) {
	var kid string
	var der []byte
	err := k.db.QueryRowContext(ctx, `
		SELECT kid, private_der FROM oauth_signing_keys WHERE is_current = 1
	`).Scan(&kid, &der)
	if err != nil {
		return "", nil, err
	}
	key, err := x509.ParsePKCS1PrivateKey(der)
	if err != nil {
		return "", nil, fmt.Errorf("parse signing key %s: %w", kid, err)
	}
	return kid, key, nil
}

// rotate generates a new RSA keypair, makes it the current signing key, and
// keeps prior keys around for JWKS verification of already-issued tokens.
func (k *KeyStore) rotate(ctx context.Context) (string, *rsa.PrivateKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, keyBits)
	if err != nil {
		return "", nil, fmt.Errorf("generate rsa key: %w", err)
	}
	kid := uuid.NewString()
	der := x509.MarshalPKCS1PrivateKey(key)

	tx, err := k.db.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `UPDATE oauth_signing_keys SET is_current = 0`); err != nil {
		return "", nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO oauth_signing_keys (kid, private_der, is_current) VALUES (?, ?, 1)
	`, kid, der); err != nil {
		return "", nil, err
	}
	if err := tx.Commit(); err != nil {
		return "", nil, err
	}
	return kid, key, nil
}

// KeyGetter adapts EnsureKey to the func(ctx) (interface{}, error) shape
// Fosite's JWT signer expects.
func (k *KeyStore) KeyGetter(ctx context.Context) (interface{}, error) {
	_, key, err := k.EnsureKey(ctx)
	if err != nil {
		return nil, err
	}
	return key, nil
}

// EnsureHMACSecret returns the persisted HMAC secret used to sign
// authorize codes and refresh tokens, generating one on first run.
func (k *KeyStore) EnsureHMACSecret(ctx context.Context) ([]byte, error) {
	var secret []byte
	err := k.db.QueryRowContext(ctx, `SELECT secret FROM oauth_hmac_secret WHERE id = 1`).Scan(&secret)
	if err == nil {
		return secret, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	secret = make([]byte, 64)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate hmac secret: %w", err)
	}
	if _, err := k.db.ExecContext(ctx, `INSERT INTO oauth_hmac_secret (id, secret) VALUES (1, ?)`, secret); err != nil {
		return nil, fmt.Errorf("persist hmac secret: %w", err)
	}
	return secret, nil
}

// JWKS returns the public half of every stored signing key (current and
// past), suitable for serving at /.well-known/jwks.json so resource
// servers can verify access tokens without calling back to us.
func (k *KeyStore) JWKS(ctx context.Context) (*josejwk.JSONWebKeySet, error) {
	rows, err := k.db.QueryContext(ctx, `SELECT kid, private_der FROM oauth_signing_keys`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	set := &josejwk.JSONWebKeySet{}
	for rows.Next() {
		var kid string
		var der []byte
		if err := rows.Scan(&kid, &der); err != nil {
			return nil, err
		}
		key, err := x509.ParsePKCS1PrivateKey(der)
		if err != nil {
			return nil, fmt.Errorf("parse signing key %s: %w", kid, err)
		}
		set.Keys = append(set.Keys, josejwk.JSONWebKey{
			Key:       &key.PublicKey,
			KeyID:     kid,
			Algorithm: "RS256",
			Use:       "sig",
		})
	}
	return set, rows.Err()
}
