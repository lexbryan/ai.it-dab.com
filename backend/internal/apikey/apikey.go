// Package apikey is the data-access layer for the two-key + persona API
// credential model. It owns the api_keys table: a credential is a public key id
// (dab_pk_..., stored plainly) paired with a secret (dab_sk_...) stored only as
// a hash, plus a per-credential persona the gateway injects on every LLM call.
//
// # Secret hashing
//
// secret_hash stores a hash of the secret half, never the plaintext. This
// package is strategy-agnostic — it stores and returns the opaque hash string
// the caller supplies — but the intended strategy (chosen here, implemented by
// the key-generation and gateway-validation tickets) is a keyed HMAC-SHA256
// over the secret rather than bcrypt:
//
//   - The secret is a high-entropy random token, not a human password, so it is
//     not brute-forceable offline; bcrypt's deliberate slowness buys little.
//   - The gateway verifies the secret on EVERY request. A microsecond HMAC keeps
//     per-request cost negligible and avoids the bcrypt-CPU exhaustion an
//     attacker could trigger by spamming a known key id with wrong secrets.
//   - Lookup is by the public key_id (unique, indexed) first, then a
//     constant-time compare of the hash, so the hash need only be verifiable.
//
// Admin passwords still use bcrypt (see internal/auth) because those are
// low-entropy and verified rarely.
package apikey

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sentinel errors callers can match with errors.Is.
var (
	// ErrNotFound is returned when no credential matches the lookup (including a
	// revoked credential looked up as active).
	ErrNotFound = errors.New("apikey: not found")
	// ErrKeyIDTaken is returned when creating a credential whose key_id already
	// exists.
	ErrKeyIDTaken = errors.New("apikey: key id already exists")
)

// uniqueViolation is the Postgres SQLSTATE for a unique-constraint violation.
const uniqueViolation = "23505"

// APIKey is a full credential row, including the secret hash. It is returned by
// Create and GetActiveByKeyID, where the caller needs the hash to verify a
// presented secret. SecretHash is the hash of the secret, never the plaintext.
type APIKey struct {
	ID         string
	KeyID      string
	SecretHash string
	Name       string
	Persona    *string // nil when no persona is set
	CreatedBy  *string // nil for system/bootstrap-created keys; else a users.id
	CreatedAt  time.Time
	RevokedAt  *time.Time // nil = active
	LastUsedAt *time.Time
}

// Metadata is the non-secret view of a credential returned by List and Update.
// It deliberately omits SecretHash so the secret hash cannot leak through those
// paths.
type Metadata struct {
	ID         string
	KeyID      string
	Name       string
	Persona    *string
	CreatedBy  *string
	CreatedAt  time.Time
	RevokedAt  *time.Time // nil = active
	LastUsedAt *time.Time
}

// Repository reads and writes api_keys over a pgx pool.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository returns a Repository backed by pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// fullColumns includes secret_hash and is used only where the caller must verify
// a secret (Create, GetActiveByKeyID). metaColumns omits the hash for the
// list/update paths so it cannot be selected there.
const fullColumns = `id, key_id, secret_hash, name, persona, created_by, created_at, revoked_at, last_used_at`
const metaColumns = `id, key_id, name, persona, created_by, created_at, revoked_at, last_used_at`

// CreateParams are the fields needed to store a new credential. SecretHash is the
// already-hashed secret (this layer never hashes or sees the plaintext).
type CreateParams struct {
	KeyID      string
	SecretHash string
	Name       string
	Persona    *string
	CreatedBy  *string
}

// Create inserts a credential and returns the stored row (with its generated id
// and timestamps). A duplicate key_id returns ErrKeyIDTaken.
func (r *Repository) Create(ctx context.Context, p CreateParams) (APIKey, error) {
	const q = `INSERT INTO api_keys (key_id, secret_hash, name, persona, created_by)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING ` + fullColumns
	k, err := scanAPIKey(r.pool.QueryRow(ctx, q, p.KeyID, p.SecretHash, p.Name, p.Persona, p.CreatedBy))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return APIKey{}, ErrKeyIDTaken
		}
		return APIKey{}, fmt.Errorf("apikey: creating key: %w", err)
	}
	return k, nil
}

// GetActiveByKeyID returns the active (non-revoked) credential for keyID,
// including its secret hash and persona, so the gateway can verify the presented
// secret and inject the persona. A revoked or unknown key id returns ErrNotFound.
func (r *Repository) GetActiveByKeyID(ctx context.Context, keyID string) (APIKey, error) {
	const q = `SELECT ` + fullColumns + ` FROM api_keys WHERE key_id = $1 AND revoked_at IS NULL`
	k, err := scanAPIKey(r.pool.QueryRow(ctx, q, keyID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return APIKey{}, ErrNotFound
		}
		return APIKey{}, fmt.Errorf("apikey: querying key: %w", err)
	}
	return k, nil
}

// List returns metadata for every credential (active and revoked), newest first.
// The secret hash is never selected, so it cannot leak through this path.
func (r *Repository) List(ctx context.Context) ([]Metadata, error) {
	const q = `SELECT ` + metaColumns + ` FROM api_keys ORDER BY created_at DESC, id`
	rows, err := r.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("apikey: listing keys: %w", err)
	}
	defer rows.Close()

	var out []Metadata
	for rows.Next() {
		m, err := scanMetadata(rows)
		if err != nil {
			return nil, fmt.Errorf("apikey: scanning key: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("apikey: iterating keys: %w", err)
	}
	return out, nil
}

// UpdateParams are the editable fields of a credential.
type UpdateParams struct {
	Name    string
	Persona *string
}

// Update sets the label and persona of the credential with the given id and
// returns the updated metadata. ErrNotFound if no credential has that id.
func (r *Repository) Update(ctx context.Context, id string, p UpdateParams) (Metadata, error) {
	const q = `UPDATE api_keys SET name = $2, persona = $3 WHERE id = $1
		RETURNING ` + metaColumns
	m, err := scanMetadata(r.pool.QueryRow(ctx, q, id, p.Name, p.Persona))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Metadata{}, ErrNotFound
		}
		return Metadata{}, fmt.Errorf("apikey: updating key: %w", err)
	}
	return m, nil
}

// Revoke marks the active credential with the given id revoked (sets
// revoked_at). It targets active credentials: an unknown or already-revoked id
// returns ErrNotFound, so revoking is not silently a no-op.
func (r *Repository) Revoke(ctx context.Context, id string) error {
	const q = `UPDATE api_keys SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`
	tag, err := r.pool.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("apikey: revoking key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// scanner is the subset of pgx.Row both QueryRow results and rows iteration
// satisfy.
type scanner interface {
	Scan(dest ...any) error
}

func scanAPIKey(s scanner) (APIKey, error) {
	var k APIKey
	err := s.Scan(&k.ID, &k.KeyID, &k.SecretHash, &k.Name, &k.Persona, &k.CreatedBy, &k.CreatedAt, &k.RevokedAt, &k.LastUsedAt)
	return k, err
}

func scanMetadata(s scanner) (Metadata, error) {
	var m Metadata
	err := s.Scan(&m.ID, &m.KeyID, &m.Name, &m.Persona, &m.CreatedBy, &m.CreatedAt, &m.RevokedAt, &m.LastUsedAt)
	return m, err
}
