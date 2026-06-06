package usage

import (
	"context"
	"testing"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/dbtest"
)

// Each test seeds a minimal api_keys row (only key_id + secret_hash are required)
// so a gateway_usage row can satisfy its api_key_id foreign key.

func TestRepository_RecordPersistsContentFreeRow(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()

	var apiKeyID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO api_keys (key_id, secret_hash) VALUES ('dab_pk_usage', 'hash') RETURNING id`,
	).Scan(&apiKeyID); err != nil {
		t.Fatalf("seeding api key: %v", err)
	}

	pt, ct, status := 11, 7, 200
	if err := NewRepository(pool).Record(ctx, Record{
		APIKeyID:             apiKeyID,
		ConversationID:       "", // NULL — no session bound
		Model:                "qwen2.5",
		Stream:               true,
		PromptMsgCount:       3,
		PromptTokenCount:     &pt,
		CompletionTokenCount: &ct,
		UpstreamStatus:       &status,
		Outcome:              "success",
		LatencyMS:            42,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM gateway_usage`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("gateway_usage rows = %d, want exactly 1", rows)
	}

	var (
		gotKey     string
		convNull   bool
		model      string
		stream     bool
		promptMsgs int
		promptToks int
		outcome    string
		latency    int
	)
	if err := pool.QueryRow(ctx,
		`SELECT api_key_id, conversation_id IS NULL, model, stream, prompt_msg_count, prompt_token_count, outcome, latency_ms FROM gateway_usage`,
	).Scan(&gotKey, &convNull, &model, &stream, &promptMsgs, &promptToks, &outcome, &latency); err != nil {
		t.Fatalf("reading row: %v", err)
	}
	if gotKey != apiKeyID || !convNull || model != "qwen2.5" || !stream || promptMsgs != 3 || promptToks != 11 || outcome != "success" || latency != 42 {
		t.Errorf("row mismatch: key=%s convNull=%v model=%s stream=%v msgs=%d toks=%d outcome=%s latency=%d",
			gotKey, convNull, model, stream, promptMsgs, promptToks, outcome, latency)
	}

	// The audit table must be structurally incapable of holding message content.
	var hasContentColumn bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_name = 'gateway_usage' AND column_name IN ('content','message','prompt','completion','body'))`,
	).Scan(&hasContentColumn); err != nil {
		t.Fatal(err)
	}
	if hasContentColumn {
		t.Error("gateway_usage must not have any message-content column")
	}
}

func TestRepository_RecordNullableFieldsStoreNull(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()

	var apiKeyID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO api_keys (key_id, secret_hash) VALUES ('dab_pk_usage2', 'hash') RETURNING id`,
	).Scan(&apiKeyID); err != nil {
		t.Fatalf("seeding api key: %v", err)
	}

	// A streamed success with no upstream usage: token counts and status are nil.
	if err := NewRepository(pool).Record(ctx, Record{
		APIKeyID: apiKeyID, Model: "m", Stream: true, PromptMsgCount: 1, Outcome: "success", LatencyMS: 5,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	var ptNull, ctNull, statusNull bool
	if err := pool.QueryRow(ctx,
		`SELECT prompt_token_count IS NULL, completion_token_count IS NULL, upstream_status IS NULL FROM gateway_usage`,
	).Scan(&ptNull, &ctNull, &statusNull); err != nil {
		t.Fatal(err)
	}
	if !ptNull || !ctNull || !statusNull {
		t.Errorf("nullable fields not NULL: prompt=%v completion=%v status=%v", ptNull, ctNull, statusNull)
	}
}
