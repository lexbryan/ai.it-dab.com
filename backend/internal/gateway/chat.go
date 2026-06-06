package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/conversation"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/gatewaycore"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/vllm"
)

// GatewayChatPath is the public chat endpoint. A single endpoint serves both the
// non-streaming and the SSE-streaming paths; `stream` in the body selects which.
const GatewayChatPath = "/v1/gateway/chat"

// maxChatBody bounds the request body. A request carries only the new turn(s)
// (history is server-side), so this is generous; the unified validation ticket
// may tighten it.
const maxChatBody = 1 << 20

// RouteRegistrar is the route-registration seam, satisfied by *httpserver.Router
// and *http.ServeMux.
type RouteRegistrar interface {
	Handle(pattern string, handler http.Handler)
}

// upstreamClient is the slice of the vLLM client the gateway needs: the
// non-streaming Complete and the streaming Stream. *vllm.Client satisfies it.
type upstreamClient interface {
	Complete(ctx context.Context, req vllm.ChatRequest) (*vllm.ChatResponse, error)
	Stream(ctx context.Context, req vllm.ChatRequest) (*vllm.StreamResponse, error)
}

// ChatHandler serves the gateway endpoint. It owns no context or persistence
// logic — it delegates all of it to the shared core so the streaming and
// non-streaming paths cannot diverge.
type ChatHandler struct {
	core     *gatewaycore.Service
	upstream upstreamClient
}

// NewChatHandler builds the handler over the shared core and an upstream client.
func NewChatHandler(core *gatewaycore.Service, upstream upstreamClient) *ChatHandler {
	return &ChatHandler{core: core, upstream: upstream}
}

// RegisterChatRoutes mounts the gateway endpoint behind the two-key auth
// middleware.
func RegisterChatRoutes(mux RouteRegistrar, authn *Authenticator, h *ChatHandler) {
	mux.Handle("POST "+GatewayChatPath, authn.RequireCredential(http.HandlerFunc(h.Chat)))
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequestBody struct {
	SessionID   string        `json:"session_id"`
	Model       string        `json:"model"`
	Message     string        `json:"message"`  // convenience single user turn
	Messages    []chatMessage `json:"messages"` // explicit turn(s)
	Stream      bool          `json:"stream"`
	Temperature *float64      `json:"temperature"`
	TopP        *float64      `json:"top_p"`
	MaxTokens   *int          `json:"max_tokens"`
	Stop        []string      `json:"stop"`
}

type chatResponseBody struct {
	SessionID string      `json:"session_id"`
	Model     string      `json:"model"`
	Message   chatMessage `json:"message"`
	Usage     *vllm.Usage `json:"usage,omitempty"`
}

// Chat handles a chat turn on the single public gateway endpoint. It assembles
// persona + history + the new turn via the shared core, then dispatches to the
// non-streaming or the SSE-streaming path based on `stream`. Both paths share the
// same preparation and the same core, so context assembly and persistence cannot
// diverge between them.
func (h *ChatHandler) Chat(w http.ResponseWriter, r *http.Request) {
	p, ok := h.prepareTurn(w, r)
	if !ok {
		return
	}
	if p.req.Stream {
		h.streamChat(w, r, p)
		return
	}
	h.completeChat(w, r, p)
}

// preparedTurn is the result of validating a request and assembling its upstream
// context: everything both paths need to call vLLM and persist the exchange.
type preparedTurn struct {
	conv      conversation.Conversation
	incoming  []gatewaycore.Message
	assembled []gatewaycore.Message
	effModel  string
	req       chatRequestBody
}

// prepareTurn authenticates, decodes and validates the request, resolves (or
// issues) the session, and assembles persona + full history + the new turn
// through the shared core. It writes the appropriate error and returns ok=false
// on any failure, so the streaming and non-streaming paths share identical
// validation and context assembly.
func (h *ChatHandler) prepareTurn(w http.ResponseWriter, r *http.Request) (*preparedTurn, bool) {
	cred, ok := CredentialFromContext(r.Context())
	if !ok {
		// The auth middleware should make this unreachable; fail closed.
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return nil, false
	}

	var req chatRequestBody
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxChatBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is not valid JSON")
		return nil, false
	}

	incoming, ok := parseIncoming(w, req)
	if !ok {
		return nil, false
	}

	sessionID := strings.TrimSpace(req.SessionID)
	model := strings.TrimSpace(req.Model)
	if sessionID == "" && model == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "model is required for a new conversation")
		return nil, false
	}

	ctx := r.Context()
	conv, err := h.core.Resolve(ctx, cred.ID, sessionID, model)
	if err != nil {
		if errors.Is(err, gatewaycore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "session not found")
			return nil, false
		}
		writeError(w, http.StatusServiceUnavailable, "unavailable", "could not load the conversation")
		return nil, false
	}

	effModel := model
	if effModel == "" {
		effModel = conv.Model
	}
	if effModel == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "model is required")
		return nil, false
	}

	persona := ""
	if cred.Persona != nil {
		persona = *cred.Persona
	}
	assembled, err := h.core.AssembleContext(ctx, persona, conv, incoming)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "could not assemble context")
		return nil, false
	}

	return &preparedTurn{conv: conv, incoming: incoming, assembled: assembled, effModel: effModel, req: req}, true
}

// completeChat runs the non-streaming path: one upstream call, then on success
// the new user turn(s) + assistant reply are persisted atomically and the reply
// is returned with the gateway-issued session id. Nothing is persisted on any
// upstream failure.
func (h *ChatHandler) completeChat(w http.ResponseWriter, r *http.Request, p *preparedTurn) {
	resp, err := h.upstream.Complete(r.Context(), vllm.ChatRequest{
		Model:       p.effModel,
		Messages:    toVLLMMessages(p.assembled),
		Temperature: p.req.Temperature,
		TopP:        p.req.TopP,
		MaxTokens:   p.req.MaxTokens,
		Stop:        p.req.Stop,
	})
	if err != nil {
		// Nothing is persisted on any upstream failure. The error is sanitized;
		// we never echo the upstream body, URL, or the VLLM_API_KEY.
		writeError(w, http.StatusBadGateway, "upstream_error", "the model backend could not complete the request")
		return
	}
	if len(resp.Choices) == 0 {
		writeError(w, http.StatusBadGateway, "upstream_error", "the model backend returned no completion")
		return
	}

	assistant := gatewaycore.Message{Role: gatewaycore.RoleAssistant, Content: resp.Choices[0].Message.Content}
	if err := h.core.PersistExchange(r.Context(), p.conv, p.incoming, assistant); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not save the conversation")
		return
	}

	writeJSON(w, http.StatusOK, chatResponseBody{
		SessionID: p.conv.ID,
		Model:     p.effModel,
		Message:   chatMessage{Role: assistant.Role, Content: assistant.Content},
		Usage:     resp.Usage,
	})
}

// parseIncoming validates and builds the incoming turn(s) from the request. It
// writes a 400 and returns ok=false on any problem. The caller never sends a
// system message — the persona is server-side — so a system role is rejected.
func parseIncoming(w http.ResponseWriter, req chatRequestBody) ([]gatewaycore.Message, bool) {
	if len(req.Messages) > 0 {
		out := make([]gatewaycore.Message, 0, len(req.Messages))
		for _, m := range req.Messages {
			role := strings.TrimSpace(m.Role)
			if role != gatewaycore.RoleUser && role != gatewaycore.RoleAssistant {
				writeError(w, http.StatusBadRequest, "invalid_request", "each message role must be 'user' or 'assistant'")
				return nil, false
			}
			if strings.TrimSpace(m.Content) == "" {
				writeError(w, http.StatusBadRequest, "invalid_request", "each message must have non-empty content")
				return nil, false
			}
			out = append(out, gatewaycore.Message{Role: role, Content: m.Content})
		}
		return out, true
	}
	if strings.TrimSpace(req.Message) != "" {
		return []gatewaycore.Message{{Role: gatewaycore.RoleUser, Content: req.Message}}, true
	}
	writeError(w, http.StatusBadRequest, "invalid_request", "a message or a non-empty messages array is required")
	return nil, false
}

func toVLLMMessages(msgs []gatewaycore.Message) []vllm.Message {
	out := make([]vllm.Message, len(msgs))
	for i, m := range msgs {
		out[i] = vllm.Message{Role: m.Role, Content: m.Content}
	}
	return out
}
