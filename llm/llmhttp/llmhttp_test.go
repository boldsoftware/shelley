package llmhttp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/llm"
)

func requireIdleStall(t *testing.T, err error) llm.RequestErrorInfo {
	t.Helper()
	info, ok := llm.RequestErrorInfoFromError(err)
	if !ok || info.IdleStallDuration <= 0 {
		t.Fatalf("error = %v, want idle-stall request metadata", err)
	}
	if !info.Retryable {
		t.Fatalf("idle-stall error metadata = %+v, want retryable", info)
	}
	return info
}

func TestContextFunctions(t *testing.T) {
	ctx := t.Context()

	// Test ConversationID
	ctx = WithConversationID(ctx, "conv-123")
	if got := ConversationIDFromContext(ctx); got != "conv-123" {
		t.Errorf("ConversationIDFromContext() = %q, want %q", got, "conv-123")
	}
	if got := PromptCacheKeyFromContext(ctx); got != "conv-123" {
		t.Errorf("PromptCacheKeyFromContext() = %q, want conversation fallback", got)
	}
	ctx = WithPromptCacheKey(ctx, "shared-prefix")
	if got := PromptCacheKeyFromContext(ctx); got != "shared-prefix" {
		t.Errorf("PromptCacheKeyFromContext() = %q, want explicit key", got)
	}

	// Test ModelID
	ctx = WithModelID(ctx, "model-456")
	if got := ModelIDFromContext(ctx); got != "model-456" {
		t.Errorf("ModelIDFromContext() = %q, want %q", got, "model-456")
	}

	// Test Provider
	ctx = WithProvider(ctx, "anthropic")
	if got := ProviderFromContext(ctx); got != "anthropic" {
		t.Errorf("ProviderFromContext() = %q, want %q", got, "anthropic")
	}

	ctx = WithOpenAIProtocol(ctx)
	if !IsOpenAIProtocol(ctx) {
		t.Error("IsOpenAIProtocol() = false, want true")
	}

	// Test empty context
	emptyCtx := t.Context()
	if IsOpenAIProtocol(emptyCtx) {
		t.Error("IsOpenAIProtocol(empty) = true, want false")
	}
	if got := ConversationIDFromContext(emptyCtx); got != "" {
		t.Errorf("ConversationIDFromContext(empty) = %q, want empty", got)
	}
	if got := ModelIDFromContext(emptyCtx); got != "" {
		t.Errorf("ModelIDFromContext(empty) = %q, want empty", got)
	}
	if got := ProviderFromContext(emptyCtx); got != "" {
		t.Errorf("ProviderFromContext(empty) = %q, want empty", got)
	}
}

func TestTransportAddsHeaders(t *testing.T) {
	// Create a test server that echoes request headers
	var receivedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	client := NewClient(nil)

	// Make a request with conversation ID in context
	ctx := WithConversationID(t.Context(), "test-conv-id")
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	resp.Body.Close()

	// Verify User-Agent header was added
	if !strings.HasPrefix(receivedHeaders.Get("User-Agent"), "Shelley") {
		t.Errorf("User-Agent = %q, want prefix 'Shelley'", receivedHeaders.Get("User-Agent"))
	}

	// Verify Shelley-Conversation-Id header was added
	if got := receivedHeaders.Get("Shelley-Conversation-Id"); got != "test-conv-id" {
		t.Errorf("Shelley-Conversation-Id = %q, want %q", got, "test-conv-id")
	}

	// Verify x-session-affinity is NOT added for non-fireworks providers
	if got := receivedHeaders.Get("x-session-affinity"); got != "" {
		t.Errorf("x-session-affinity = %q, want empty for non-fireworks", got)
	}
	if got := receivedHeaders.Get("session-id"); got != "" {
		t.Errorf("session-id = %q, want empty without OpenAI protocol", got)
	}
}

// TestTransportProviderCacheAffinityHeaders covers the per-provider prompt-cache
// affinity headers: x-session-affinity for Fireworks, and session-id for
// OpenAI-compatible requests regardless of provider name.
func TestTransportProviderCacheAffinityHeaders(t *testing.T) {
	var receivedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
	}))
	defer server.Close()

	tests := []struct {
		name                                 string
		provider, sessionAffinity, sessionID string
		openAI                               bool
	}{
		{"fireworks", "fireworks", "shared-prefix", "", true},
		{"openai", "openai", "", "shared-prefix", true},
		{"custom-openai", "zen-go", "", "shared-prefix", true},
		{"unnamed-openai", "", "", "shared-prefix", true},
		{"provider-name-alone", "openai", "", "", false},
		{"unknown-protocol", "zen-go", "", "", false},
		{"anthropic", "anthropic", "", "", false},
		{"gemini", "gemini", "", "", false},
		{"builtin", "builtin", "", "", false},
		{"unnamed", "", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := WithProvider(WithPromptCacheKey(t.Context(), "shared-prefix"), tt.provider)
			if tt.openAI {
				ctx = WithOpenAIProtocol(ctx)
			}
			req, _ := http.NewRequestWithContext(ctx, "POST", server.URL, nil)
			resp, err := NewClient(nil).Do(req)
			if err != nil {
				t.Fatalf("Request failed: %v", err)
			}
			resp.Body.Close()
			if got := receivedHeaders.Get("x-session-affinity"); got != tt.sessionAffinity {
				t.Errorf("x-session-affinity = %q, want %q", got, tt.sessionAffinity)
			}
			if got := receivedHeaders.Get("session-id"); got != tt.sessionID {
				t.Errorf("session-id = %q, want %q", got, tt.sessionID)
			}
		})
	}
}

// flushWrite writes s to w and flushes it so the client can read it before the
// handler returns.
func flushWrite(t *testing.T, w http.ResponseWriter, s string) {
	t.Helper()
	if _, err := io.WriteString(w, s); err != nil {
		t.Logf("write: %v", err)
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// TestIdleTimeoutFiresOnStall verifies that a stream that goes silent longer
// than the idle timeout is aborted with an idle-timeout error, and that the
// error identifies the timeout duration.
func TestIdleTimeoutFiresOnStall(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flushWrite(t, w, "data: hello\n\n")
		// Go silent until the test releases us (well past the idle timeout).
		<-release
	}))
	defer server.Close()
	defer close(release)

	client := NewClientWithIdleTimeout(nil, 100*time.Millisecond)
	req, _ := http.NewRequest("GET", server.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	_, err = io.ReadAll(resp.Body)
	if err == nil {
		t.Fatalf("expected idle-timeout error reading body, got nil")
	}
	info := requireIdleStall(t, err)
	if info.IdleStallDuration != 100*time.Millisecond {
		t.Errorf("idle stall duration = %s, want 100ms", info.IdleStallDuration)
	}
	if !strings.Contains(err.Error(), "100ms") {
		t.Errorf("error %q does not mention the idle timeout duration", err.Error())
	}
}

// TestIdleTimeoutDoesNotFireWhileStreaming verifies that a stream that keeps
// sending data — even for far longer than the idle window — is not aborted, as
// long as each gap between chunks stays under the idle timeout.
func TestIdleTimeoutDoesNotFireWhileStreaming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Send 10 chunks, each well within the idle window. Total elapsed
		// (~250ms) exceeds the 80ms idle timeout, proving the timer resets
		// on progress rather than capping total duration.
		for i := 0; i < 10; i++ {
			flushWrite(t, w, "data: chunk\n\n")
			time.Sleep(25 * time.Millisecond)
		}
		flushWrite(t, w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewClientWithIdleTimeout(nil, 80*time.Millisecond)
	req, _ := http.NewRequest("GET", server.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v (idle timeout fired despite steady progress)", err)
	}
	if !strings.Contains(string(body), "[DONE]") {
		t.Fatalf("did not read full stream, got %q", body)
	}
}

// TestIdleTimeoutFiresBeforeFirstByte verifies the idle timer also covers the
// time-to-first-byte window (headers sent, then silence).
func TestIdleTimeoutFiresBeforeFirstByte(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release
	}))
	defer server.Close()
	defer close(release)

	client := NewClientWithIdleTimeout(nil, 100*time.Millisecond)
	req, _ := http.NewRequest("GET", server.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		// Some stacks deliver the stall as a RoundTrip error instead of a body
		// read error; either is acceptable as long as it has idle-stall metadata.
		info := requireIdleStall(t, err)
		if info.IdleStallDuration != 100*time.Millisecond {
			t.Errorf("idle stall duration = %s, want 100ms", info.IdleStallDuration)
		}
		return
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err != nil {
		requireIdleStall(t, err)
	} else {
		t.Fatal("ReadAll error = nil, want idle-stall request metadata")
	}
}

// TestNoIdleTimeoutWhenDisabled verifies that idle timeout of 0 disables the
// mechanism entirely.
func TestNoIdleTimeoutWhenDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "ok")
	}))
	defer server.Close()

	client := NewClientWithIdleTimeout(nil, 0)
	req, _ := http.NewRequest("GET", server.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("body = %q, want ok", body)
	}
}

// TestRequestTraceCapturesIDs verifies that the Transport sets a Shelley
// request-id header and captures the upstream provider request id into the
// RequestTrace on the context.
func TestRequestTraceCapturesIDs(t *testing.T) {
	var gotShelleyID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotShelleyID = r.Header.Get("Shelley-Request-Id")
		w.Header().Set("X-Request-Id", "req_upstream_123")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "ok")
	}))
	defer server.Close()

	client := NewClient(nil)
	ctx, trace := llm.WithRequestTrace(t.Context())
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	if gotShelleyID == "" {
		t.Fatalf("server did not receive a Shelley-Request-Id header")
	}
	if trace.Value("shelley_request_id") != gotShelleyID {
		t.Errorf("trace ShelleyRequestID = %q, want %q", trace.Value("shelley_request_id"), gotShelleyID)
	}
	if trace.Value("upstream_request_id") != "req_upstream_123" {
		t.Errorf("trace UpstreamRequestID = %q, want req_upstream_123", trace.Value("upstream_request_id"))
	}
	if s := trace.String(); !strings.Contains(s, gotShelleyID) || !strings.Contains(s, "req_upstream_123") {
		t.Errorf("trace String = %q, want both ids", s)
	}
}

// TestRequestTraceHasShelleyIDOnStall verifies that even when a stream stalls
// (no readable body / upstream id), the RequestTrace still carries the
// Shelley-generated id so a user-facing error can reference it.
func TestRequestTraceHasShelleyIDOnStall(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release
	}))
	defer server.Close()
	defer close(release)

	client := NewClientWithIdleTimeout(nil, 100*time.Millisecond)
	ctx, trace := llm.WithRequestTrace(t.Context())
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	resp, err := client.Do(req)
	if err == nil {
		_, err = io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	requireIdleStall(t, err)
	if trace.Value("shelley_request_id") == "" {
		t.Fatalf("trace missing Shelley request id after stall")
	}
}

// TestRequestTraceHonorsExistingID verifies that a pre-set Shelley-Request-Id
// header is preserved rather than overwritten.
func TestRequestTraceHonorsExistingID(t *testing.T) {
	var gotID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = r.Header.Get("Shelley-Request-Id")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(nil)
	ctx, trace := llm.WithRequestTrace(t.Context())
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	req.Header.Set("Shelley-Request-Id", "preset-id")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()
	if gotID != "preset-id" {
		t.Errorf("server got Shelley-Request-Id %q, want preset-id", gotID)
	}
	if trace.Value("shelley_request_id") != "preset-id" {
		t.Errorf("trace ShelleyRequestID = %q, want preset-id", trace.Value("shelley_request_id"))
	}
}

// TestRequestTraceCapturesIDOnErrorResponse verifies the upstream request id
// is captured even when the provider returns a non-2xx status — the case where
// surfacing the id matters most.
func TestRequestTraceCapturesIDOnErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Openai-Request-Id", "req_err_500")
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":"boom"}`)
	}))
	defer server.Close()

	client := NewClient(nil)
	ctx, trace := llm.WithRequestTrace(t.Context())
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	if trace.Value("upstream_request_id") != "req_err_500" {
		t.Errorf("UpstreamRequestID = %q, want req_err_500", trace.Value("upstream_request_id"))
	}
}

// TestRequestTraceCapturesIDWhenIdleDisabled verifies id capture on the code
// path taken when the idle timeout is disabled (IdleTimeout <= 0).
func TestRequestTraceCapturesIDWhenIdleDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req_nodeadline")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "ok")
	}))
	defer server.Close()

	client := NewClientWithIdleTimeout(nil, 0)
	ctx, trace := llm.WithRequestTrace(t.Context())
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	if trace.Value("upstream_request_id") != "req_nodeadline" {
		t.Errorf("UpstreamRequestID = %q, want req_nodeadline", trace.Value("upstream_request_id"))
	}
}
