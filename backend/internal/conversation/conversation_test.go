package conversation

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/dbtest"
)

func setup(t *testing.T) (*Repository, *pgxpool.Pool) {
	pool := dbtest.Pool(t)
	return NewRepository(pool), pool
}

// insertKey creates an api_keys row (the FK target) and returns its id.
func insertKey(t *testing.T, pool *pgxpool.Pool, keyID string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO api_keys (key_id, secret_hash) VALUES ($1, 'h') RETURNING id`, keyID).Scan(&id); err != nil {
		t.Fatalf("insert api_key: %v", err)
	}
	return id
}

func TestCreateAndGetConversation(t *testing.T) {
	r, pool := setup(t)
	ctx := context.Background()
	keyA := insertKey(t, pool, "dab_pk_a")

	conv, err := r.CreateConversation(ctx, keyA, "qwen")
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if conv.ID == "" {
		t.Fatal("conversation id should be server-issued")
	}
	if conv.APIKeyID != keyA || conv.Model != "qwen" {
		t.Errorf("unexpected conversation: %+v", conv)
	}

	got, err := r.GetConversation(ctx, keyA, conv.ID)
	if err != nil || got.ID != conv.ID {
		t.Fatalf("GetConversation(owner) = %+v, %v", got, err)
	}
}

func TestGetConversation_CrossTenantNotFound(t *testing.T) {
	r, pool := setup(t)
	ctx := context.Background()
	keyA := insertKey(t, pool, "dab_pk_a")
	keyB := insertKey(t, pool, "dab_pk_b")

	conv, err := r.CreateConversation(ctx, keyA, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.GetConversation(ctx, keyB, conv.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-tenant GetConversation = %v, want ErrNotFound", err)
	}
	if _, err := r.GetConversation(ctx, keyA, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing GetConversation = %v, want ErrNotFound", err)
	}
}

func TestLoadHistory_Ordered(t *testing.T) {
	r, pool := setup(t)
	ctx := context.Background()
	keyA := insertKey(t, pool, "dab_pk_a")
	conv, _ := r.CreateConversation(ctx, keyA, "qwen")

	n := 7
	if err := r.AppendMessages(ctx, conv.ID, []NewMessage{
		{Role: RoleSystem, Content: "persona"},
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: "hello"},
		{Role: RoleUser, Content: "q2"},
		{Role: RoleAssistant, Content: "a2"},
		{Role: RoleUser, Content: "q3"},
		{Role: RoleAssistant, Content: "a3"},
	}); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	hist, err := r.LoadHistory(ctx, conv.ID)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(hist) != n {
		t.Fatalf("loaded %d, want %d", len(hist), n)
	}
	wantContent := []string{"persona", "hi", "hello", "q2", "a2", "q3", "a3"}
	for i, m := range hist {
		if m.Content != wantContent[i] {
			t.Errorf("message %d content = %q, want %q (order not preserved)", i, m.Content, wantContent[i])
		}
	}
}

func TestAppendMessages_AtomicOnFailure(t *testing.T) {
	r, pool := setup(t)
	ctx := context.Background()
	keyA := insertKey(t, pool, "dab_pk_a")
	conv, _ := r.CreateConversation(ctx, keyA, "qwen")

	// A bad role violates the CHECK constraint mid-batch; the whole append must
	// roll back, leaving no messages.
	err := r.AppendMessages(ctx, conv.ID, []NewMessage{
		{Role: RoleUser, Content: "kept?"},
		{Role: "robot", Content: "invalid"},
	})
	if err == nil {
		t.Fatal("expected an error from the invalid role")
	}
	hist, _ := r.LoadHistory(ctx, conv.ID)
	if len(hist) != 0 {
		t.Errorf("failed append must persist nothing, found %d messages", len(hist))
	}
}

func TestAppendMessages_BumpsUpdatedAt(t *testing.T) {
	r, pool := setup(t)
	ctx := context.Background()
	keyA := insertKey(t, pool, "dab_pk_a")
	conv, _ := r.CreateConversation(ctx, keyA, "qwen")

	tokens := 12
	if err := r.AppendMessages(ctx, conv.ID, []NewMessage{
		{Role: RoleUser, Content: "hi", TokenCount: &tokens},
	}); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}
	after, err := r.GetConversation(ctx, keyA, conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.UpdatedAt.After(conv.UpdatedAt) {
		t.Errorf("updated_at not bumped: was %v, now %v", conv.UpdatedAt, after.UpdatedAt)
	}

	hist, _ := r.LoadHistory(ctx, conv.ID)
	if len(hist) != 1 || hist[0].TokenCount == nil || *hist[0].TokenCount != tokens {
		t.Errorf("token_count not round-tripped: %+v", hist)
	}
}

func TestAppendMessages_UnknownConversation(t *testing.T) {
	r, _ := setup(t)
	ctx := context.Background()
	err := r.AppendMessages(ctx, "00000000-0000-0000-0000-000000000000", []NewMessage{
		{Role: RoleUser, Content: "x"},
	})
	if err == nil {
		t.Error("appending to a non-existent conversation should error")
	}
}
