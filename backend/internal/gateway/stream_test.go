package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/conversation"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/vllm"
)

// --- SSE test helpers ---------------------------------------------------------

const sseDone = "data: [DONE]\n\n"

// sseChunk builds one OpenAI/vLLM streaming content frame.
func sseChunk(content string) string {
	payload, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"delta": map[string]string{"content": content}}},
	})
	return "data: " + string(payload) + "\n\n"
}

// streamReq builds a gateway request carrying the given context and credential.
func streamReq(ctx context.Context, body string, cred Credential) *http.Request {
	r := httptest.NewRequest(http.MethodPost, GatewayChatPath, strings.NewReader(body))
	return r.WithContext(withCredential(ctx, cred))
}

// flushRecorder is a ResponseRecorder that counts Flush calls and (optionally)
// signals each flush, so a test can prove chunks are flushed individually and
// arrive incrementally rather than buffered into one write.
type flushRecorder struct {
	*httptest.ResponseRecorder
	flushes int
	signal  chan struct{}
}

func newFlushRecorder() *flushRecorder {
	return &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (f *flushRecorder) Flush() {
	f.flushes++
	f.ResponseRecorder.Flush()
	if f.signal != nil {
		f.signal <- struct{}{}
	}
}

// noFlushWriter is a ResponseWriter that does NOT implement http.Flusher, so the
// streaming handler must fall back to a normal JSON error.
type noFlushWriter struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func newNoFlushWriter() *noFlushWriter { return &noFlushWriter{header: http.Header{}} }

func (w *noFlushWriter) Header() http.Header         { return w.header }
func (w *noFlushWriter) Write(b []byte) (int, error) { return w.body.Write(b) }
func (w *noFlushWriter) WriteHeader(code int)        { w.code = code }

// scriptedBody is a streaming upstream body the test drives chunk-by-chunk. Read
// blocks until the test pushes a chunk, the script is finished (EOF), or the
// context is canceled (a client disconnect, which surfaces ctx.Err()). It is the
// in-test stand-in for the real vLLM client's still-open body, whose reads
// likewise fail when the request context is canceled.
type scriptedBody struct {
	ctx    context.Context
	chunks chan []byte
	buf    []byte
	closed atomic.Bool
}

func newScriptedBody(ctx context.Context) *scriptedBody {
	return &scriptedBody{ctx: ctx, chunks: make(chan []byte)} // unbuffered: push blocks until read
}

func (b *scriptedBody) push(s string) { b.chunks <- []byte(s) }
func (b *scriptedBody) finish()       { close(b.chunks) }

func (b *scriptedBody) Read(p []byte) (int, error) {
	for len(b.buf) == 0 {
		select {
		case <-b.ctx.Done():
			return 0, b.ctx.Err()
		case chunk, ok := <-b.chunks:
			if !ok {
				return 0, io.EOF
			}
			b.buf = chunk
		}
	}
	n := copy(p, b.buf)
	b.buf = b.buf[n:]
	return n, nil
}

func (b *scriptedBody) Close() error { b.closed.Store(true); return nil }

// chunkThenErrBody yields one chunk, then a non-EOF read error: a mid-stream
// upstream failure that is NOT a client disconnect.
type chunkThenErrBody struct {
	chunk []byte
	sent  bool
	err   error
}

func (b *chunkThenErrBody) Read(p []byte) (int, error) {
	if !b.sent {
		b.sent = true
		return copy(p, b.chunk), nil
	}
	return 0, b.err
}

func (b *chunkThenErrBody) Close() error { return nil }

func waitFlush(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for flush (%s) — the stream appears buffered, not incremental", what)
	}
}

// --- tests --------------------------------------------------------------------

// The headline streaming case: stream:true selects the SSE path, every chunk is
// flushed individually, the gateway-issued session id is surfaced (header + SSE
// frame), the full persona+turn context goes upstream exactly as the
// non-streaming path sends it, and after [DONE] the reconstructed assistant
// message (the concatenated deltas) is persisted with the new user turn.
func TestStreamChat_FlushesEachChunkReconstructsAndPersists(t *testing.T) {
	repo := newFakeConvRepo()
	up := &fakeUpstream{streamChunks: []string{sseChunk("Hel"), sseChunk("lo"), sseChunk(" world"), sseDone}}
	h := newChatServer(repo, up)

	fr := newFlushRecorder()
	h.Chat(fr, streamReq(context.Background(), `{"model":"qwen","message":"hi","stream":true}`, credWithPersona("key-1", "You are helpful.")))

	if fr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", fr.Code, fr.Body)
	}
	if ct := fr.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	session := fr.Header().Get(HeaderSessionID)
	if session == "" {
		t.Fatal("streaming response must carry the gateway-issued session id header")
	}

	body := fr.Body.String()
	// The leading session frame surfaces the same id as the header.
	if !strings.Contains(body, "event: session") || !strings.Contains(body, `"session_id":"`+session+`"`) {
		t.Errorf("body missing the leading session frame for %q:\n%s", session, body)
	}
	// Every content delta and the terminator were passed through.
	for _, want := range []string{`"content":"Hel"`, `"content":"lo"`, `"content":" world"`, "data: [DONE]"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
	// Each chunk was flushed: session frame + 3 content frames at minimum.
	if fr.flushes < 4 {
		t.Errorf("flushes = %d, want >= 4 (per-chunk flushing, not one buffered write)", fr.flushes)
	}

	// Context parity with the non-streaming path: persona leads, then the turn.
	if got, want := roleContents(up.got.Messages), []string{"system:You are helpful.", "user:hi"}; !equalStrings(got, want) {
		t.Errorf("upstream messages = %v, want %v", got, want)
	}
	if up.got.Model != "qwen" {
		t.Errorf("upstream model = %q, want qwen", up.got.Model)
	}
	// After [DONE], the reconstructed assistant (concatenated deltas) + the user
	// turn are persisted — identical in shape to the non-streaming path.
	if got, want := storedRoleContents(repo, session), []string{"user:hi", "assistant:Hello world"}; !equalStrings(got, want) {
		t.Errorf("persisted = %v, want %v (assistant = concatenated deltas)", got, want)
	}
}

// The first content chunk must reach the client before the upstream sends
// [DONE] — proof the body is streamed, not buffered to completion first.
func TestStreamChat_FirstChunkArrivesBeforeDone(t *testing.T) {
	repo := newFakeConvRepo()
	up := &fakeUpstream{}
	bodyCh := make(chan *scriptedBody, 1)
	up.streamBodyFunc = func(ctx context.Context) io.ReadCloser {
		b := newScriptedBody(ctx)
		bodyCh <- b
		return b
	}
	h := newChatServer(repo, up)

	fr := newFlushRecorder()
	fr.signal = make(chan struct{}, 32)
	done := make(chan struct{})
	go func() {
		h.Chat(fr, streamReq(context.Background(), `{"model":"qwen","message":"hi","stream":true}`, credWithPersona("key-1", "P")))
		close(done)
	}()

	b := <-bodyCh
	waitFlush(t, fr.signal, "session frame")       // the leading session frame flushes first
	b.push(sseChunk("first"))                      // delivered while the stream is still open
	waitFlush(t, fr.signal, "first content chunk") // arrives BEFORE we send [DONE]

	b.push(sseDone)
	b.finish()
	<-done

	if got, want := storedRoleContents(repo, fr.Header().Get(HeaderSessionID)), []string{"user:hi", "assistant:first"}; !equalStrings(got, want) {
		t.Errorf("persisted = %v, want %v", got, want)
	}
}

// A streamed follow-up sends prior history + persona upstream and persists only
// the new turn + reply — the streaming path reuses the same core as
// non-streaming, so history handling cannot diverge.
func TestStreamChat_FollowUpSendsHistoryAndPersistsOnlyNewTurn(t *testing.T) {
	repo := newFakeConvRepo()
	repo.seed("conv-X", "key-1", "qwen",
		conversation.Message{Role: "user", Content: "h1"},
		conversation.Message{Role: "assistant", Content: "a1"},
	)
	up := &fakeUpstream{streamChunks: []string{sseChunk("re"), sseChunk("ply"), sseDone}}
	h := newChatServer(repo, up)

	fr := newFlushRecorder()
	h.Chat(fr, streamReq(context.Background(), `{"session_id":"conv-X","message":"h2","stream":true}`, credWithPersona("key-1", "P")))

	if fr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", fr.Code, fr.Body)
	}
	if got := fr.Header().Get(HeaderSessionID); got != "conv-X" {
		t.Errorf("session id header = %q, want conv-X", got)
	}
	if got, want := roleContents(up.got.Messages), []string{"system:P", "user:h1", "assistant:a1", "user:h2"}; !equalStrings(got, want) {
		t.Errorf("upstream messages = %v, want %v", got, want)
	}
	if up.got.Model != "qwen" {
		t.Errorf("follow-up upstream model = %q, want inherited qwen", up.got.Model)
	}
	if got, want := storedRoleContents(repo, "conv-X"), []string{"user:h1", "assistant:a1", "user:h2", "assistant:reply"}; !equalStrings(got, want) {
		t.Errorf("persisted = %v, want %v", got, want)
	}
}

// A pre-stream upstream error answers with the normal JSON error envelope, never
// a half-open SSE stream, and persists nothing.
func TestStreamChat_PreStreamError_IsJSONNotSSE(t *testing.T) {
	repo := newFakeConvRepo()
	up := &fakeUpstream{streamErr: &vllm.UpstreamError{StatusCode: http.StatusBadGateway, Message: "no upstream"}}
	h := newChatServer(repo, up)

	fr := newFlushRecorder()
	h.Chat(fr, streamReq(context.Background(), `{"model":"qwen","message":"hi","stream":true}`, credWithPersona("key-1", "P")))

	if fr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", fr.Code)
	}
	if ct := fr.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json (not SSE)", ct)
	}
	body := fr.Body.String()
	if strings.Contains(body, "data:") || strings.Contains(body, "event:") {
		t.Errorf("pre-stream error must not emit any SSE frames:\n%s", body)
	}
	if strings.Contains(body, "no upstream") {
		t.Errorf("error body leaked the upstream message:\n%s", body)
	}
	var env struct {
		Error struct{ Type string } `json:"error"`
	}
	if err := json.Unmarshal(fr.Body.Bytes(), &env); err != nil || env.Error.Type != "upstream_error" {
		t.Errorf("error envelope = %s, want type upstream_error", body)
	}
	assertNothingPersisted(t, repo)
}

// A mid-stream upstream failure (a read error after the stream began) emits an
// SSE error frame and persists nothing — a truncated reply is never stored.
func TestStreamChat_MidStreamError_EmitsSSEErrorNoPersist(t *testing.T) {
	repo := newFakeConvRepo()
	up := &fakeUpstream{}
	up.streamBodyFunc = func(context.Context) io.ReadCloser {
		return &chunkThenErrBody{chunk: []byte(sseChunk("partial")), err: errors.New("upstream exploded")}
	}
	h := newChatServer(repo, up)

	fr := newFlushRecorder()
	h.Chat(fr, streamReq(context.Background(), `{"model":"qwen","message":"hi","stream":true}`, credWithPersona("key-1", "P")))

	if fr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the stream had already started)", fr.Code)
	}
	body := fr.Body.String()
	if !strings.Contains(body, `"content":"partial"`) {
		t.Errorf("the partial chunk should have reached the client:\n%s", body)
	}
	if !strings.Contains(body, "event: error") || !strings.Contains(body, "upstream_error") {
		t.Errorf("a mid-stream error must emit an SSE error frame:\n%s", body)
	}
	assertNothingPersisted(t, repo)
}

// A client disconnect cancels the upstream call (its body is closed) and emits
// no error frame; the partial turn is discarded, per the documented policy.
func TestStreamChat_ClientDisconnect_DiscardsAndClosesUpstream(t *testing.T) {
	repo := newFakeConvRepo()
	up := &fakeUpstream{}
	bodyCh := make(chan *scriptedBody, 1)
	up.streamBodyFunc = func(ctx context.Context) io.ReadCloser {
		b := newScriptedBody(ctx)
		bodyCh <- b
		return b
	}
	h := newChatServer(repo, up)

	ctx, cancel := context.WithCancel(context.Background())
	fr := newFlushRecorder()
	done := make(chan struct{})
	go func() {
		h.Chat(fr, streamReq(ctx, `{"model":"qwen","message":"hi","stream":true}`, credWithPersona("key-1", "P")))
		close(done)
	}()

	b := <-bodyCh
	b.push(sseChunk("partial")) // one chunk relayed, then the client vanishes
	cancel()
	<-done

	if body := fr.Body.String(); strings.Contains(body, "event: error") {
		t.Errorf("a client disconnect must not emit an error frame:\n%s", body)
	}
	if !b.closed.Load() {
		t.Error("the upstream stream body must be closed when the client disconnects")
	}
	assertNothingPersisted(t, repo)
}

// If the upstream truncates mid-frame (a final data: line with no trailing
// newline), the partial frame must still be terminated before the SSE error
// frame, so the two do not run together into malformed SSE.
func TestStreamChat_TruncatedFinalFrameStaysWellFormed(t *testing.T) {
	repo := newFakeConvRepo()
	up := &fakeUpstream{}
	up.streamBodyFunc = func(context.Context) io.ReadCloser {
		// A content frame with NO trailing newline, then EOF: a truncated upstream.
		return &chunkThenErrBody{chunk: []byte(`data: {"choices":[{"delta":{"content":"x"}}]}`), err: io.EOF}
	}
	h := newChatServer(repo, up)

	fr := newFlushRecorder()
	h.Chat(fr, streamReq(context.Background(), `{"model":"qwen","message":"hi","stream":true}`, credWithPersona("key-1", "P")))

	body := fr.Body.String()
	if strings.Contains(body, "}event: error") {
		t.Errorf("truncated frame ran into the error frame (malformed SSE):\n%s", body)
	}
	if !strings.Contains(body, "event: error") {
		t.Errorf("a truncated stream must still emit an SSE error frame:\n%s", body)
	}
	assertNothingPersisted(t, repo)
}

// Without an http.Flusher the handler cannot stream, so it fails before any SSE
// with a normal JSON error and never calls upstream.
func TestStreamChat_NoFlusher_Returns500JSON(t *testing.T) {
	repo := newFakeConvRepo()
	up := &fakeUpstream{}
	h := newChatServer(repo, up)

	w := newNoFlushWriter()
	h.Chat(w, streamReq(context.Background(), `{"model":"qwen","message":"hi","stream":true}`, credWithPersona("key-1", "P")))

	if w.code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.code)
	}
	if !strings.Contains(w.body.String(), "internal_error") {
		t.Errorf("body = %s, want a JSON internal_error envelope", w.body.String())
	}
	if up.called {
		t.Error("upstream must not be called when streaming is impossible")
	}
	assertNothingPersisted(t, repo)
}

// If persistence fails after a fully delivered stream, the caller has already
// received the reply, so the failure is surfaced as a trailing SSE error frame.
func TestStreamChat_PersistFailure_EmitsTrailingSSEError(t *testing.T) {
	repo := newFakeConvRepo()
	repo.appendErr = errors.New("db write failed")
	up := &fakeUpstream{streamChunks: []string{sseChunk("done"), sseDone}}
	h := newChatServer(repo, up)

	fr := newFlushRecorder()
	h.Chat(fr, streamReq(context.Background(), `{"model":"qwen","message":"hi","stream":true}`, credWithPersona("key-1", "P")))

	body := fr.Body.String()
	if !strings.Contains(body, `"content":"done"`) || !strings.Contains(body, "data: [DONE]") {
		t.Errorf("the full reply should have been delivered before the save failure:\n%s", body)
	}
	if !strings.Contains(body, "event: error") || !strings.Contains(body, "could not be saved") {
		t.Errorf("a persist failure after delivery must emit a trailing SSE error frame:\n%s", body)
	}
	assertNothingPersisted(t, repo)
}

// assertNothingPersisted checks no message was written to any conversation.
func assertNothingPersisted(t *testing.T, repo *fakeConvRepo) {
	t.Helper()
	for id, msgs := range repo.msgs {
		if len(msgs) != 0 {
			t.Errorf("expected no persisted messages, got %v for %s", msgs, id)
		}
	}
}
