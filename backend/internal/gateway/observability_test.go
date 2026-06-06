package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/gatewaycore"
	applog "github.com/lexbryan/ai.it-dab.com/backend/internal/log"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/usage"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/vllm"
)

// fakeRecorder captures usage records synchronously so a test can assert exactly
// what the handler emitted.
type fakeRecorder struct {
	mu      sync.Mutex
	records []usage.Record
}

func (f *fakeRecorder) Record(_ context.Context, rec usage.Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, rec)
	return nil
}

func (f *fakeRecorder) all() []usage.Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]usage.Record(nil), f.records...)
}

func decodeErrType(t *testing.T, body []byte) string {
	t.Helper()
	var env struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decoding error envelope: %v (%s)", err, body)
	}
	return env.Error.Type
}

// A successful non-streaming call records exactly one audit row with the model,
// counts, and a 200 upstream status — and no message content (the Record type
// has no content field).
func TestChat_AuditsSuccessfulCall(t *testing.T) {
	repo := newFakeConvRepo()
	up := &fakeUpstream{} // default reply with usage prompt=3 completion=2
	rec := &fakeRecorder{}
	h := NewChatHandler(gatewaycore.NewService(repo, 0), up, rec)

	rr := httptest.NewRecorder()
	h.Chat(rr, chatReq(`{"model":"qwen","message":"hi"}`, credWithPersona("key-1", "P")))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body)
	}

	records := rec.all()
	if len(records) != 1 {
		t.Fatalf("usage records = %d, want exactly 1", len(records))
	}
	got := records[0]
	if got.Outcome != "success" {
		t.Errorf("outcome = %q, want success", got.Outcome)
	}
	if got.APIKeyID != "key-1" || got.Model != "qwen" || got.Stream {
		t.Errorf("record fields wrong: %+v", got)
	}
	if got.UpstreamStatus == nil || *got.UpstreamStatus != 200 {
		t.Errorf("upstream_status = %v, want 200", got.UpstreamStatus)
	}
	if got.PromptTokenCount == nil || *got.PromptTokenCount != 3 || got.CompletionTokenCount == nil || *got.CompletionTokenCount != 2 {
		t.Errorf("token counts = %v/%v, want 3/2", got.PromptTokenCount, got.CompletionTokenCount)
	}
	if got.PromptMsgCount != 2 { // persona + the new user turn
		t.Errorf("prompt_msg_count = %d, want 2", got.PromptMsgCount)
	}
}

// A failed-upstream call records exactly one row with the upstream status and no
// token counts.
func TestChat_AuditsUpstreamError(t *testing.T) {
	repo := newFakeConvRepo()
	up := &fakeUpstream{err: &vllm.UpstreamError{StatusCode: 500, Message: "boom"}}
	rec := &fakeRecorder{}
	h := NewChatHandler(gatewaycore.NewService(repo, 0), up, rec)

	rr := httptest.NewRecorder()
	h.Chat(rr, chatReq(`{"model":"qwen","message":"hi"}`, credWithPersona("key-1", "P")))
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rr.Code)
	}
	records := rec.all()
	if len(records) != 1 {
		t.Fatalf("usage records = %d, want 1", len(records))
	}
	if records[0].Outcome != "upstream_error" {
		t.Errorf("outcome = %q, want upstream_error", records[0].Outcome)
	}
	if records[0].UpstreamStatus == nil || *records[0].UpstreamStatus != 500 {
		t.Errorf("upstream_status = %v, want 500", records[0].UpstreamStatus)
	}
	if records[0].PromptTokenCount != nil {
		t.Error("a failed call must not carry token counts")
	}
}

// A streamed call records exactly one row (parity with non-streaming), with
// stream=true, a 200 status, and nil token counts (the stream reports none).
func TestStreamChat_AuditsSuccessfulCall(t *testing.T) {
	repo := newFakeConvRepo()
	up := &fakeUpstream{streamChunks: []string{sseChunk("a"), sseChunk("b"), sseDone}}
	rec := &fakeRecorder{}
	h := NewChatHandler(gatewaycore.NewService(repo, 0), up, rec)

	fr := newFlushRecorder()
	h.Chat(fr, streamReq(context.Background(), `{"model":"qwen","message":"hi","stream":true}`, credWithPersona("key-1", "P")))
	if fr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", fr.Code)
	}
	records := rec.all()
	if len(records) != 1 {
		t.Fatalf("usage records = %d, want 1", len(records))
	}
	got := records[0]
	if got.Outcome != "success" || !got.Stream {
		t.Errorf("record = %+v, want success/stream", got)
	}
	if got.PromptTokenCount != nil || got.CompletionTokenCount != nil {
		t.Error("a streamed call reports no token counts")
	}
	if got.UpstreamStatus == nil || *got.UpstreamStatus != 200 {
		t.Errorf("upstream_status = %v, want 200", got.UpstreamStatus)
	}
}

// A request rejected by validation never reached the model, so it is neither
// audited nor sent upstream.
func TestChat_ValidationFailureIsNotAudited(t *testing.T) {
	repo := newFakeConvRepo()
	up := &fakeUpstream{}
	rec := &fakeRecorder{}
	h := NewChatHandler(gatewaycore.NewService(repo, 0), up, rec)

	rr := httptest.NewRecorder()
	h.Chat(rr, chatReq(`{"model":"qwen"}`, credWithPersona("key-1", "P"))) // no message/messages
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if up.called {
		t.Error("upstream must not be called on a rejected request")
	}
	if n := len(rec.all()); n != 0 {
		t.Errorf("validation rejection must not be audited, got %d records", n)
	}
}

// An oversized body is rejected with a structured 400 before any upstream call.
func TestChat_OversizedBodyRejected(t *testing.T) {
	repo := newFakeConvRepo()
	up := &fakeUpstream{}
	h := newChatServer(repo, up)

	big := strings.Repeat("a", (1<<20)+1) // exceeds maxChatBody
	rr := httptest.NewRecorder()
	h.Chat(rr, chatReq(`{"model":"qwen","message":"`+big+`"}`, credWithPersona("key-1", "P")))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("oversized status = %d, want 400", rr.Code)
	}
	if decodeErrType(t, rr.Body.Bytes()) != errInvalidRequest {
		t.Errorf("oversized type = %q, want invalid_request", decodeErrType(t, rr.Body.Bytes()))
	}
	if up.called {
		t.Error("upstream must not be called for an oversized body")
	}
}

// Each failure mode answers with the stable, documented error type paired with
// its status — the single source of that mapping is statusForError.
func TestChat_ErrorEnvelopeCarriesStableType(t *testing.T) {
	// unauthorized: no credential in context (handler called without the middleware).
	h := newChatServer(newFakeConvRepo(), &fakeUpstream{})
	rr := httptest.NewRecorder()
	noCred := httptest.NewRequest(http.MethodPost, GatewayChatPath, strings.NewReader(`{"model":"q","message":"hi"}`))
	noCred = noCred.WithContext(applog.WithContext(noCred.Context(), discardLogger()))
	h.Chat(rr, noCred)
	if rr.Code != http.StatusUnauthorized || decodeErrType(t, rr.Body.Bytes()) != errUnauthorized {
		t.Errorf("missing credential: %d/%s, want 401/unauthorized", rr.Code, decodeErrType(t, rr.Body.Bytes()))
	}

	// invalid_request: malformed body.
	rr = httptest.NewRecorder()
	h.Chat(rr, chatReq(`not-json`, credWithPersona("k", "P")))
	if rr.Code != http.StatusBadRequest || decodeErrType(t, rr.Body.Bytes()) != errInvalidRequest {
		t.Errorf("bad body: %d/%s, want 400/invalid_request", rr.Code, decodeErrType(t, rr.Body.Bytes()))
	}

	// not_found: a session owned by another credential.
	repo := newFakeConvRepo()
	repo.seed("conv-X", "key-A", "qwen")
	h2 := newChatServer(repo, &fakeUpstream{})
	rr = httptest.NewRecorder()
	h2.Chat(rr, chatReq(`{"session_id":"conv-X","message":"hi"}`, credWithPersona("key-B", "P")))
	if rr.Code != http.StatusNotFound || decodeErrType(t, rr.Body.Bytes()) != errNotFound {
		t.Errorf("cross-tenant: %d/%s, want 404/not_found", rr.Code, decodeErrType(t, rr.Body.Bytes()))
	}

	// upstream_error.
	h3 := newChatServer(newFakeConvRepo(), &fakeUpstream{err: &vllm.UpstreamError{StatusCode: 503, Message: "x"}})
	rr = httptest.NewRecorder()
	h3.Chat(rr, chatReq(`{"model":"q","message":"hi"}`, credWithPersona("k", "P")))
	if rr.Code != http.StatusBadGateway || decodeErrType(t, rr.Body.Bytes()) != errUpstream {
		t.Errorf("upstream: %d/%s, want 502/upstream_error", rr.Code, decodeErrType(t, rr.Body.Bytes()))
	}
}

// The per-call observability log carries the required fields and the correlation
// id (via the request-scoped logger) but never message content or the persona.
func TestChat_EmitsStructuredLogWithoutContent(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	repo := newFakeConvRepo()
	h := NewChatHandler(gatewaycore.NewService(repo, 0), &fakeUpstream{}, nil)

	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, GatewayChatPath, strings.NewReader(`{"model":"qwen","message":"secret-user-content"}`))
	r = r.WithContext(applog.WithContext(withCredential(r.Context(), credWithPersona("key-1", "secret-persona")), logger))
	h.Chat(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	logged := buf.String()
	for _, field := range []string{"gateway call", "api_key_id", "model", "outcome", "latency_ms", "stream", "prompt_msg_count"} {
		if !strings.Contains(logged, field) {
			t.Errorf("log missing %q: %s", field, logged)
		}
	}
	if strings.Contains(logged, "secret-user-content") || strings.Contains(logged, "secret-persona") {
		t.Errorf("log leaked message content or persona: %s", logged)
	}
}
