package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/dbtest"
)

// TestConversationsSchema validates the conversations/messages migration: the
// gateway-issued session id, ordered history, per-credential ownership, and the
// cascade from conversation to messages. It is skipped without a test database.
func TestConversationsSchema(t *testing.T) {
	pool := dbtest.Pool(t)
	dbtest.Truncate(t, pool, "messages", "conversations", "api_keys")
	ctx := context.Background()

	// Two credentials, to prove tenant scoping.
	var keyA, keyB string
	if err := pool.QueryRow(ctx,
		`INSERT INTO api_keys (key_id, secret_hash) VALUES ('dab_pk_a','h') RETURNING id`).Scan(&keyA); err != nil {
		t.Fatalf("insert api_key A: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO api_keys (key_id, secret_hash) VALUES ('dab_pk_b','h') RETURNING id`).Scan(&keyB); err != nil {
		t.Fatalf("insert api_key B: %v", err)
	}

	// The conversation id is issued by the DB default (gateway-issued session id).
	var convID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO conversations (api_key_id, model) VALUES ($1, 'qwen') RETURNING id`, keyA).Scan(&convID); err != nil {
		t.Fatalf("insert conversation: %v", err)
	}
	if convID == "" {
		t.Fatal("conversation id should be server-issued, got empty")
	}

	// Insert ordered messages with explicit increasing timestamps.
	base := time.Now().Add(-time.Hour)
	want := []struct{ role, content string }{
		{"system", "persona"},
		{"user", "hello"},
		{"assistant", "hi there"},
	}
	for i, m := range want {
		if _, err := pool.Exec(ctx,
			`INSERT INTO messages (conversation_id, role, content, created_at) VALUES ($1,$2,$3,$4)`,
			convID, m.role, m.content, base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("insert message %d: %v", i, err)
		}
	}

	// History loads oldest-first.
	rows, err := pool.Query(ctx,
		`SELECT role, content FROM messages WHERE conversation_id = $1 ORDER BY created_at`, convID)
	if err != nil {
		t.Fatalf("load history: %v", err)
	}
	var got []struct{ role, content string }
	for rows.Next() {
		var r struct{ role, content string }
		if err := rows.Scan(&r.role, &r.content); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	rows.Close()
	if len(got) != len(want) {
		t.Fatalf("loaded %d messages, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("message %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	// An invalid role is rejected by the CHECK constraint.
	if _, err := pool.Exec(ctx,
		`INSERT INTO messages (conversation_id, role, content) VALUES ($1,'robot','x')`, convID); err == nil {
		t.Error("an invalid role should violate the CHECK constraint")
	}

	// Tenant scoping: key B does not own key A's conversation.
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM conversations WHERE api_key_id = $1 AND id = $2`, keyB, convID).Scan(&n); err != nil {
		t.Fatalf("scoped count: %v", err)
	}
	if n != 0 {
		t.Error("conversation must not be visible to another credential")
	}

	// Cascade: deleting the conversation removes its messages.
	if _, err := pool.Exec(ctx, `DELETE FROM conversations WHERE id = $1`, convID); err != nil {
		t.Fatalf("delete conversation: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM messages WHERE conversation_id = $1`, convID).Scan(&n); err != nil {
		t.Fatalf("post-cascade count: %v", err)
	}
	if n != 0 {
		t.Errorf("messages should cascade-delete with the conversation, %d remain", n)
	}
}
