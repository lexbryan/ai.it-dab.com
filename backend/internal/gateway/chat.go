package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/gatewaycore"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/vllm"
)

// GatewayChatPath is the public non-streaming chat endpoint.
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

// upstreamCompleter is the slice of the vLLM client the handler needs.
// *vllm.Client satisfies it.
type upstreamCompleter interface {
	Complete(ctx context.Context, req vllm.ChatRequest) (*vllm.ChatResponse, error)
}

// ChatHandler serves the non-streaming gateway endpoint. It owns no context or
// persistence logic — it delegates all of it to the shared core so it cannot
// diverge from the streaming path.
type ChatHandler struct {
	core     *gatewaycore.Service
	upstream upstreamCompleter
}

// NewChatHandler builds the handler over the shared core and an upstream client.
func NewChatHandler(core *gatewaycore.Service, upstream upstreamCompleter) *ChatHandler {
	return &ChatHandler{core: core, upstream: upstream}
}

// RegisterChatRoutes mounts the non-streaming gateway endpoint behind the
// two-key auth middleware.
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

// Chat handles a non-streaming chat turn: it resolves (or issues) the session,
// assembles persona + history + the new turn via the core, calls vLLM, and on
// success persists the exchange atomically and returns the assistant reply with
// the gateway-issued session id.
func (h *ChatHandler) Chat(w http.ResponseWriter, r *http.Request) {
	cred, ok := CredentialFromContext(r.Context())
	if !ok {
		// The auth middleware should make this unreachable; fail closed.
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	var req chatRequestBody
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxChatBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is not valid JSON")
		return
	}
	if req.Stream {
		writeError(w, http.StatusBadRequest, "invalid_request", "streaming is not supported at this endpoint")
		return
	}

	incoming, ok := parseIncoming(w, req)
	if !ok {
		return
	}

	sessionID := strings.TrimSpace(req.SessionID)
	model := strings.TrimSpace(req.Model)
	if sessionID == "" && model == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "model is required for a new conversation")
		return
	}

	ctx := r.Context()
	conv, err := h.core.Resolve(ctx, cred.ID, sessionID, model)
	if err != nil {
		if errors.Is(err, gatewaycore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "session not found")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "unavailable", "could not load the conversation")
		return
	}

	effModel := model
	if effModel == "" {
		effModel = conv.Model
	}
	if effModel == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "model is required")
		return
	}

	persona := ""
	if cred.Persona != nil {
		persona = *cred.Persona
	}
	assembled, err := h.core.AssembleContext(ctx, persona, conv, incoming)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "could not assemble context")
		return
	}

	resp, err := h.upstream.Complete(ctx, vllm.ChatRequest{
		Model:       effModel,
		Messages:    toVLLMMessages(assembled),
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   req.MaxTokens,
		Stop:        req.Stop,
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
	if err := h.core.PersistExchange(ctx, conv, incoming, assistant); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not save the conversation")
		return
	}

	writeJSON(w, http.StatusOK, chatResponseBody{
		SessionID: conv.ID,
		Model:     effModel,
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
