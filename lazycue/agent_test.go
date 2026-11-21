package lazycue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsRetryableAnthropicErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context canceled", context.Canceled, false},
		{"deadline exceeded", context.DeadlineExceeded, true},
		{"429 too many requests", &anthropicStatusError{StatusCode: http.StatusTooManyRequests}, true},
		{"500 server error", &anthropicStatusError{StatusCode: http.StatusInternalServerError}, true},
		{"503 unavailable", &anthropicStatusError{StatusCode: http.StatusServiceUnavailable}, true},
		{"400 bad request", &anthropicStatusError{StatusCode: http.StatusBadRequest}, false},
		{"401 unauthorized", &anthropicStatusError{StatusCode: http.StatusUnauthorized}, false},
		{"wrapped 503", fmt.Errorf("HTTP request: %w", &anthropicStatusError{StatusCode: 503}), true},
		{"transport error", errors.New("connection reset by peer"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryableAnthropicErr(tt.err); got != tt.want {
				t.Errorf("isRetryableAnthropicErr(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// shrinkBackoff sets a tiny retry backoff for the duration of a test so the
// retry loop doesn't sleep for real seconds. AGENTS.md: don't sleep in tests.
func shrinkBackoff(t *testing.T) {
	t.Helper()
	prev := anthropicRetryBackoff
	anthropicRetryBackoff = time.Millisecond
	t.Cleanup(func() { anthropicRetryBackoff = prev })
}

// validAnthropicResponse is a minimal well-formed apiResponse body.
const validAnthropicResponse = `{"id":"msg_1","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`

func newTestAgentConfig(baseURL string) *AgentConfig {
	return &AgentConfig{
		Model:            "claude-haiku-4-5",
		AnthropicBaseURL: baseURL,
		AnthropicAPIKey:  "test-key",
		Verbose:          true,
	}
}

func TestCallAnthropicRetriesTransient5xx(t *testing.T) {
	shrinkBackoff(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, validAnthropicResponse)
	}))
	defer srv.Close()

	cfg := newTestAgentConfig(srv.URL)
	resp, err := callAnthropic(context.Background(), cfg, "sys", []apiMessage{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("callAnthropic returned error after retries: %v", err)
	}
	if resp == nil || resp.ID != "msg_1" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("expected 3 attempts (2 failures + 1 success), got %d", got)
	}
}

func TestCallAnthropicDoesNotRetry4xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer srv.Close()

	cfg := newTestAgentConfig(srv.URL)
	_, err := callAnthropic(context.Background(), cfg, "sys", []apiMessage{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error from 400 response")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("expected exactly 1 attempt for non-retryable 400, got %d", got)
	}
}

func TestCallAnthropicExhaustsAttempts(t *testing.T) {
	shrinkBackoff(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "still down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	cfg := newTestAgentConfig(srv.URL)
	_, err := callAnthropic(context.Background(), cfg, "sys", []apiMessage{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if got := atomic.LoadInt32(&calls); got != anthropicMaxAttempts {
		t.Errorf("expected %d attempts, got %d", anthropicMaxAttempts, got)
	}
}

func TestCallAnthropicStopsOnContextCancel(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	// Cancel well before the first backoff (2s) elapses so the retry loop must
	// observe the cancelled context instead of sleeping through it.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	cfg := newTestAgentConfig(srv.URL)
	_, err := callAnthropic(ctx, cfg, "sys", []apiMessage{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error when context is cancelled")
	}
	// One request happens, then the loop should bail rather than retry to
	// exhaustion under a cancelled context.
	if got := atomic.LoadInt32(&calls); got >= anthropicMaxAttempts {
		t.Errorf("expected fewer than %d attempts under cancellation, got %d", anthropicMaxAttempts, got)
	}
}

func TestToolSchemasAreJSON(t *testing.T) {
	for _, tool := range buildTools() {
		var v map[string]any
		if err := json.Unmarshal(tool.InputSchema, &v); err != nil {
			t.Errorf("%s: %v", tool.Name, err)
		}
	}
}

func TestInlinedVar(t *testing.T) {
	vars := map[string]string{"DIR": "/tmp/fixture-123", "N": "7"}
	if got := inlinedVar([]Step{{Action: ActionFill, Value: "cd {{DIR}}"}, {Action: ActionAssertText, Text: "7 items"}}, vars); got != "" {
		t.Errorf("placeholders and short values: got %q, want none", got)
	}
	if got := inlinedVar([]Step{{Action: ActionEval, Expression: "x('/tmp/fixture-123/a')"}}, vars); got != "DIR" {
		t.Errorf("inlined value: got %q, want DIR", got)
	}
}

func TestCacheConversationMarksOnlyTheLastBlock(t *testing.T) {
	msgs := []apiMessage{
		{Role: "user", Content: "describe"},
		{Role: "assistant", Content: []apiContentBlock{{Type: "text", Text: "ok"}}},
		{Role: "user", Content: []apiContentBlock{{Type: "tool_result", ToolUseID: "a"}, {Type: "tool_result", ToolUseID: "b"}}},
	}
	out := cacheConversation(msgs)
	b, _ := json.Marshal(out)
	if n := strings.Count(string(b), "cache_control"); n != 1 {
		t.Fatalf("%d cache_control markers, want 1: %s", n, b)
	}
	last := out[2].Content.([]apiContentBlock)
	if last[1].CacheControl == nil || last[0].CacheControl != nil {
		t.Errorf("marker not on the last block: %+v", last)
	}
	if msgs[2].Content.([]apiContentBlock)[1].CacheControl != nil {
		t.Error("cacheConversation modified its input")
	}
	if out := cacheConversation(msgs[:1]); out[0].Content.([]apiContentBlock)[0].CacheControl == nil {
		t.Error("a string prompt should become a marked block")
	}
}
