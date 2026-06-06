package user

import (
	"context"
	"errors"
	"testing"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/dbtest"
)

func newRepo(t *testing.T) *Repository {
	pool := dbtest.Pool(t)
	dbtest.Truncate(t, pool, "users")
	return NewRepository(pool)
}

func TestCreateAndGetByEmailAndID(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()

	created, err := r.Create(ctx, "Admin@Example.com", "hash-value", true)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" {
		t.Error("Create should return a generated id")
	}
	if !created.IsSuperuser || created.PasswordHash != "hash-value" {
		t.Errorf("unexpected stored row: %+v", created)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Error("timestamps should be populated")
	}

	byEmail, err := r.GetByEmail(ctx, "Admin@Example.com")
	if err != nil || byEmail.ID != created.ID {
		t.Fatalf("GetByEmail = %+v, %v; want id %s", byEmail, err, created.ID)
	}

	byID, err := r.GetByID(ctx, created.ID)
	if err != nil || byID.Email != created.Email {
		t.Fatalf("GetByID = %+v, %v", byID, err)
	}
}

func TestGetByEmail_CaseInsensitive(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	created, err := r.Create(ctx, "Mixed.Case@Example.com", "h", false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := r.GetByEmail(ctx, "mixed.case@EXAMPLE.COM")
	if err != nil || got.ID != created.ID {
		t.Errorf("case-insensitive lookup failed: %+v, %v", got, err)
	}
}

func TestCreate_DuplicateEmailRejected(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	if _, err := r.Create(ctx, "dup@example.com", "h1", false); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	// Same email differing only by case must collide at the DB level.
	if _, err := r.Create(ctx, "DUP@example.com", "h2", false); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("duplicate Create error = %v, want ErrEmailTaken", err)
	}
}

func TestGet_NotFound(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	if _, err := r.GetByEmail(ctx, "nobody@example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetByEmail(missing) = %v, want ErrNotFound", err)
	}
	if _, err := r.GetByID(ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetByID(missing) = %v, want ErrNotFound", err)
	}
}
