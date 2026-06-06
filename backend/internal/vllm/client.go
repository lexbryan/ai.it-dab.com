// Package vllm is the internal HTTP client for the self-hosted vLLM
// OpenAI-compatible API — the Gateway → vLLM hop.
//
// It injects the shared upstream secret (VLLM_API_KEY) as a bearer token on
// every request. Project two-key credentials are never forwarded upstream, and
// the VLLM_API_KEY is never returned downstream or placed in an error or log.
//
// Two call paths share one configured *http.Client:
//   - Complete: non-streaming; decodes the chat-completion response.
//   - Stream: sets stream=true and returns the raw upstream body unread, so the
//     gateway handler can pass SSE chunks through unbuffered.
//
// The client sets a connect/TLS and response-header timeout so a dead upstream
// fails fast, but deliberately sets no overall client timeout — that would cut
// a long-lived stream. Per-call cancelation is via the context.
package vllm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/config"
)

const chatCompletionsPath = "/v1/chat/completions"

// errorBodyLimit bounds how much of an upstream error body we read before
// discarding, so a hostile/huge error response cannot exhaust memory.
const errorBodyLimit = 8 << 10

// Message is one chat message in the OpenAI format.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is a chat-completion request. The gateway core fills Messages
// (persona + history + new turn); this client only transports it and the
// passed-through sampling parameters.
type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Stream      bool      `json:"stream,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	TopP        *float64  `json:"top_p,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
	Stop        []string  `json:"stop,omitempty"`
}

// Usage is the token accounting from a non-streaming response.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Choice is one completion choice.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// ChatResponse is a decoded non-streaming chat completion.
type ChatResponse struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage"`
}

// StreamResponse carries the raw, still-open upstream SSE body. The caller reads
// it incrementally and MUST Close it.
type StreamResponse struct {
	Body   io.ReadCloser
	Header http.Header
}

// UpstreamError is a sanitized error for a non-2xx upstream response. It carries
// the status code and a short message, never the VLLM_API_KEY or upstream URL.
type UpstreamError struct {
	StatusCode int
	Message    string
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("vllm: upstream returned %d: %s", e.StatusCode, e.Message)
}

// Client talks to one vLLM endpoint with a fixed bearer secret.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// New builds a Client from configuration.
func New(cfg config.Config) *Client {
	return &Client{
		baseURL: strings.TrimRight(cfg.VLLMURL, "/"),
		apiKey:  cfg.VLLMAPIKey,
		http: &http.Client{
			// No overall Timeout: it would truncate a long stream. Connect, TLS,
			// and time-to-first-header are bounded instead; the body is bounded
			// by the caller's context.
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 60 * time.Second,
				MaxIdleConns:          100,
				IdleConnTimeout:       90 * time.Second,
			},
		},
	}
}

// Complete performs a non-streaming chat completion and decodes the response. A
// non-2xx upstream status is returned as an *UpstreamError.
func (c *Client) Complete(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	req.Stream = false
	resp, err := c.do(ctx, req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if err := upstreamStatusError(resp); err != nil {
		return nil, err
	}

	var out ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("vllm: decoding response: %w", err)
	}
	return &out, nil
}

// Stream performs a streaming chat completion and returns the still-open body so
// the caller can pass SSE chunks through unbuffered. A pre-stream non-2xx status
// is returned as an *UpstreamError (the body is closed); the caller never sees a
// half-open stream on error.
func (c *Client) Stream(ctx context.Context, req ChatRequest) (*StreamResponse, error) {
	req.Stream = true
	resp, err := c.do(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := upstreamStatusError(resp); err != nil {
		_ = resp.Body.Close()
		return nil, err
	}
	return &StreamResponse{Body: resp.Body, Header: resp.Header}, nil
}

// do marshals req and issues the authenticated POST. It never logs the body or
// the bearer token.
func (c *Client) do(ctx context.Context, req ChatRequest) (*http.Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("vllm: encoding request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+chatCompletionsPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("vllm: building request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	// The shared upstream secret — injected on every request, never logged.
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		// http errors can embed the request URL but never the Authorization
		// header, so the secret cannot leak here.
		return nil, fmt.Errorf("vllm: request failed: %w", err)
	}
	return resp, nil
}

// upstreamStatusError returns an *UpstreamError for a non-2xx response, reading a
// bounded amount of the body for a short sanitized message. On 2xx it returns
// nil and leaves the body untouched.
func upstreamStatusError(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
	return &UpstreamError{StatusCode: resp.StatusCode, Message: sanitizeMessage(snippet)}
}

// sanitizeMessage extracts a short, safe message from an upstream error body,
// preferring an OpenAI-style {"error":{"message":...}} field and otherwise a
// generic note. It never echoes secrets or large bodies.
func sanitizeMessage(body []byte) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		if parsed.Error.Message != "" {
			return truncate(parsed.Error.Message, 200)
		}
		if parsed.Error.Type != "" {
			return truncate(parsed.Error.Type, 200)
		}
	}
	return "upstream error"
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}
