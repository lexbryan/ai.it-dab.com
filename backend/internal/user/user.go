// Package user is the data-access layer for admin / superuser accounts. It owns
// the users table: creating accounts and looking them up by email or id.
// Password hashing and verification live in internal/auth, not here.
package user

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
	// ErrNotFound is returned when no user matches the lookup.
	ErrNotFound = errors.New("user: not found")
	// ErrEmailTaken is returned when creating a user whose email (case-
	// insensitively) already exists.
	ErrEmailTaken = errors.New("user: email already exists")
)

// uniqueViolation is the Postgres SQLSTATE for a unique-constraint violation.
const uniqueViolation = "23505"

// User is an admin account row. PasswordHash is the full encoded bcrypt hash;
// it is never the plaintext password.
type User struct {
	ID           string
	Email        string
	PasswordHash string
	IsSuperuser  bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Repository reads and writes users over a pgx pool.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository returns a Repository backed by pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

const selectColumns = `id, email, password_hash, is_superuser, created_at, updated_at`

// Create inserts a user and returns the stored row (with its generated id and
// timestamps). A duplicate email returns ErrEmailTaken.
func (r *Repository) Create(ctx context.Context, email, passwordHash string, isSuperuser bool) (User, error) {
	const q = `INSERT INTO users (email, password_hash, is_superuser)
		VALUES ($1, $2, $3)
		RETURNING ` + selectColumns
	u, err := scanUser(r.pool.QueryRow(ctx, q, email, passwordHash, isSuperuser))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return User{}, ErrEmailTaken
		}
		return User{}, fmt.Errorf("user: creating user: %w", err)
	}
	return u, nil
}

// GetByEmail looks a user up by email (case-insensitive). ErrNotFound if none.
func (r *Repository) GetByEmail(ctx context.Context, email string) (User, error) {
	const q = `SELECT ` + selectColumns + ` FROM users WHERE email = $1`
	return r.getOne(ctx, q, email)
}

// GetByID looks a user up by id. ErrNotFound if none.
func (r *Repository) GetByID(ctx context.Context, id string) (User, error) {
	const q = `SELECT ` + selectColumns + ` FROM users WHERE id = $1`
	return r.getOne(ctx, q, id)
}

func (r *Repository) getOne(ctx context.Context, query string, arg any) (User, error) {
	u, err := scanUser(r.pool.QueryRow(ctx, query, arg))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, fmt.Errorf("user: querying user: %w", err)
	}
	return u, nil
}

// row is the subset of pgx.Row scanUser needs (a *pgxpool.Pool's QueryRow
// result satisfies it).
type row interface {
	Scan(dest ...any) error
}

func scanUser(r row) (User, error) {
	var u User
	err := r.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.IsSuperuser, &u.CreatedAt, &u.UpdatedAt)
	return u, err
}
