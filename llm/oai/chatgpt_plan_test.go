package oai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/llm"
)

// fakeCredentials hands out tok-1, then a new token for each refresh.
type fakeCredentials struct {
	token    string
	rejected []string
	err      error
}

func (c *fakeCredentials) Token(context.Context) (string, error) { return c.token, c.err }

func (c *fakeCredentials) Refresh(_ context.Context, rejected string) (string, error) {
	c.rejected = append(c.rejected, rejected)
	c.token = fmt.Sprintf("tok-%d", len(c.rejected)+1)
	return c.token, nil
}

type planRequest struct {
	auth string
	body map[string]any
}

// planServer answers each request with the next of replies (status, body);
// 200 bodies are streamed as a completed response.
func planServer(t *testing.T, replies ...func(w http.ResponseWriter)) (*httptest.Server, *[]planRequest) {
	var requests []planRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests = append(requests, planRequest{auth: r.Header.Get("Authorization"), body: body})
		if len(requests) > len(replies) {
			t.Errorf("unexpected request %d", len(requests))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		replies[len(requests)-1](w)
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func planCompleted(t *testing.T) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, responsesSSECompleted(t, responsesResponse{
			ID: "r", Status: "completed", Model: "gpt-6.1-sol",
			Output: []responsesOutputItem{{Type: "message", Role: "assistant", Content: []responsesContent{{Type: "output_text", Text: "done"}}}},
		}))
	}
}

func planError(status int, body string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}
}

func planService(url string, creds ChatGPTCredentials) *ResponsesService {
	return &ResponsesService{
		Model: GPT61Sol, ModelURL: url, ProviderName: "openai", Backoff: []time.Duration{0},
		ChatGPTPlan: &ChatGPTPlan{Account: "acct", Credentials: creds},
	}
}

func planToolRequest() *llm.Request {
	return &llm.Request{
		System: []llm.SystemContent{{Text: "be helpful"}},
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "list files"}}},
			{Role: llm.MessageRoleAssistant, Content: []llm.Content{{Type: llm.ContentTypeToolUse, ID: "call_1", ToolName: "bash", ToolInput: json.RawMessage(`{"command":"ls"}`)}}},
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeToolResult, ToolUseID: "call_1", ToolName: "bash", ToolResult: llm.TextContent("a.txt")}}},
		},
		Tools: []*llm.Tool{
			{Name: "bash", Description: "run a command", InputSchema: llm.EmptySchema()},
			{Name: "apply_patch", Description: "patch files", CustomGrammar: "start: /.+/"},
		},
	}
}

// The ChatGPT plan route rejects requests outside its preview limits, so every
// turn must be sent within them.
func TestChatGPTPlanRequestFitsPreviewLimits(t *testing.T) {
	server, requests := planServer(t, planCompleted(t))
	resp, err := planService(server.URL, &fakeCredentials{token: "tok-1"}).Do(t.Context(), planToolRequest())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content[0].Text != "done" {
		t.Fatalf("response = %+v", resp.Content)
	}
	got := (*requests)[0]
	if got.auth != "Bearer tok-1" {
		t.Errorf("Authorization = %q", got.auth)
	}
	body := got.body
	if body["store"] != false || body["stream"] != true || body["instructions"] != "be helpful" {
		t.Errorf("store %v, stream %v, instructions %v", body["store"], body["stream"], body["instructions"])
	}
	for _, field := range []string{"max_output_tokens", "previous_response_id", "temperature", "top_p", "metadata", "user", "safety_identifier", "prompt_cache_retention"} {
		if _, ok := body[field]; ok {
			t.Errorf("request has unsupported field %s", field)
		}
	}
	tools := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v, want one namespace", tools)
	}
	namespace := tools[0].(map[string]any)
	if namespace["type"] != "namespace" || len(namespace["tools"].([]any)) != 2 {
		t.Fatalf("tools = %v, want both tools in a namespace", tools)
	}
	var call map[string]any
	for _, item := range body["input"].([]any) {
		item := item.(map[string]any)
		if item["role"] == "system" {
			t.Errorf("input has a system item: %v", item)
		}
		if item["type"] == "function_call" {
			call = item
		}
	}
	if call == nil || call["namespace"] != namespace["name"] {
		t.Errorf("replayed call %v is not in namespace %v", call, namespace["name"])
	}
}

func TestChatGPTPlanCannotPrewarm(t *testing.T) {
	s := planService("http://unused.invalid", &fakeCredentials{token: "tok-1"})
	if _, err := s.WarmCache(t.Context(), planToolRequest()); !errors.Is(err, llm.ErrCacheWarmUnsupported) {
		t.Fatalf("WarmCache err = %v, want unsupported", err)
	}
}

// A revoked or early-expired token is refreshed once, then the turn proceeds.
func TestChatGPTPlanRefreshesAfterUnauthorized(t *testing.T) {
	server, requests := planServer(t, planError(http.StatusUnauthorized, `{"detail":"expired"}`), planCompleted(t))
	creds := &fakeCredentials{token: "tok-1"}
	if _, err := planService(server.URL, creds).Do(t.Context(), planToolRequest()); err != nil {
		t.Fatal(err)
	}
	if len(creds.rejected) != 1 || creds.rejected[0] != "tok-1" || (*requests)[1].auth != "Bearer tok-2" {
		t.Fatalf("rejected %v, retried with %q", creds.rejected, (*requests)[1].auth)
	}
}

func TestChatGPTPlanUnauthorizedTwiceAsksToSignIn(t *testing.T) {
	server, requests := planServer(t, planError(http.StatusUnauthorized, `{"detail":"no"}`), planError(http.StatusUnauthorized, `{"detail":"no"}`))
	_, err := planService(server.URL, &fakeCredentials{token: "tok-1"}).Do(t.Context(), planToolRequest())
	if err == nil || !strings.Contains(err.Error(), "sign in with ChatGPT again") || len(*requests) != 2 {
		t.Fatalf("err = %v after %d requests", err, len(*requests))
	}
}

// Retrying a usage-limit error would only spin until the user changes their
// limits, so it stops the turn and says where to manage usage.
func TestChatGPTPlanUsageLimitIsTerminal(t *testing.T) {
	limit := `{"error":{"message":"limit","type":"usage_limit","code":"subscription_sharing_usage_limit_exceeded"}}`
	server, requests := planServer(t, planError(http.StatusTooManyRequests, limit))
	_, err := planService(server.URL, &fakeCredentials{token: "tok-1"}).Do(t.Context(), planToolRequest())
	if err == nil || !strings.Contains(err.Error(), ChatGPTUsageURL) || len(*requests) != 1 {
		t.Fatalf("err = %v after %d requests", err, len(*requests))
	}
	if info, ok := llm.RequestErrorInfoFromError(err); !ok || info.Retryable {
		t.Fatalf("error info = %+v, %v; want terminal", info, ok)
	}

	streamed, requests := planServer(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":%s}}\n\n",
			`{"message":"limit","code":"subscription_sharing_usage_limit_exceeded"}`)
	})
	_, err = planService(streamed.URL, &fakeCredentials{token: "tok-1"}).Do(t.Context(), planToolRequest())
	if err == nil || !strings.Contains(err.Error(), ChatGPTUsageURL) || len(*requests) != 1 {
		t.Fatalf("streamed err = %v after %d requests", err, len(*requests))
	}
}

func TestChatGPTPlanSignedOutFailsBeforeRequest(t *testing.T) {
	server, requests := planServer(t)
	creds := &fakeCredentials{err: errors.New("sign in with ChatGPT again")}
	if _, err := planService(server.URL, creds).Do(t.Context(), planToolRequest()); err == nil || len(*requests) != 0 {
		t.Fatalf("err = %v after %d requests", err, len(*requests))
	}
}
