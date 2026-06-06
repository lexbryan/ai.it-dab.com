package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/conversation"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/gatewaycore"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/vllm"
)

// fakeConvRepo is an in-memory gatewaycore.ConvRepo mirroring the real repo's
// tenant-scoped semantics, so the handler can be tested with the REAL core but
// no database.
type fakeConvRepo struct {
	seq       int
	convs     map[string]conversation.Conversation
	msgs      map[string][]conversation.Message
	appendErr error
}

func newFakeConvRepo() *fakeConvRepo {
	return &fakeConvRepo{convs: map[string]conversation.Conversation{}, msgs: map[string][]conversation.Message{}}
}

func (f *fakeConvRepo) CreateConversation(_ context.Context, apiKeyID, model string) (conversation.Conversation, error) {
	f.seq++
	c := conversation.Conversation{ID: "conv-" + strconv.Itoa(f.seq), APIKeyID: apiKeyID, Model: model}
	f.convs[c.ID] = c
	return c, nil
}

func (f *fakeConvRepo) GetConversation(_ context.Context, apiKeyID, sessionID string) (conversation.Conversation, error) {
	c, ok := f.convs[sessionID]
	if !ok || c.APIKeyID != apiKeyID {
		return conversation.Conversation{}, conversation.ErrNotFound
	}
	return c, nil
}

func (f *fakeConvRepo) LoadHistory(_ context.Context, conversationID string) ([]conversation.Message, error) {
	return f.msgs[conversationID], nil
}

func (f *fakeConvRepo) AppendMessages(_ context.Context, conversationID string, msgs []conversation.NewMessage) error {
	if f.appendErr != nil {
		return f.appendErr
	}
	for _, m := range msgs {
		f.msgs[conversationID] = append(f.msgs[conversationID], conversation.Message{ConversationID: conversationID, Role: m.Role, Content: m.Content})
	}
	return nil
}

// seed installs a conversation with prior history.
func (f *fakeConvRepo) seed(id, apiKeyID, model string, history ...conversation.Message) {
	f.convs[id] = conversation.Conversation{ID: id, APIKeyID: apiKeyID, Model: model}
	f.msgs[id] = history
}

type fakeUpstream struct {
	got    vllm.ChatRequest
	called bool
	resp   *vllm.ChatResponse
	err    error

	// Streaming knobs, exercised by the SSE tests in stream_test.go.
	streamErr      error                               // pre-stream error from Stream
	streamChunks   []string                            // raw SSE bytes served by the default body
	streamBodyFunc func(context.Context) io.ReadCloser // custom, ctx-aware body (disconnect/timing tests)
}

func (f *fakeUpstream) Complete(_ context.Context, req vllm.ChatRequest) (*vllm.ChatResponse, error) {
	f.got = req
	f.called = true
	if f.err != nil {
		return nil, f.err
	}
	if f.resp != nil {
		return f.resp, nil
	}
	return &vllm.ChatResponse{
		Choices: []vllm.Choice{{Message: vllm.Message{Role: "assistant", Content: "hi there"}}},
		Usage:   &vllm.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
	}, nil
}

func (f *fakeUpstream) Stream(ctx context.Context, req vllm.ChatRequest) (*vllm.StreamResponse, error) {
	f.got = req
	f.called = true
	if f.streamErr != nil {
		return nil, f.streamErr
	}
	var body io.ReadCloser
	switch {
	case f.streamBodyFunc != nil:
		body = f.streamBodyFunc(ctx)
	default:
		body = io.NopCloser(strings.NewReader(strings.Join(f.streamChunks, "")))
	}
	return &vllm.StreamResponse{Body: body, Header: http.Header{}}, nil
}

func newChatServer(repo *fakeConvRepo, up *fakeUpstream) *ChatHandler {
	return NewChatHandler(gatewaycore.NewService(repo, 0), up)
}

func chatReq(body string, cred Credential) *http.Request {
	r := httptest.NewRequest(http.MethodPost, GatewayChatPath, strings.NewReader(body))
	return r.WithContext(withCredential(r.Context(), cred))
}

func credWithPersona(id, persona string) Credential {
	return Credential{ID: id, KeyID: "dab_pk_" + id, Persona: &persona}
}

func roleContents(msgs []vllm.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Role + ":" + m.Content
	}
	return out
}

func TestChat_FirstCall_IssuesSessionInjectsPersonaAndPersists(t *testing.T) {
	repo := newFakeConvRepo()
	up := &fakeUpstream{}
	h := newChatServer(repo, up)

	rr := httptest.NewRecorder()
	h.Chat(rr, chatReq(`{"model":"qwen","message":"hello"}`, credWithPersona("key-1", "You are helpful.")))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body)
	}
	var resp chatResponseBody
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.SessionID == "" {
		t.Error("first call must return a gateway-issued session id")
	}
	if resp.Message.Role != "assistant" || resp.Message.Content != "hi there" {
		t.Errorf("reply = %+v, want assistant/hi there", resp.Message)
	}
	// Token usage from upstream is forwarded to the caller.
	if resp.Usage == nil || resp.Usage.TotalTokens != 5 {
		t.Errorf("usage = %+v, want TotalTokens 5", resp.Usage)
	}

	// Persona leads, then the new user turn — in order — and the model is forwarded.
	if got, want := roleContents(up.got.Messages), []string{"system:You are helpful.", "user:hello"}; !equalStrings(got, want) {
		t.Errorf("upstream messages = %v, want %v", got, want)
	}
	if up.got.Model != "qwen" {
		t.Errorf("upstream model = %q, want qwen", up.got.Model)
	}
	// Exactly the user turn + assistant reply were persisted.
	if got, want := storedRoleContents(repo, resp.SessionID), []string{"user:hello", "assistant:hi there"}; !equalStrings(got, want) {
		t.Errorf("persisted = %v, want %v", got, want)
	}
}

func TestChat_FollowUp_SendsPriorHistoryAndPersona(t *testing.T) {
	repo := newFakeConvRepo()
	repo.seed("conv-X", "key-1", "qwen",
		conversation.Message{Role: "user", Content: "h1"},
		conversation.Message{Role: "assistant", Content: "a1"},
	)
	up := &fakeUpstream{}
	h := newChatServer(repo, up)

	rr := httptest.NewRecorder()
	h.Chat(rr, chatReq(`{"session_id":"conv-X","message":"h2"}`, credWithPersona("key-1", "P")))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body)
	}
	want := []string{"system:P", "user:h1", "assistant:a1", "user:h2"}
	if got := roleContents(up.got.Messages); !equalStrings(got, want) {
		t.Errorf("upstream messages = %v, want %v", got, want)
	}
	// The follow-up omitted model and inherits the conversation's stored model.
	if up.got.Model != "qwen" {
		t.Errorf("follow-up upstream model = %q, want inherited qwen", up.got.Model)
	}
	// The follow-up appends only the new turn + reply (history is not re-stored).
	if got, want := storedRoleContents(repo, "conv-X"), []string{"user:h1", "assistant:a1", "user:h2", "assistant:hi there"}; !equalStrings(got, want) {
		t.Errorf("persisted = %v, want %v", got, want)
	}
}

// A caller that re-sends the last stored turn (a common chat-completions pattern)
// must not have it stored twice: assembly and persistence stay in lockstep.
func TestChat_ReSentTailIsNotDoublePersisted(t *testing.T) {
	repo := newFakeConvRepo()
	repo.seed("conv-X", "key-1", "qwen",
		conversation.Message{Role: "user", Content: "h1"},
		conversation.Message{Role: "assistant", Content: "a1"},
	)
	up := &fakeUpstream{}
	h := newChatServer(repo, up)

	rr := httptest.NewRecorder()
	// messages re-send the stored tail (a1) plus a genuinely new user turn (h2).
	h.Chat(rr, chatReq(`{"session_id":"conv-X","messages":[{"role":"assistant","content":"a1"},{"role":"user","content":"h2"}]}`, credWithPersona("key-1", "P")))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body)
	}
	// Upstream gets the deduplicated context...
	if got, want := roleContents(up.got.Messages), []string{"system:P", "user:h1", "assistant:a1", "user:h2"}; !equalStrings(got, want) {
		t.Errorf("upstream messages = %v, want %v", got, want)
	}
	// ...and storage gains exactly the new turn + reply — a1 is stored once, not twice.
	if got, want := storedRoleContents(repo, "conv-X"), []string{"user:h1", "assistant:a1", "user:h2", "assistant:hi there"}; !equalStrings(got, want) {
		t.Errorf("persisted = %v, want %v (the re-sent a1 must not be duplicated)", got, want)
	}
}

func TestChat_MessagesArrayMultiTurnIsSentAndPersistedInOrder(t *testing.T) {
	repo := newFakeConvRepo()
	up := &fakeUpstream{}
	h := newChatServer(repo, up)

	rr := httptest.NewRecorder()
	h.Chat(rr, chatReq(`{"model":"qwen","messages":[{"role":"user","content":"q1"},{"role":"assistant","content":"draft"},{"role":"user","content":"q2"}]}`, credWithPersona("key-1", "P")))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body)
	}
	var resp chatResponseBody
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if got, want := roleContents(up.got.Messages), []string{"system:P", "user:q1", "assistant:draft", "user:q2"}; !equalStrings(got, want) {
		t.Errorf("upstream messages = %v, want %v", got, want)
	}
	if got, want := storedRoleContents(repo, resp.SessionID), []string{"user:q1", "assistant:draft", "user:q2", "assistant:hi there"}; !equalStrings(got, want) {
		t.Errorf("persisted = %v, want %v", got, want)
	}
}

func TestChat_PersistFailureReturns500(t *testing.T) {
	repo := newFakeConvRepo()
	repo.appendErr = errors.New("db write failed")
	up := &fakeUpstream{}
	h := newChatServer(repo, up)

	rr := httptest.NewRecorder()
	h.Chat(rr, chatReq(`{"model":"qwen","message":"hello"}`, credWithPersona("key-1", "P")))

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("persist-failure status = %d, want 500", rr.Code)
	}
	if !up.called {
		t.Error("upstream should have been called before the persist failure")
	}
}

func TestChat_CrossTenantSessionIsNotFoundAndDoesNotCallUpstream(t *testing.T) {
	repo := newFakeConvRepo()
	repo.seed("conv-X", "key-A", "qwen")
	up := &fakeUpstream{}
	h := newChatServer(repo, up)

	rr := httptest.NewRecorder()
	h.Chat(rr, chatReq(`{"session_id":"conv-X","message":"hi"}`, credWithPersona("key-B", "P")))

	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-tenant status = %d, want 404", rr.Code)
	}
	if up.called {
		t.Error("upstream must not be called for a non-owned session")
	}
}

func TestChat_UpstreamErrorPersistsNothing(t *testing.T) {
	repo := newFakeConvRepo()
	up := &fakeUpstream{err: &vllm.UpstreamError{StatusCode: 500, Message: "boom"}}
	h := newChatServer(repo, up)

	rr := httptest.NewRecorder()
	h.Chat(rr, chatReq(`{"model":"qwen","message":"hello"}`, credWithPersona("key-1", "P")))

	if rr.Code != http.StatusBadGateway {
		t.Errorf("upstream-error status = %d, want 502", rr.Code)
	}
	// The error is sanitized: it never echoes the upstream message ("boom") or
	// status, only the generic envelope type.
	body := rr.Body.String()
	if strings.Contains(body, "boom") || strings.Contains(body, "500") {
		t.Errorf("error body leaked upstream detail: %s", body)
	}
	var env struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil || env.Error.Type != "upstream_error" {
		t.Errorf("error envelope = %s (type %q), want type upstream_error", body, env.Error.Type)
	}
	for id := range repo.msgs {
		if len(repo.msgs[id]) != 0 {
			t.Errorf("no messages should be persisted on upstream error, got %v for %s", repo.msgs[id], id)
		}
	}
}

func TestChat_Validation(t *testing.T) {
	cases := map[string]string{
		"no message or messages":    `{"model":"q"}`,
		"new conversation no model": `{"message":"hi"}`,
		"system role rejected":      `{"model":"q","messages":[{"role":"system","content":"x"}]}`,
		"blank content":             `{"model":"q","messages":[{"role":"user","content":"   "}]}`,
		"unknown field":             `{"model":"q","message":"hi","nope":1}`,
		"malformed json":            `not-json`,
	}
	for name, body := range cases {
		repo := newFakeConvRepo()
		up := &fakeUpstream{}
		h := newChatServer(repo, up)
		rr := httptest.NewRecorder()
		h.Chat(rr, chatReq(body, credWithPersona("key-1", "P")))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rr.Code)
		}
		if up.called {
			t.Errorf("%s: upstream must not be called on a rejected request", name)
		}
	}
}

func TestChat_RequiresCredentialInContext(t *testing.T) {
	h := newChatServer(newFakeConvRepo(), &fakeUpstream{})
	// No credential in context (handler called directly without the middleware).
	r := httptest.NewRequest(http.MethodPost, GatewayChatPath, strings.NewReader(`{"model":"q","message":"hi"}`))
	rr := httptest.NewRecorder()
	h.Chat(rr, r)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("missing credential status = %d, want 401", rr.Code)
	}
}

func TestRegisterChatRoutes_GuardedByTwoKeyAuth(t *testing.T) {
	store := newFakeStore()
	authn := NewAuthenticator(store)
	mux := http.NewServeMux()
	RegisterChatRoutes(mux, authn, newChatServer(newFakeConvRepo(), &fakeUpstream{}))

	// No credential headers -> the two-key middleware rejects before the handler.
	r := httptest.NewRequest(http.MethodPost, GatewayChatPath, strings.NewReader(`{"model":"q","message":"hi"}`))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, r)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated gateway call status = %d, want 401", rr.Code)
	}
}

func storedRoleContents(repo *fakeConvRepo, convID string) []string {
	msgs := repo.msgs[convID]
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Role + ":" + m.Content
	}
	return out
}

func equalStrings(a, b []string) bool {
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
