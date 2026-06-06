// Package e2e is the cross-cutting end-to-end proof the gateway works exactly as
// a real calling project would: the assembled stack (the real router/middleware
// from internal/app) over a throwaway Postgres, talking to a stub vLLM. It is
// gated on DAB_TEST_DATABASE_URL (skipped otherwise) and is the suite `make e2e`
// runs.
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/app"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/auth"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/config"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/dbtest"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/gateway"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/user"
)

const (
	testVLLMSecret = "test-vllm-shared-secret"
	testPersona    = "You are the DAB test persona."
	adminEmail     = "admin@dab.test"
	adminPassword  = "correct horse battery staple"
)

// --- stub vLLM ---------------------------------------------------------------

type stubMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// stubRequest is one upstream call the stub received, so the test can assert on
// exactly what the gateway SENT upstream (history + persona + auth header).
type stubRequest struct {
	auth     string
	messages []stubMessage
	stream   bool
	rawBody  string
}

// stubVLLM is an httptest server speaking the OpenAI chat-completions contract
// for both streaming and non-streaming, with knobs for canned content, per-chunk
// delay, and a forced error. It records every request it receives.
type stubVLLM struct {
	server *httptest.Server

	mu       sync.Mutex
	requests []stubRequest

	streamDeltas []string
	reply        string
	chunkDelay   time.Duration
	failStatus   int
}

func newStubVLLM() *stubVLLM {
	// chunkDelay spaces the streamed chunks with real wall-clock sleeps, so the
	// minimum first-chunk-to-[DONE] span is gated by the stub (here ~80ms for two
	// deltas). That gives the incremental-streaming assertion a wide margin over
	// its 20ms threshold, so it cannot flake short of the test reader being
	// starved for the whole span.
	s := &stubVLLM{streamDeltas: []string{"Hel", "lo"}, reply: "Hello", chunkDelay: 40 * time.Millisecond}
	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *stubVLLM) URL() string { return s.server.URL }
func (s *stubVLLM) Close()      { s.server.Close() }
func (s *stubVLLM) recorded() []stubRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stubRequest(nil), s.requests...)
}

func (s *stubVLLM) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var parsed struct {
		Stream   bool          `json:"stream"`
		Messages []stubMessage `json:"messages"`
	}
	_ = json.Unmarshal(body, &parsed)
	s.mu.Lock()
	s.requests = append(s.requests, stubRequest{
		auth: r.Header.Get("Authorization"), messages: parsed.Messages, stream: parsed.Stream, rawBody: string(body),
	})
	s.mu.Unlock()

	if s.failStatus > 0 {
		w.WriteHeader(s.failStatus)
		_, _ = io.WriteString(w, `{"error":{"message":"stub failure"}}`)
		return
	}

	if parsed.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, delta := range s.streamDeltas {
			frame, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"delta": map[string]string{"content": delta}}}})
			_, _ = io.WriteString(w, "data: "+string(frame)+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(s.chunkDelay) // space the chunks so the test can prove flush-through
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		return
	}

	resp, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": s.reply}}},
		"usage":   map[string]int{"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7},
	})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(resp)
}

// --- the assembled stack under test ------------------------------------------

type e2eEnv struct {
	gatewayURL string
	stub       *stubVLLM
	pool       *pgxpool.Pool
	keyID      string // dab_pk_...
	secret     string // dab_sk_...
}

// setupE2E boots the real router over a throwaway Postgres wired to a stub vLLM,
// bootstraps a superuser, logs in, and creates an API key WITH a persona —
// returning everything a calling project needs.
func setupE2E(t *testing.T) *e2eEnv {
	t.Helper()
	pool := dbtest.Pool(t)
	stub := newStubVLLM()
	t.Cleanup(stub.Close)

	cfg := config.Config{
		Env:              "test",
		VLLMURL:          stub.URL(),
		VLLMAPIKey:       testVLLMSecret,
		JWTSecret:        "test-jwt-secret",
		LoginRateLimit:   config.RateLimit{RequestsPerMinute: 0},
		GatewayRateLimit: config.RateLimit{RequestsPerMinute: 0},
	}
	srv := httptest.NewServer(app.BuildHandler(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), pool))
	t.Cleanup(srv.Close)

	// Bootstrap a superuser the way the createsuperuser CLI does (hash + Create).
	hash, err := auth.HashPassword(adminPassword)
	if err != nil {
		t.Fatalf("hashing password: %v", err)
	}
	if _, err := user.NewRepository(pool).Create(context.Background(), adminEmail, hash, true); err != nil {
		t.Fatalf("creating superuser: %v", err)
	}

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	// Admin login -> session cookie stored in the jar.
	login := mustPostJSON(t, client, srv.URL+"/api/admin/login", map[string]string{"email": adminEmail, "password": adminPassword})
	if login.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", login.StatusCode)
	}
	_ = login.Body.Close()

	// Create an API key WITH a persona; capture the one-time secret.
	created := mustPostJSON(t, client, srv.URL+"/api/admin/keys", map[string]any{"name": "e2e", "persona": testPersona})
	if created.StatusCode < 200 || created.StatusCode >= 300 {
		b, _ := io.ReadAll(created.Body)
		t.Fatalf("create key status = %d (%s)", created.StatusCode, b)
	}
	var key struct {
		KeyID  string `json:"key_id"`
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(created.Body).Decode(&key); err != nil {
		t.Fatalf("decoding key response: %v", err)
	}
	_ = created.Body.Close()
	if !strings.HasPrefix(key.KeyID, "dab_pk_") || !strings.HasPrefix(key.Secret, "dab_sk_") {
		t.Fatalf("unexpected key pair: id=%q secret-has-prefix=%v", key.KeyID, strings.HasPrefix(key.Secret, "dab_sk_"))
	}

	return &e2eEnv{gatewayURL: srv.URL, stub: stub, pool: pool, keyID: key.KeyID, secret: key.Secret}
}

// --- the flow ----------------------------------------------------------------

// The headline happy path: a streamed two-turn conversation proving the gateway
// issues the session id, streams incrementally, retains history + persona
// server-side, persists every turn, and injects the upstream secret while never
// forwarding the project keys.
func TestE2E_StreamingTwoTurnFlow(t *testing.T) {
	env := setupE2E(t)

	// Turn 1: no session id -> the gateway issues one.
	frames1, headers1 := env.streamChat(t, "", "qwen2.5", "Hello, my name is Ada.")
	session := headers1.Get(gateway.HeaderSessionID)
	if session == "" {
		t.Fatal("turn 1 did not return a gateway-issued session id")
	}
	assertIncremental(t, frames1)
	if got := assistantText(frames1); got != "Hello" {
		t.Errorf("turn-1 streamed reply = %q, want %q (concatenated deltas)", got, "Hello")
	}

	// Turn 2: reuse the session id, omit the model (inherited).
	_, _ = env.streamChat(t, session, "", "What is my name?")

	reqs := env.stub.recorded()
	if len(reqs) != 2 {
		t.Fatalf("stub received %d upstream requests, want 2", len(reqs))
	}

	// Security: every upstream call carries the shared bearer and never a project key.
	for i, r := range reqs {
		if r.auth != "Bearer "+testVLLMSecret {
			t.Errorf("upstream req %d Authorization = %q, want the shared bearer", i, r.auth)
		}
		if strings.Contains(r.rawBody, "dab_pk_") || strings.Contains(r.rawBody, "dab_sk_") {
			t.Errorf("upstream req %d leaked a project key in its body", i)
		}
	}

	// Turn 1 upstream: persona leads, then the first user turn.
	if got, want := roles(reqs[0].messages), []string{"system:" + testPersona, "user:Hello, my name is Ada."}; !equalSlices(got, want) {
		t.Errorf("turn-1 upstream messages = %v, want %v", got, want)
	}
	// Turn 2 upstream: persona + the FULL prior history + the new turn — server-side retention.
	want2 := []string{
		"system:" + testPersona,
		"user:Hello, my name is Ada.",
		"assistant:Hello",
		"user:What is my name?",
	}
	if got := roles(reqs[1].messages); !equalSlices(got, want2) {
		t.Errorf("turn-2 upstream messages = %v, want %v", got, want2)
	}

	// Persistence: both turns stored under the gateway-issued session id.
	want := []string{"user:Hello, my name is Ada.", "assistant:Hello", "user:What is my name?", "assistant:Hello"}
	if got := env.storedMessages(t, session); !equalSlices(got, want) {
		t.Errorf("persisted = %v, want %v", got, want)
	}

	// Audit: one usage row per call (the write is async, so allow it to settle).
	env.waitUsageRows(t, 2)
}

// An incomplete or wrong key pair is rejected with 401 before the request can
// reach the model.
func TestE2E_AuthNegativesRejectedBeforeUpstream(t *testing.T) {
	env := setupE2E(t)
	before := len(env.stub.recorded())

	cases := []struct{ name, keyID, secret string }{
		{"missing key id", "", env.secret},
		{"missing secret", env.keyID, ""},
		{"wrong secret", env.keyID, "dab_sk_wrongwrongwrong"},
		{"unknown key id", "dab_pk_doesnotexist", env.secret},
	}
	for _, c := range cases {
		body, _ := json.Marshal(map[string]any{"model": "qwen2.5", "message": "hi"})
		req, _ := http.NewRequest(http.MethodPost, env.gatewayURL+gateway.GatewayChatPath, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if c.keyID != "" {
			req.Header.Set(gateway.HeaderKeyID, c.keyID)
		}
		if c.secret != "" {
			req.Header.Set(gateway.HeaderSecret, c.secret)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", c.name, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}

	if after := len(env.stub.recorded()); after != before {
		t.Errorf("auth failures reached the stub: %d unexpected upstream requests", after-before)
	}
}

// --- helpers -----------------------------------------------------------------

type sseFrame struct {
	event string
	data  string
	at    time.Time
}

// streamChat POSTs a streaming gateway turn with the project's two keys and
// returns the SSE frames (with arrival times) plus the response headers.
func (e *e2eEnv) streamChat(t *testing.T, sessionID, model, message string) ([]sseFrame, http.Header) {
	t.Helper()
	body := map[string]any{"message": message, "stream": true}
	if sessionID != "" {
		body["session_id"] = sessionID
	}
	if model != "" {
		body["model"] = model
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, e.gatewayURL+gateway.GatewayChatPath, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(gateway.HeaderKeyID, e.keyID)
	req.Header.Set(gateway.HeaderSecret, e.secret)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("gateway stream request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		bb, _ := io.ReadAll(resp.Body)
		t.Fatalf("gateway stream status = %d (%s)", resp.StatusCode, bb)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	return readSSEFrames(resp.Body), resp.Header.Clone()
}

func readSSEFrames(body io.Reader) []sseFrame {
	reader := bufio.NewReader(body)
	var frames []sseFrame
	var cur sseFrame
	have := false
	for {
		line, err := reader.ReadString('\n')
		trimmed := strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(trimmed, "event:"):
			cur.event = strings.TrimSpace(trimmed[len("event:"):])
			have = true
		case strings.HasPrefix(trimmed, "data:"):
			cur.data = strings.TrimSpace(trimmed[len("data:"):])
			cur.at = time.Now()
			have = true
		case trimmed == "" && have:
			frames = append(frames, cur)
			cur = sseFrame{}
			have = false
		}
		if err != nil {
			break
		}
	}
	if have {
		frames = append(frames, cur)
	}
	return frames
}

// assertIncremental proves multiple discrete content frames arrived spaced out
// before [DONE] — i.e. the response was flushed through, not buffered into one
// blob.
func assertIncremental(t *testing.T, frames []sseFrame) {
	t.Helper()
	var firstContent, done time.Time
	content := 0
	for _, f := range frames {
		if f.event == "" && f.data != "" && f.data != "[DONE]" {
			if content == 0 {
				firstContent = f.at
			}
			content++
		}
		if f.data == "[DONE]" {
			done = f.at
		}
	}
	if content < 2 {
		t.Errorf("streamed content frames = %d, want >= 2 (discrete chunks)", content)
	}
	if done.Sub(firstContent) < 20*time.Millisecond {
		t.Errorf("content arrived in ~one blob (%v from first chunk to [DONE]); not incremental", done.Sub(firstContent))
	}
}

// assistantText reconstructs the assistant reply from the streamed content deltas.
func assistantText(frames []sseFrame) string {
	var sb strings.Builder
	for _, f := range frames {
		if f.event != "" || f.data == "" || f.data == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(f.data), &chunk) == nil && len(chunk.Choices) > 0 {
			sb.WriteString(chunk.Choices[0].Delta.Content)
		}
	}
	return sb.String()
}

func (e *e2eEnv) storedMessages(t *testing.T, session string) []string {
	t.Helper()
	rows, err := e.pool.Query(context.Background(),
		`SELECT role, content FROM messages WHERE conversation_id = $1 ORDER BY created_at, id`, session)
	if err != nil {
		t.Fatalf("querying stored messages: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var role, content string
		if err := rows.Scan(&role, &content); err != nil {
			t.Fatalf("scanning message: %v", err)
		}
		out = append(out, role+":"+content)
	}
	return out
}

// waitUsageRows polls for the expected gateway_usage count, since the audit write
// is asynchronous and best-effort.
func (e *e2eEnv) waitUsageRows(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var n int
		if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM gateway_usage`).Scan(&n); err != nil {
			t.Fatalf("counting usage rows: %v", err)
		}
		if n == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("gateway_usage rows = %d, want %d within 3s", n, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func mustPostJSON(t *testing.T, client *http.Client, url string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

func roles(msgs []stubMessage) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Role + ":" + m.Content
	}
	return out
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
