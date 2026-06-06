// Package gatewaycore is the single transport-agnostic core both gateway
// handlers (non-streaming and streaming) consume, so context assembly, persona
// injection, history loading, truncation, tenant scoping, and atomic persistence
// are implemented once and cannot diverge.
//
// It contains no HTTP or vLLM types. Handlers translate its Message list to/from
// the upstream client.
package gatewaycore

import (
	"context"
	"errors"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/conversation"
)

// ErrNotFound is returned when a session does not belong to the calling
// credential (or does not exist). It wraps the repository's not-found.
var ErrNotFound = conversation.ErrNotFound

// Message roles.
const (
	RoleSystem    = conversation.RoleSystem
	RoleUser      = conversation.RoleUser
	RoleAssistant = conversation.RoleAssistant
)

// defaultMaxHistory caps how many non-persona messages (stored history + the
// incoming turn) are sent upstream when no positive cap is configured.
const defaultMaxHistory = 40

// Message is a role/content pair — the unit of assembled context. It is
// deliberately free of HTTP/vLLM and DB types.
type Message struct {
	Role    string
	Content string
}

// ConvRepo is the persistence the core needs; *conversation.Repository satisfies
// it. Defining it as an interface keeps the core unit-testable without a DB.
type ConvRepo interface {
	CreateConversation(ctx context.Context, apiKeyID, model string) (conversation.Conversation, error)
	GetConversation(ctx context.Context, apiKeyID, sessionID string) (conversation.Conversation, error)
	LoadHistory(ctx context.Context, conversationID string) ([]conversation.Message, error)
	AppendMessages(ctx context.Context, conversationID string, msgs []conversation.NewMessage) error
}

// Service assembles context and persists exchanges over a conversation repo.
type Service struct {
	repo       ConvRepo
	maxHistory int
}

// NewService builds the core. maxHistoryMessages caps the number of non-persona
// messages sent upstream (<= 0 uses the default).
func NewService(repo ConvRepo, maxHistoryMessages int) *Service {
	if maxHistoryMessages <= 0 {
		maxHistoryMessages = defaultMaxHistory
	}
	return &Service{repo: repo, maxHistory: maxHistoryMessages}
}

// Resolve returns the conversation for this turn. With an empty sessionID the
// gateway ISSUES a new session (CreateConversation); otherwise it fetches the
// session scoped to apiKeyID — a wrong owner or missing session returns
// ErrNotFound. The returned conversation's ID is the session id to hand back to
// the caller.
func (s *Service) Resolve(ctx context.Context, apiKeyID, sessionID, model string) (conversation.Conversation, error) {
	if sessionID == "" {
		return s.repo.CreateConversation(ctx, apiKeyID, model)
	}
	conv, err := s.repo.GetConversation(ctx, apiKeyID, sessionID)
	if err != nil {
		if errors.Is(err, conversation.ErrNotFound) {
			return conversation.Conversation{}, ErrNotFound
		}
		return conversation.Conversation{}, err
	}
	return conv, nil
}

// AssembleContext builds the upstream message list, in order:
//
//  1. the persona as the leading system message (when non-empty) — always
//     present and always preserved under truncation,
//  2. the stored history (oldest-first),
//  3. the incoming turn(s).
//
// Incoming messages that merely echo the tail of history are dropped so a turn
// is not double-counted. When the non-persona message count exceeds the cap, the
// oldest history messages are dropped first; the persona and the incoming turn
// are never dropped.
func (s *Service) AssembleContext(ctx context.Context, persona string, conv conversation.Conversation, incoming []Message) ([]Message, error) {
	stored, err := s.repo.LoadHistory(ctx, conv.ID)
	if err != nil {
		return nil, err
	}

	history := make([]Message, len(stored))
	for i, m := range stored {
		history[i] = Message{Role: m.Role, Content: m.Content}
	}

	incoming = dropOverlap(history, incoming)
	history = capHistory(history, incoming, s.maxHistory)

	out := make([]Message, 0, len(history)+len(incoming)+1)
	if persona != "" {
		out = append(out, Message{Role: RoleSystem, Content: persona})
	}
	out = append(out, history...)
	out = append(out, incoming...)
	return out, nil
}

// PersistExchange atomically appends the new user message(s) and the assistant
// reply in one transaction. The persona is never stored — it comes from the
// credential each call. Callers persist ONLY on a successful upstream response;
// on failure they simply do not call this, so nothing is stored.
func (s *Service) PersistExchange(ctx context.Context, conv conversation.Conversation, userMsgs []Message, assistant Message) error {
	msgs := make([]conversation.NewMessage, 0, len(userMsgs)+1)
	for _, m := range userMsgs {
		msgs = append(msgs, conversation.NewMessage{Role: m.Role, Content: m.Content})
	}
	msgs = append(msgs, conversation.NewMessage{Role: assistant.Role, Content: assistant.Content})
	return s.repo.AppendMessages(ctx, conv.ID, msgs)
}

// dropOverlap removes the longest prefix of incoming that exactly matches the
// equal-length suffix of history, so a caller that re-sends the last stored
// turn(s) does not double-count them.
func dropOverlap(history, incoming []Message) []Message {
	maxK := len(history)
	if len(incoming) < maxK {
		maxK = len(incoming)
	}
	for k := maxK; k > 0; k-- {
		if equalMessages(history[len(history)-k:], incoming[:k]) {
			return incoming[k:]
		}
	}
	return incoming
}

// capHistory drops the oldest history messages so the total non-persona count
// (history + incoming) fits maxHistory, but never drops the incoming turn.
func capHistory(history, incoming []Message, maxHistory int) []Message {
	over := len(history) + len(incoming) - maxHistory
	if over <= 0 {
		return history
	}
	if over > len(history) {
		over = len(history)
	}
	return history[over:]
}

func equalMessages(a, b []Message) bool {
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
