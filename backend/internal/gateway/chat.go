package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/conversation"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/gatewaycore"
	applog "github.com/lexbryan/ai.it-dab.com/backend/internal/log"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/usage"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/vllm"
)

// GatewayChatPath is the public chat endpoint. A single endpoint serves both the
// non-streaming and the SSE-streaming paths; `stream` in the body selects which.
const GatewayChatPath = "/v1/gateway/chat"

// maxChatBody bounds the request body. A request carries only the new turn(s)
// (history is server-side), so this is generous while still rejecting an
// oversized payload with a structured error.
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
// non-streaming paths cannot diverge — and emits a content-free audit row plus
// one structured log line per call.
type ChatHandler struct {
	core     *gatewaycore.Service
	upstream upstreamClient
	usage    usage.Recorder // best-effort audit; nil disables auditing
}

// NewChatHandler builds the handler over the shared core, an upstream client, and
// a best-effort usage recorder (nil disables auditing).
func NewChatHandler(core *gatewaycore.Service, upstream upstreamClient, recorder usage.Recorder) *ChatHandler {
	return &ChatHandler{core: core, upstream: upstream, usage: recorder}
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
	start := time.Now()
	p, ok := h.prepareTurn(w, r)
	if !ok {
		// Validation/auth rejections never reached the model, so they are not
		// audited as LLM calls.
		return
	}
	p.start = start
	if p.req.Stream {
		h.streamChat(w, r, p)
		return
	}
	h.completeChat(w, r, p)
}

// preparedTurn is the result of validating a request and assembling its upstream
// context: everything both paths need to call vLLM, persist the exchange, and
// audit the call.
type preparedTurn struct {
	cred      Credential
	conv      conversation.Conversation
	incoming  []gatewaycore.Message
	assembled []gatewaycore.Message
	effModel  string
	req       chatRequestBody
	start     time.Time
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
		writeError(w, errUnauthorized, "authentication required")
		return nil, false
	}

	var req chatRequestBody
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxChatBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, errInvalidRequest, "request body too large")
			return nil, false
		}
		writeError(w, errInvalidRequest, "request body is not valid JSON or contains unknown fields")
		return nil, false
	}

	incoming, ok := parseIncoming(w, req)
	if !ok {
		return nil, false
	}

	sessionID := strings.TrimSpace(req.SessionID)
	model := strings.TrimSpace(req.Model)
	if sessionID == "" && model == "" {
		writeError(w, errInvalidRequest, "model is required for a new conversation")
		return nil, false
	}

	ctx := r.Context()
	conv, err := h.core.Resolve(ctx, cred.ID, sessionID, model)
	if err != nil {
		if errors.Is(err, gatewaycore.ErrNotFound) {
			writeError(w, errNotFound, "session not found")
			return nil, false
		}
		writeError(w, errUnavailable, "could not load the conversation")
		return nil, false
	}

	effModel := model
	if effModel == "" {
		effModel = conv.Model
	}
	if effModel == "" {
		writeError(w, errInvalidRequest, "model is required")
		return nil, false
	}

	persona := ""
	if cred.Persona != nil {
		persona = *cred.Persona
	}
	assembled, err := h.core.AssembleContext(ctx, persona, conv, incoming)
	if err != nil {
		writeError(w, errUnavailable, "could not assemble context")
		return nil, false
	}

	return &preparedTurn{cred: cred, conv: conv, incoming: incoming, assembled: assembled, effModel: effModel, req: req}, true
}

// completeChat runs the non-streaming path: one upstream call, then on success
// the new user turn(s) + assistant reply are persisted atomically and the reply
// is returned with the gateway-issued session id. Every terminal outcome records
// exactly one content-free audit row. Nothing is persisted on an upstream failure.
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
		writeError(w, errUpstream, "the model backend could not complete the request")
		h.finish(r, p.usageRecord(outcomeUpstreamError, upstreamStatusOf(err), nil, nil), 0)
		return
	}
	if len(resp.Choices) == 0 {
		writeError(w, errUpstream, "the model backend returned no completion")
		h.finish(r, p.usageRecord(outcomeUpstreamError, intPtr(http.StatusOK), nil, nil), 0)
		return
	}

	promptTokens, completionTokens := tokenCounts(resp.Usage)
	assistant := gatewaycore.Message{Role: gatewaycore.RoleAssistant, Content: resp.Choices[0].Message.Content}
	if err := h.core.PersistExchange(r.Context(), p.conv, p.incoming, assistant); err != nil {
		writeError(w, errInternal, "could not save the conversation")
		h.finish(r, p.usageRecord(outcomePersistError, intPtr(http.StatusOK), promptTokens, completionTokens), 0)
		return
	}

	writeJSON(w, http.StatusOK, chatResponseBody{
		SessionID: p.conv.ID,
		Model:     p.effModel,
		Message:   chatMessage{Role: assistant.Role, Content: assistant.Content},
		Usage:     resp.Usage,
	})
	h.finish(r, p.usageRecord(outcomeSuccess, intPtr(http.StatusOK), promptTokens, completionTokens), 0)
}

// Audit outcomes recorded per call.
const (
	outcomeSuccess          = "success"
	outcomeUpstreamError    = "upstream_error"
	outcomePersistError     = "persist_error"
	outcomeClientDisconnect = "client_disconnect"
	outcomeInternalError    = "internal_error"
)

// usageRecord builds the content-free audit record for this turn with the given
// outcome, upstream status, and (optional) token counts. It carries only counts,
// the outcome, and the measured latency — never message content.
func (p *preparedTurn) usageRecord(outcome string, upstreamStatus, promptTokens, completionTokens *int) usage.Record {
	return usage.Record{
		APIKeyID:             p.cred.ID,
		ConversationID:       p.conv.ID,
		Model:                p.effModel,
		Stream:               p.req.Stream,
		PromptMsgCount:       len(p.assembled),
		PromptTokenCount:     promptTokens,
		CompletionTokenCount: completionTokens,
		UpstreamStatus:       upstreamStatus,
		Outcome:              outcome,
		LatencyMS:            int(time.Since(p.start).Milliseconds()),
	}
}

// finish records the per-call audit row (best-effort, on a detached context so a
// disconnect cannot abort it) and emits exactly one structured observability log
// line. Both carry only counts, the outcome, and timings — never message content
// or secrets. The request-scoped logger already carries the correlation id, which
// the base middleware also echoes in the X-Request-Id response header.
func (h *ChatHandler) finish(r *http.Request, rec usage.Record, chunkCount int) {
	if h.usage != nil {
		_ = h.usage.Record(context.WithoutCancel(r.Context()), rec)
	}
	applog.FromContext(r.Context()).LogAttrs(r.Context(), slog.LevelInfo, "gateway call",
		slog.String("api_key_id", rec.APIKeyID),
		slog.String("conversation_id", rec.ConversationID),
		slog.String("model", rec.Model),
		slog.Bool("stream", rec.Stream),
		slog.String("outcome", rec.Outcome),
		slog.Int("prompt_msg_count", rec.PromptMsgCount),
		slog.Int("chunk_count", chunkCount),
		slog.Int("latency_ms", rec.LatencyMS),
	)
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
				writeError(w, errInvalidRequest, "each message role must be 'user' or 'assistant'")
				return nil, false
			}
			if strings.TrimSpace(m.Content) == "" {
				writeError(w, errInvalidRequest, "each message must have non-empty content")
				return nil, false
			}
			out = append(out, gatewaycore.Message{Role: role, Content: m.Content})
		}
		return out, true
	}
	if strings.TrimSpace(req.Message) != "" {
		return []gatewaycore.Message{{Role: gatewaycore.RoleUser, Content: req.Message}}, true
	}
	writeError(w, errInvalidRequest, "a message or a non-empty messages array is required")
	return nil, false
}

func toVLLMMessages(msgs []gatewaycore.Message) []vllm.Message {
	out := make([]vllm.Message, len(msgs))
	for i, m := range msgs {
		out[i] = vllm.Message{Role: m.Role, Content: m.Content}
	}
	return out
}

// intPtr returns a pointer to n, for the nullable audit fields.
func intPtr(n int) *int { return &n }

// upstreamStatusOf extracts the HTTP status from a typed upstream error, or nil
// when the failure carried no upstream response (e.g. a transport error).
func upstreamStatusOf(err error) *int {
	var ue *vllm.UpstreamError
	if errors.As(err, &ue) {
		return intPtr(ue.StatusCode)
	}
	return nil
}

// tokenCounts splits an upstream usage block into nullable prompt/completion
// counts, returning nils when the upstream reported no usage.
func tokenCounts(u *vllm.Usage) (prompt, completion *int) {
	if u == nil {
		return nil, nil
	}
	return intPtr(u.PromptTokens), intPtr(u.CompletionTokens)
}
