package main

import (
	"context"
	"errors"
	"testing"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/auth"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/dbtest"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/user"
)

func TestParseEmail(t *testing.T) {
	if got, err := parseEmail("  Admin@Example.com "); err != nil || got != "Admin@Example.com" {
		t.Errorf("parseEmail = %q, %v; want normalized address", got, err)
	}
	for _, bad := range []string{"", "   ", "not-an-email", "a@b@c"} {
		if _, err := parseEmail(bad); err == nil {
			t.Errorf("parseEmail(%q) should error", bad)
		}
	}
}

func newRepo(t *testing.T) *user.Repository {
	pool := dbtest.Pool(t)
	dbtest.Truncate(t, pool, "users")
	return user.NewRepository(pool)
}

func TestCreateSuperuser_FreshDB(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	u, created, err := createSuperuser(ctx, repo, "admin@example.com", "a-strong-password", false)
	if err != nil || !created {
		t.Fatalf("createSuperuser = %+v, created=%v, err=%v", u, created, err)
	}
	if !u.IsSuperuser {
		t.Error("created account should be a superuser")
	}
	ok, err := auth.VerifyPassword("a-strong-password", u.PasswordHash)
	if err != nil || !ok {
		t.Errorf("stored password should verify: ok=%v err=%v", ok, err)
	}
}

func TestCreateSuperuser_DuplicateRefused(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	if _, _, err := createSuperuser(ctx, repo, "admin@example.com", "first-password", false); err != nil {
		t.Fatalf("first createSuperuser: %v", err)
	}
	// Re-running without -update-password must fail and not create a duplicate.
	if _, _, err := createSuperuser(ctx, repo, "admin@example.com", "second-password", false); !errors.Is(err, errUserExists) {
		t.Fatalf("duplicate createSuperuser err = %v, want errUserExists", err)
	}
	// Still exactly one account, with the original password.
	got, err := repo.GetByEmail(ctx, "admin@example.com")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if ok, _ := auth.VerifyPassword("first-password", got.PasswordHash); !ok {
		t.Error("original password should be unchanged after a refused duplicate")
	}
}

func TestCreateSuperuser_UpdatePassword(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	created, _, err := createSuperuser(ctx, repo, "admin@example.com", "first-password", false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	updated, wasCreated, err := createSuperuser(ctx, repo, "admin@example.com", "second-password", true)
	if err != nil || wasCreated {
		t.Fatalf("update path = created %v, err %v", wasCreated, err)
	}
	if updated.ID != created.ID {
		t.Error("update should target the same user")
	}
	if ok, _ := auth.VerifyPassword("second-password", updated.PasswordHash); !ok {
		t.Error("new password should verify after update")
	}
	if ok, _ := auth.VerifyPassword("first-password", updated.PasswordHash); ok {
		t.Error("old password should no longer verify")
	}
}

func TestCreateSuperuser_ShortPasswordRejected(t *testing.T) {
	repo := newRepo(t)
	if _, _, err := createSuperuser(context.Background(), repo, "admin@example.com", "short", false); !errors.Is(err, auth.ErrPasswordTooShort) {
		t.Errorf("short password err = %v, want ErrPasswordTooShort", err)
	}
}
