// Package usage records a durable, content-free audit row per gateway LLM call.
// Exactly one row is written per call (success or failure) for usage accounting
// and observability. A record stores counts, an outcome, and timings ONLY —
// never message content, project secrets, the VLLM_API_KEY, or upstream bodies.
package usage

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Record is one gateway-call audit entry. It deliberately carries no message
// content: only counts, the outcome, and timings.
type Record struct {
	APIKeyID             string // owning credential (api_keys.id)
	ConversationID       string // gateway-issued session id; "" is stored as NULL
	Model                string
	Stream               bool
	PromptMsgCount       int
	PromptTokenCount     *int // nil when the upstream did not report usage
	CompletionTokenCount *int // nil when the upstream did not report usage
	UpstreamStatus       *int // nil when no upstream HTTP response was received
	Outcome              string
	LatencyMS            int
}

// Recorder persists a usage record. The gateway calls it best-effort, so an
// implementation must never fail or delay the request it audits.
type Recorder interface {
	Record(ctx context.Context, rec Record) error
}

// Repository writes usage rows to Postgres.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository returns a Repository backed by pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Record inserts one usage row. An empty ConversationID is stored as NULL.
func (r *Repository) Record(ctx context.Context, rec Record) error {
	const q = `INSERT INTO gateway_usage
		(api_key_id, conversation_id, model, stream, prompt_msg_count,
		 prompt_token_count, completion_token_count, upstream_status, outcome, latency_ms)
		VALUES ($1, NULLIF($2, '')::uuid, $3, $4, $5, $6, $7, $8, $9, $10)`
	if _, err := r.pool.Exec(ctx, q,
		rec.APIKeyID, rec.ConversationID, rec.Model, rec.Stream, rec.PromptMsgCount,
		rec.PromptTokenCount, rec.CompletionTokenCount, rec.UpstreamStatus, rec.Outcome, rec.LatencyMS); err != nil {
		return fmt.Errorf("usage: recording: %w", err)
	}
	return nil
}

// Async wraps a Recorder so Record returns immediately and the write runs on a
// detached goroutine with its own timeout: a best-effort audit must never add
// latency to, or fail, the request it records. Write failures are logged, never
// returned.
func Async(inner Recorder, logger *slog.Logger) Recorder {
	if logger == nil {
		logger = slog.Default()
	}
	return &asyncRecorder{inner: inner, logger: logger, timeout: 5 * time.Second}
}

type asyncRecorder struct {
	inner   Recorder
	logger  *slog.Logger
	timeout time.Duration
}

// Record fires the write on a detached goroutine and returns nil immediately.
func (a *asyncRecorder) Record(_ context.Context, rec Record) error {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
		defer cancel()
		if err := a.inner.Record(ctx, rec); err != nil {
			a.logger.Warn("gateway usage audit write failed", "error", err, "outcome", rec.Outcome)
		}
	}()
	return nil
}
