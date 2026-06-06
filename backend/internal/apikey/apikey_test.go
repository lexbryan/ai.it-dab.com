package apikey

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/dbtest"
)

func newRepo(t *testing.T) (*Repository, *pgxpool.Pool) {
	pool := dbtest.Pool(t)
	// api_keys references users; clear both so each test starts clean.
	dbtest.Truncate(t, pool, "api_keys", "users")
	return NewRepository(pool), pool
}

// insertUser creates a real users row and returns its id, so created_by can be
// exercised against the actual foreign key.
func insertUser(t *testing.T, pool *pgxpool.Pool, email string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email, password_hash) VALUES ($1, 'x') RETURNING id`, email).Scan(&id)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

func strptr(s string) *string { return &s }

func TestCreateAndGetActiveByKeyID(t *testing.T) {
	r, pool := newRepo(t)
	ctx := context.Background()
	creator := insertUser(t, pool, "creator@example.com")
	persona := "You are the DAB Hub assistant."

	created, err := r.Create(ctx, CreateParams{
		KeyID:      "dab_pk_abc123",
		SecretHash: "hash-of-secret",
		Name:       "DAB Hub",
		Persona:    &persona,
		CreatedBy:  &creator,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" {
		t.Error("Create should return a generated id")
	}
	if created.CreatedAt.IsZero() {
		t.Error("created_at should be populated")
	}
	if created.RevokedAt != nil {
		t.Errorf("a new key should be active (revoked_at nil), got %v", created.RevokedAt)
	}
	if created.Persona == nil || *created.Persona != persona {
		t.Errorf("persona = %v, want %q", created.Persona, persona)
	}
	if created.CreatedBy == nil || *created.CreatedBy != creator {
		t.Errorf("created_by = %v, want %s", created.CreatedBy, creator)
	}

	got, err := r.GetActiveByKeyID(ctx, "dab_pk_abc123")
	if err != nil {
		t.Fatalf("GetActiveByKeyID: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("GetActiveByKeyID id = %s, want %s", got.ID, created.ID)
	}
	if got.SecretHash != "hash-of-secret" {
		t.Errorf("GetActiveByKeyID should return the secret hash for verification, got %q", got.SecretHash)
	}
	if got.Persona == nil || *got.Persona != persona {
		t.Errorf("GetActiveByKeyID persona = %v, want %q", got.Persona, persona)
	}
}

func TestCreate_PersonaNilRoundTrips(t *testing.T) {
	r, _ := newRepo(t)
	ctx := context.Background()

	created, err := r.Create(ctx, CreateParams{KeyID: "dab_pk_nopersona", SecretHash: "h"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Persona != nil {
		t.Errorf("persona = %v, want nil", created.Persona)
	}
	if created.CreatedBy != nil {
		t.Errorf("created_by = %v, want nil (system key)", created.CreatedBy)
	}

	got, err := r.GetActiveByKeyID(ctx, "dab_pk_nopersona")
	if err != nil {
		t.Fatalf("GetActiveByKeyID: %v", err)
	}
	if got.Persona != nil {
		t.Errorf("round-tripped persona = %v, want nil", got.Persona)
	}
}

func TestCreatedBy_SetNullWhenCreatingUserDeleted(t *testing.T) {
	r, pool := newRepo(t)
	ctx := context.Background()
	creator := insertUser(t, pool, "soon-deleted@example.com")

	if _, err := r.Create(ctx, CreateParams{KeyID: "dab_pk_survives", SecretHash: "h", CreatedBy: &creator}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The FK is ON DELETE SET NULL: removing the admin who created a credential
	// must neither delete the credential (would revoke a project's API access)
	// nor be blocked by it — the credential survives with created_by cleared.
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, creator); err != nil {
		t.Fatalf("delete creating user: %v", err)
	}

	got, err := r.GetActiveByKeyID(ctx, "dab_pk_survives")
	if err != nil {
		t.Fatalf("key should survive its creator's deletion, got: %v", err)
	}
	if got.CreatedBy != nil {
		t.Errorf("created_by after creator deletion = %v, want nil (SET NULL)", got.CreatedBy)
	}
}

func TestCreate_DuplicateKeyIDRejected(t *testing.T) {
	r, _ := newRepo(t)
	ctx := context.Background()
	if _, err := r.Create(ctx, CreateParams{KeyID: "dab_pk_dup", SecretHash: "h1"}); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := r.Create(ctx, CreateParams{KeyID: "dab_pk_dup", SecretHash: "h2"}); !errors.Is(err, ErrKeyIDTaken) {
		t.Errorf("duplicate Create error = %v, want ErrKeyIDTaken", err)
	}
}

func TestGetActiveByKeyID_NotFound(t *testing.T) {
	r, _ := newRepo(t)
	ctx := context.Background()
	if _, err := r.GetActiveByKeyID(ctx, "dab_pk_missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetActiveByKeyID(missing) = %v, want ErrNotFound", err)
	}
}

func TestRevoke_ExcludesFromActiveButListsWithTimestamp(t *testing.T) {
	r, _ := newRepo(t)
	ctx := context.Background()
	created, err := r.Create(ctx, CreateParams{KeyID: "dab_pk_revoke", SecretHash: "h"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := r.Revoke(ctx, created.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	// Revoked keys are not returned by the active lookup...
	if _, err := r.GetActiveByKeyID(ctx, "dab_pk_revoke"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetActiveByKeyID after revoke = %v, want ErrNotFound", err)
	}

	// ...but still appear in the list, marked with a revoked_at timestamp.
	list, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List len = %d, want 1", len(list))
	}
	if list[0].RevokedAt == nil {
		t.Error("revoked key should have revoked_at set in the list")
	}

	// Revoking an already-revoked (or unknown) id reports ErrNotFound rather
	// than silently succeeding.
	if err := r.Revoke(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("re-Revoke = %v, want ErrNotFound", err)
	}
	if err := r.Revoke(ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Revoke(missing) = %v, want ErrNotFound", err)
	}
}

func TestList_ReturnsMetadataNewestFirst(t *testing.T) {
	r, _ := newRepo(t)
	ctx := context.Background()
	p1 := "persona one"
	if _, err := r.Create(ctx, CreateParams{KeyID: "dab_pk_1", SecretHash: "h1", Name: "first", Persona: &p1}); err != nil {
		t.Fatalf("Create 1: %v", err)
	}
	if _, err := r.Create(ctx, CreateParams{KeyID: "dab_pk_2", SecretHash: "h2", Name: "second"}); err != nil {
		t.Fatalf("Create 2: %v", err)
	}

	list, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List len = %d, want 2", len(list))
	}
	// Newest first: the second key created sorts ahead of the first.
	if list[0].KeyID != "dab_pk_2" || list[1].KeyID != "dab_pk_1" {
		t.Errorf("List order = [%s, %s], want [dab_pk_2, dab_pk_1]", list[0].KeyID, list[1].KeyID)
	}
	// Metadata carries the persona but, by type, no secret hash field at all.
	if list[1].Persona == nil || *list[1].Persona != p1 {
		t.Errorf("metadata persona = %v, want %q", list[1].Persona, p1)
	}
}

func TestUpdate_LabelAndPersona(t *testing.T) {
	r, _ := newRepo(t)
	ctx := context.Background()
	orig := "original persona"
	created, err := r.Create(ctx, CreateParams{KeyID: "dab_pk_upd", SecretHash: "h", Name: "old name", Persona: &orig})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := r.Update(ctx, created.ID, UpdateParams{Name: "new name", Persona: strptr("new persona")})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "new name" {
		t.Errorf("updated name = %q, want %q", updated.Name, "new name")
	}
	if updated.Persona == nil || *updated.Persona != "new persona" {
		t.Errorf("updated persona = %v, want %q", updated.Persona, "new persona")
	}

	// The change is visible to the active lookup the gateway uses.
	got, err := r.GetActiveByKeyID(ctx, "dab_pk_upd")
	if err != nil {
		t.Fatalf("GetActiveByKeyID: %v", err)
	}
	if got.Persona == nil || *got.Persona != "new persona" {
		t.Errorf("persona after update = %v, want %q", got.Persona, "new persona")
	}

	// Persona can be cleared back to NULL.
	cleared, err := r.Update(ctx, created.ID, UpdateParams{Name: "new name", Persona: nil})
	if err != nil {
		t.Fatalf("Update clear persona: %v", err)
	}
	if cleared.Persona != nil {
		t.Errorf("cleared persona = %v, want nil", cleared.Persona)
	}
}

func TestUpdate_NotFound(t *testing.T) {
	r, _ := newRepo(t)
	ctx := context.Background()
	_, err := r.Update(ctx, "00000000-0000-0000-0000-000000000000", UpdateParams{Name: "x"})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Update(missing) = %v, want ErrNotFound", err)
	}
}
