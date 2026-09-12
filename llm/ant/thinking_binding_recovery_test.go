package ant

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/llm"
)

func thinkingJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func thinkingRound(id string) []llm.Message {
	return []llm.Message{
		{Role: llm.MessageRoleAssistant, Content: []llm.Content{
			{Type: llm.ContentTypeThinking, Thinking: "reasoning", Signature: "signature-" + id},
			{Type: llm.ContentTypeRedactedThinking, Data: "redacted-" + id},
			{Type: llm.ContentTypeToolUse, ID: id, ToolName: "lookup", ToolInput: json.RawMessage(`{"key":"A"}`)},
		}},
		{Role: llm.MessageRoleUser, Content: []llm.Content{
			{Type: llm.ContentTypeToolResult, ToolUseID: id, ToolResult: llm.TextContent("23")},
		}},
	}
}

func thinkingRequestState(t *testing.T, req *http.Request) (bool, int) {
	t.Helper()
	var wire request
	if err := json.NewDecoder(req.Body).Decode(&wire); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, message := range wire.Messages {
		for _, block := range message.Content {
			if block.Type == "thinking" || block.Type == "redacted_thinking" {
				count++
			}
		}
	}
	return wire.Thinking != nil && wire.Thinking.BlockBinding != nil, count
}

func providerErrorJSON(t *testing.T, errorType, message string) string {
	t.Helper()
	return string(thinkingJSON(t, map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    errorType,
			"message": message,
		},
	}))
}

func httpResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func testService(fn func(*http.Request) (*http.Response, error)) *Service {
	return &Service{
		Model:                 Claude46Opus,
		URL:                   "https://gateway.example/v1/messages",
		ThinkingLevel:         llm.ThinkingLevelMedium,
		EnableThinkingBinding: true,
		Backoff:               []time.Duration{0},
		HTTPC:                 &http.Client{Transport: &roundTripFunc{fn: fn}},
	}
}

func TestThinkingBindingRejectionFails(t *testing.T) {
	calls := 0
	s := testService(func(*http.Request) (*http.Response, error) {
		calls++
		return httpResponse(http.StatusBadRequest, providerErrorJSON(t, "invalid_request_error",
			"thinking.block_binding: Extra inputs are not permitted")), nil
	})
	request := &llm.Request{Messages: []llm.Message{llm.UserStringMessage("hello")}}
	if _, err := s.Do(t.Context(), request); err == nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestThinkingSignatureHTTPRetryStripsAllThinking(t *testing.T) {
	history := thinkingRound("active")
	before := thinkingJSON(t, history)
	var states []struct {
		binding  bool
		thinking int
	}
	var retries []llm.RetryEvent
	s := testService(func(req *http.Request) (*http.Response, error) {
		binding, thinking := thinkingRequestState(t, req)
		states = append(states, struct {
			binding  bool
			thinking int
		}{binding, thinking})
		if thinking > 0 {
			return httpResponse(http.StatusBadRequest, providerErrorJSON(t, "invalid_request_error",
				"Invalid signature in thinking block")), nil
		}
		return httpResponse(http.StatusOK, mockSSEResponse("ok", Claude46Opus, "answer", 1, 1)), nil
	})
	out, err := s.Do(t.Context(), &llm.Request{
		Messages: history,
		OnRetry:  func(event llm.RetryEvent) { retries = append(retries, event) },
	})
	want := []struct {
		binding  bool
		thinking int
	}{{true, 2}, {true, 0}}
	if err != nil || out == nil || !reflect.DeepEqual(states, want) || len(retries) != 1 {
		t.Fatalf("states=%v retries=%d response=%t err=%v", states, len(retries), out != nil, err)
	}
	if !bytes.Equal(before, thinkingJSON(t, history)) {
		t.Fatal("recovery mutated history")
	}
}
