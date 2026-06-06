package vllm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/config"
)

const testAPIKey = "vllm-shared-secret"

func clientFor(url string) *Client {
	return New(config.Config{VLLMURL: url, VLLMAPIKey: testAPIKey})
}

func sampleRequest() ChatRequest {
	return ChatRequest{
		Model: "qwen",
		Messages: []Message{
			{Role: "system", Content: "you are a persona"},
			{Role: "user", Content: "hello"},
		},
	}
}

// TestComplete_InjectsAuthNeverForwardsProjectKeys checks the core security
// invariants: every request carries the shared bearer, and no project key
// (dab_pk_/dab_sk_) ever appears in the upstream request.
func TestComplete_InjectsAuthNeverForwardsProjectKeys(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		if r.URL.Path != chatCompletionsPath {
			t.Errorf("path = %q, want %q", r.URL.Path, chatCompletionsPath)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","model":"qwen","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
	}))
	defer srv.Close()

	resp, err := clientFor(srv.URL).Complete(context.Background(), sampleRequest())
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotAuth != "Bearer "+testAPIKey {
		t.Errorf("Authorization = %q, want bearer with the configured key", gotAuth)
	}
	if strings.Contains(gotBody, "dab_pk_") || strings.Contains(gotBody, "dab_sk_") {
		t.Error("upstream request must never contain a project key")
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "hi" {
		t.Errorf("decoded response wrong: %+v", resp)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 4 {
		t.Errorf("usage not decoded: %+v", resp.Usage)
	}
}

func TestComplete_UpstreamErrorMapping(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusInternalServerError} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			_, _ = io.WriteString(w, `{"error":{"message":"upstream said no","type":"boom"}}`)
		}))

		_, err := clientFor(srv.URL).Complete(context.Background(), sampleRequest())
		srv.Close()

		var ue *UpstreamError
		if !errors.As(err, &ue) {
			t.Fatalf("status %d: error = %v, want *UpstreamError", code, err)
		}
		if ue.StatusCode != code {
			t.Errorf("UpstreamError.StatusCode = %d, want %d", ue.StatusCode, code)
		}
		if strings.Contains(ue.Error(), testAPIKey) {
			t.Error("error must not contain the upstream secret")
		}
	}
}

// TestStream_IncrementalReader proves the streaming body is handed back unread:
// the first SSE chunk is readable before the upstream finishes the response.
func TestStream_IncrementalReader(t *testing.T) {
	release := make(chan struct{})
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		f, ok := w.(http.Flusher)
		if !ok {
			t.Error("stub needs a flusher")
			return
		}
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
		f.Flush()
		select {
		case <-release:
		case <-time.After(2 * time.Second):
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		f.Flush()
	}))
	defer srv.Close()
	defer close(release)

	stream, err := clientFor(srv.URL).Stream(context.Background(), sampleRequest())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer func() { _ = stream.Body.Close() }()

	if gotAuth != "Bearer "+testAPIKey {
		t.Errorf("stream Authorization = %q, want configured bearer", gotAuth)
	}

	// Read the first SSE line before releasing the stub; it must arrive while the
	// upstream is still blocked, proving the body is not fully buffered.
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		line, err := bufio.NewReader(stream.Body).ReadString('\n')
		done <- result{line, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("reading first chunk: %v", r.err)
		}
		if !strings.Contains(r.line, `"content":"a"`) {
			t.Errorf("first chunk = %q, want the first delta", r.line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first chunk did not arrive before the stream completed -> body was buffered")
	}
}

func TestStream_PreStreamErrorIsNormalError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":{"message":"no upstream"}}`)
	}))
	defer srv.Close()

	stream, err := clientFor(srv.URL).Stream(context.Background(), sampleRequest())
	if stream != nil {
		_ = stream.Body.Close()
		t.Fatal("a pre-stream error must not return a half-open stream")
	}
	var ue *UpstreamError
	if !errors.As(err, &ue) || ue.StatusCode != http.StatusBadGateway {
		t.Fatalf("Stream error = %v, want *UpstreamError 502", err)
	}
}

func TestComplete_SendsStreamFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if s, ok := req["stream"]; ok && s == true {
			t.Error("Complete must not request streaming")
		}
		_, _ = io.WriteString(w, `{"id":"x","choices":[]}`)
	}))
	defer srv.Close()
	if _, err := clientFor(srv.URL).Complete(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("Complete: %v", err)
	}
}
