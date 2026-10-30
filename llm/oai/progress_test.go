package oai

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"shelley.exe.dev/llm"
)

// Every Responses stream event except keepalive is model progress,
// including tool-call argument deltas, which produce no OnStream delta.
func TestResponsesDoReportsProgressPerEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range []string{
			`{"type":"response.created","response":{"id":"r","status":"in_progress","output":[]}}`,
			`{"type":"keepalive"}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc","call_id":"c","name":"bash","arguments":""}}`,
			`{"type":"keepalive"}`,
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{}"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc","call_id":"c","name":"bash","arguments":"{}"}}`,
			`{"type":"response.completed","response":{"id":"r","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", ev)
		}
	}))
	defer server.Close()

	svc := &ResponsesService{APIKey: "k", Model: GPT41, ModelURL: server.URL, Backoff: []time.Duration{0}}
	progress := 0
	ctx := llm.WithProgress(t.Context(), func() { progress++ })
	_, err := svc.Do(ctx, &llm.Request{
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if progress != 5 {
		t.Errorf("progress reports = %d, want 5 (one per non-keepalive event)", progress)
	}
}

// Every streamed Chat Completions chunk is model progress.
func TestChatDoReportsProgressPerChunk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": keepalive\n\n")
		fmt.Fprint(w, `data: {"id":"c","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"b"}}]}`+"\n\n")
		fmt.Fprint(w, ": keepalive\n\n")
		fmt.Fprint(w, `data: {"id":"c","model":"m","choices":[{"index":0,"delta":{"content":"ye"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	svc := &Service{APIKey: "k", Model: modelForTest("gpt-5.6-sol"), ModelURL: server.URL}
	progress := 0
	ctx := llm.WithProgress(t.Context(), func() { progress++ })
	_, err := svc.Do(ctx, &llm.Request{
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
		OnStream: func(llm.StreamDelta) {},
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if progress != 2 {
		t.Errorf("progress reports = %d, want 2 (one per chunk)", progress)
	}
}
