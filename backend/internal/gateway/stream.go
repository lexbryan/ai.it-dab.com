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
// exchange is persisted through the SAME core as the non-streaming path, and
// every terminal outcome records exactly one content-free audit row.
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
		// caller still receives a normal JSON error. This should be unreachable in
		// production (the middleware chain preserves http.Flusher); the call was
		// accepted and assembled, so it is still audited.
		writeError(w, errInternal, "streaming is not supported by the server")
		h.finish(r, p.usageRecord(outcomeInternalError, nil, nil, nil), 0)
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
		writeError(w, errUpstream, "the model backend could not start the stream")
		h.finish(r, p.usageRecord(outcomeUpstreamError, upstreamStatusOf(err), nil, nil), 0)
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

	assistant, completed, chunks := relayStream(ctx, w, flusher, stream.Body)
	if !completed {
		// Disconnect or mid-stream error: relayStream already emitted an SSE error
		// frame for a stream error (none for a disconnect). Persist nothing, but
		// still audit the call. The stream opened with a 200.
		outcome := outcomeUpstreamError
		if ctx.Err() != nil {
			outcome = outcomeClientDisconnect
		}
		h.finish(r, p.usageRecord(outcome, intPtr(http.StatusOK), nil, nil), chunks)
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
		writeSSEError(w, errInternal, "the reply was delivered but could not be saved")
		flusher.Flush()
		h.finish(r, p.usageRecord(outcomePersistError, intPtr(http.StatusOK), nil, nil), chunks)
		return
	}
	h.finish(r, p.usageRecord(outcomeSuccess, intPtr(http.StatusOK), nil, nil), chunks)
}

// relayStream copies the upstream SSE body to the client line-by-line, flushing
// after every write so chunks are never buffered, while accumulating the
// assistant content deltas to reconstruct the final message. It returns the
// reconstructed assistant message, whether the stream completed (saw `[DONE]`),
// and the number of upstream data frames relayed. On a premature end it returns
// completed=false: a client disconnect (ctx canceled) emits no frame; any other
// truncation emits an SSE error frame.
func relayStream(ctx context.Context, w io.Writer, flusher http.Flusher, body io.Reader) (gatewaycore.Message, bool, int) {
	var content strings.Builder
	chunks := 0
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
					return gatewaycore.Message{Role: gatewaycore.RoleAssistant, Content: content.String()}, true, chunks
				}
				chunks++
				content.WriteString(deltaContent(payload))
			}
		}
		if err != nil {
			// The stream ended without [DONE]: a truncation or read error. Notify
			// the client with an SSE error frame ONLY if it is still connected — a
			// disconnect cancels the request context and there is no one left to
			// receive a frame. Either way nothing is persisted.
			if ctx.Err() == nil {
				writeSSEError(w, errUpstream, "the model backend ended the stream unexpectedly")
				flusher.Flush()
			}
			return gatewaycore.Message{}, false, chunks
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
