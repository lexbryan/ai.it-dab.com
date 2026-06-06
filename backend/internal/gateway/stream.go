package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/gatewaycore"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/vllm"
)

// HeaderSessionID carries the gateway-issued session id on a streaming response,
// so a caller can capture it from the headers in addition to the leading SSE
// `session` frame.
const HeaderSessionID = "X-DAB-Session-Id"

// ssePersistTimeout bounds the detached write that saves a completed streamed
// exchange. The write runs on a context derived WITHOUT the request's
// cancelation so a client that closes the connection the instant the stream
// finishes does not cost us the persisted exchange.
const ssePersistTimeout = 10 * time.Second

// sseDonePayload is the OpenAI/vLLM stream terminator: `data: [DONE]`.
const sseDonePayload = "[DONE]"

// streamChat runs the SSE streaming path. It proxies vLLM's event stream
// straight through to the client, flushing after every chunk so nothing is
// buffered, while reconstructing the assistant message in memory. The completed
// exchange is persisted through the SAME core as the non-streaming path, so the
// two cannot diverge.
//
// Persistence policy (explicit): the exchange is persisted ONLY after the
// upstream sends `data: [DONE]`. If the client disconnects or the upstream
// stream errors before [DONE], NOTHING is persisted — a partial turn is treated
// as never having happened, so stored history can never hold a truncated reply.
//
// Error policy: a PRE-stream upstream error is returned as the normal JSON error
// envelope (never a half-open SSE). A MID-stream failure is signalled as an SSE
// `event: error` frame and persists nothing. A client disconnect cancels the
// upstream call (the body is closed by the deferred Close) and emits no frame —
// the client is already gone.
func (h *ChatHandler) streamChat(w http.ResponseWriter, r *http.Request, p *preparedTurn) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// Without flushing we cannot stream. Fail before any SSE is written so the
		// caller still receives a normal JSON error.
		writeError(w, http.StatusInternalServerError, "internal_error", "streaming is not supported by the server")
		return
	}

	ctx := r.Context()
	stream, err := h.upstream.Stream(ctx, vllm.ChatRequest{
		Model:       p.effModel,
		Messages:    toVLLMMessages(p.assembled),
		Temperature: p.req.Temperature,
		TopP:        p.req.TopP,
		MaxTokens:   p.req.MaxTokens,
		Stop:        p.req.Stop,
	})
	if err != nil {
		// Pre-stream failure: no SSE written yet, so answer with the JSON error
		// envelope. The message is sanitized — never the upstream body or secret.
		writeError(w, http.StatusBadGateway, "upstream_error", "the model backend could not start the stream")
		return
	}
	defer func() { _ = stream.Body.Close() }()

	// Commit to streaming: SSE headers, then the leading session frame, so the
	// caller learns its gateway-issued session id before any model content.
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no") // ask reverse proxies (nginx) not to buffer
	header.Set(HeaderSessionID, p.conv.ID)
	w.WriteHeader(http.StatusOK)
	writeSSESession(w, p.conv.ID)
	flusher.Flush()

	assistant, completed := relayStream(ctx, w, flusher, stream.Body)
	if !completed {
		// Disconnect or mid-stream error: relayStream already emitted an SSE error
		// frame for a stream error (none for a disconnect). Persist nothing.
		return
	}

	// Clean completion ([DONE] seen): persist through the shared core on a
	// detached context so a client that closed the connection at the very end
	// cannot cost us the finished exchange.
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ssePersistTimeout)
	defer cancel()
	if err := h.core.PersistExchange(persistCtx, p.conv, p.incoming, assistant); err != nil {
		// The caller already received the full reply; we can only signal the save
		// failure as a trailing SSE error frame.
		writeSSEError(w, "internal_error", "the reply was delivered but could not be saved")
		flusher.Flush()
	}
}

// relayStream copies the upstream SSE body to the client line-by-line, flushing
// after every write so chunks are never buffered, while accumulating the
// assistant content deltas to reconstruct the final message. It returns the
// reconstructed assistant message and whether the stream completed (saw
// `[DONE]`). On a premature end it returns completed=false: a client disconnect
// (ctx canceled) emits no frame; any other truncation emits an SSE error frame.
func relayStream(ctx context.Context, w io.Writer, flusher http.Flusher, body io.Reader) (gatewaycore.Message, bool) {
	var content strings.Builder
	reader := bufio.NewReader(body)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			// Pass the raw bytes through unchanged (preserving SSE framing) and
			// flush immediately so the client sees this chunk now. A line without a
			// trailing newline only occurs when the upstream is truncated mid-frame;
			// close the frame so a following error frame stays a separate,
			// well-formed SSE event rather than running onto the partial one.
			_, _ = w.Write(line)
			if line[len(line)-1] != '\n' {
				_, _ = io.WriteString(w, "\n\n")
			}
			flusher.Flush()
			if payload, ok := sseData(line); ok {
				if payload == sseDonePayload {
					return gatewaycore.Message{Role: gatewaycore.RoleAssistant, Content: content.String()}, true
				}
				content.WriteString(deltaContent(payload))
			}
		}
		if err != nil {
			// The stream ended without [DONE]: a truncation or read error. Notify
			// the client with an SSE error frame ONLY if it is still connected — a
			// disconnect cancels the request context and there is no one left to
			// receive a frame. Either way nothing is persisted.
			if ctx.Err() == nil {
				writeSSEError(w, "upstream_error", "the model backend ended the stream unexpectedly")
				flusher.Flush()
			}
			return gatewaycore.Message{}, false
		}
	}
}

// sseData reports whether line is an SSE `data:` line and, if so, returns its
// payload with the `data:` prefix, surrounding whitespace, and trailing newline
// removed.
func sseData(line []byte) (string, bool) {
	s := strings.TrimRight(string(line), "\r\n")
	if !strings.HasPrefix(s, "data:") {
		return "", false
	}
	return strings.TrimSpace(s[len("data:"):]), true
}

// deltaContent extracts the assistant content delta from one streamed chunk. It
// returns "" for the role-only first chunk, a parse failure, or a chunk with no
// choices, so non-content frames never corrupt the reconstructed message.
func deltaContent(payload string) string {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil || len(chunk.Choices) == 0 {
		return ""
	}
	return chunk.Choices[0].Delta.Content
}

// writeSSESession emits the leading `session` frame carrying the gateway-issued
// session id.
func writeSSESession(w io.Writer, sessionID string) {
	payload, _ := json.Marshal(map[string]string{"session_id": sessionID})
	_, _ = io.WriteString(w, "event: session\ndata: ")
	_, _ = w.Write(payload)
	_, _ = io.WriteString(w, "\n\n")
}

// writeSSEError emits an `event: error` frame using the same envelope shape as
// the JSON error contract: {"error":{"type","message"}}.
func writeSSEError(w io.Writer, errType, message string) {
	payload, _ := json.Marshal(map[string]any{"error": map[string]string{"type": errType, "message": message}})
	_, _ = io.WriteString(w, "event: error\ndata: ")
	_, _ = w.Write(payload)
	_, _ = io.WriteString(w, "\n\n")
}
