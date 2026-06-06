package gatewaycore

import (
	"context"
	"errors"
	"testing"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/conversation"
)

// fakeRepo is an in-memory ConvRepo for unit tests (no database).
type fakeRepo struct {
	convs     map[string]conversation.Conversation // by id
	histories map[string][]conversation.Message    // by conversation id
	appended  map[string][]conversation.NewMessage // captured appends
	appendErr error
	createID  string
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		convs:     map[string]conversation.Conversation{},
		histories: map[string][]conversation.Message{},
		appended:  map[string][]conversation.NewMessage{},
		createID:  "new-session-id",
	}
}

func (f *fakeRepo) CreateConversation(_ context.Context, apiKeyID, model string) (conversation.Conversation, error) {
	c := conversation.Conversation{ID: f.createID, APIKeyID: apiKeyID, Model: model}
	f.convs[c.ID] = c
	return c, nil
}

func (f *fakeRepo) GetConversation(_ context.Context, apiKeyID, sessionID string) (conversation.Conversation, error) {
	c, ok := f.convs[sessionID]
	if !ok || c.APIKeyID != apiKeyID {
		return conversation.Conversation{}, conversation.ErrNotFound
	}
	return c, nil
}

func (f *fakeRepo) LoadHistory(_ context.Context, conversationID string) ([]conversation.Message, error) {
	return f.histories[conversationID], nil
}

func (f *fakeRepo) AppendMessages(_ context.Context, conversationID string, msgs []conversation.NewMessage) error {
	if f.appendErr != nil {
		return f.appendErr
	}
	f.appended[conversationID] = append(f.appended[conversationID], msgs...)
	return nil
}

func storedMessages(roleContent ...string) []conversation.Message {
	var out []conversation.Message
	for i := 0; i+1 < len(roleContent); i += 2 {
		out = append(out, conversation.Message{Role: roleContent[i], Content: roleContent[i+1]})
	}
	return out
}

func contents(msgs []Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Role + ":" + m.Content
	}
	return out
}

func TestResolve_NewSessionWhenNoID(t *testing.T) {
	repo := newFakeRepo()
	svc := NewService(repo, 0)
	conv, err := svc.Resolve(context.Background(), "keyA", "", "qwen")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if conv.ID != "new-session-id" || conv.APIKeyID != "keyA" {
		t.Errorf("new session not issued correctly: %+v", conv)
	}
}

func TestResolve_ExistingScoped(t *testing.T) {
	repo := newFakeRepo()
	repo.convs["sess-1"] = conversation.Conversation{ID: "sess-1", APIKeyID: "keyA"}
	svc := NewService(repo, 0)

	if got, err := svc.Resolve(context.Background(), "keyA", "sess-1", ""); err != nil || got.ID != "sess-1" {
		t.Fatalf("owner Resolve = %+v, %v", got, err)
	}
	// Wrong owner → not-found.
	if _, err := svc.Resolve(context.Background(), "keyB", "sess-1", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-tenant Resolve = %v, want ErrNotFound", err)
	}
}

func TestAssembleContext_OrderAndPersonaFirst(t *testing.T) {
	repo := newFakeRepo()
	repo.histories["c1"] = storedMessages(RoleUser, "u1", RoleAssistant, "a1")
	svc := NewService(repo, 0)
	conv := conversation.Conversation{ID: "c1", APIKeyID: "keyA"}

	got, err := svc.AssembleContext(context.Background(), "PERSONA", conv, []Message{{Role: RoleUser, Content: "u2"}})
	if err != nil {
		t.Fatalf("AssembleContext: %v", err)
	}
	want := []string{"system:PERSONA", "user:u1", "assistant:a1", "user:u2"}
	if got := contents(got); !equalStrings(got, want) {
		t.Errorf("context = %v, want %v", got, want)
	}
}

func TestAssembleContext_NoPersonaOmitsSystem(t *testing.T) {
	repo := newFakeRepo()
	repo.histories["c1"] = storedMessages(RoleUser, "u1")
	svc := NewService(repo, 0)
	conv := conversation.Conversation{ID: "c1"}

	got, _ := svc.AssembleContext(context.Background(), "", conv, []Message{{Role: RoleUser, Content: "u2"}})
	if want := []string{"user:u1", "user:u2"}; !equalStrings(contents(got), want) {
		t.Errorf("context = %v, want %v (no system message)", contents(got), want)
	}
}

func TestAssembleContext_DropsEchoedTurn(t *testing.T) {
	repo := newFakeRepo()
	repo.histories["c1"] = storedMessages(RoleUser, "u1", RoleAssistant, "a1")
	svc := NewService(repo, 0)
	conv := conversation.Conversation{ID: "c1"}

	// Caller re-sends the last stored assistant turn plus a new user turn.
	got, _ := svc.AssembleContext(context.Background(), "P", conv, []Message{
		{Role: RoleAssistant, Content: "a1"},
		{Role: RoleUser, Content: "u2"},
	})
	want := []string{"system:P", "user:u1", "assistant:a1", "user:u2"}
	if !equalStrings(contents(got), want) {
		t.Errorf("context = %v, want %v (echoed turn must not double-count)", contents(got), want)
	}
}

func TestAssembleContext_TruncationKeepsPersonaAndRecent(t *testing.T) {
	repo := newFakeRepo()
	repo.histories["c1"] = storedMessages(
		RoleUser, "m1", RoleAssistant, "m2", RoleUser, "m3", RoleAssistant, "m4", RoleUser, "m5")
	svc := NewService(repo, 3) // cap of 3 non-persona messages
	conv := conversation.Conversation{ID: "c1"}

	got, _ := svc.AssembleContext(context.Background(), "P", conv, []Message{{Role: RoleAssistant, Content: "m6"}})
	// 5 history + 1 incoming = 6 > 3 → drop 3 oldest history; persona + incoming kept.
	want := []string{"system:P", "assistant:m4", "user:m5", "assistant:m6"}
	if !equalStrings(contents(got), want) {
		t.Errorf("truncated context = %v, want %v", contents(got), want)
	}
}

func TestAssembleContext_TruncationNeverDropsIncoming(t *testing.T) {
	repo := newFakeRepo()
	repo.histories["c1"] = storedMessages(RoleUser, "h1", RoleAssistant, "h2", RoleUser, "h3")
	svc := NewService(repo, 2)
	conv := conversation.Conversation{ID: "c1"}

	incoming := []Message{{Role: RoleUser, Content: "i1"}, {Role: RoleUser, Content: "i2"}, {Role: RoleUser, Content: "i3"}}
	got, _ := svc.AssembleContext(context.Background(), "", conv, incoming)
	// Incoming (3) exceeds the cap (2): all history dropped, incoming preserved.
	if want := []string{"user:i1", "user:i2", "user:i3"}; !equalStrings(contents(got), want) {
		t.Errorf("context = %v, want incoming preserved %v", contents(got), want)
	}
}

func TestPersistExchange_StoresTurnNotPersona(t *testing.T) {
	repo := newFakeRepo()
	svc := NewService(repo, 0)
	conv := conversation.Conversation{ID: "c1"}

	err := svc.PersistExchange(context.Background(), conv,
		[]Message{{Role: RoleUser, Content: "u"}},
		Message{Role: RoleAssistant, Content: "a"})
	if err != nil {
		t.Fatalf("PersistExchange: %v", err)
	}
	got := repo.appended["c1"]
	if len(got) != 2 || got[0].Content != "u" || got[1].Content != "a" {
		t.Fatalf("appended = %+v, want [user u, assistant a]", got)
	}
	for _, m := range got {
		if m.Role == RoleSystem {
			t.Error("persona/system message must never be persisted")
		}
	}
}

func TestPersistExchange_FailurePropagates(t *testing.T) {
	repo := newFakeRepo()
	repo.appendErr = errors.New("db down")
	svc := NewService(repo, 0)
	if err := svc.PersistExchange(context.Background(), conversation.Conversation{ID: "c1"},
		nil, Message{Role: RoleAssistant, Content: "a"}); err == nil {
		t.Error("PersistExchange should propagate the repo error")
	}
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
