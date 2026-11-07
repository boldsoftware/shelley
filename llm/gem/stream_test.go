package gem

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/gem/gemini"
)

func geminiSSE(events ...string) string {
	var body strings.Builder
	for _, event := range events {
		body.WriteString("data: " + strings.ReplaceAll(event, "\n", "\ndata: ") + "\n\n")
	}
	return body.String()
}

func TestGeminiDoStreamsBeforeEOF(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Exedev-Gateway-Cost", "0.001")
		fmt.Fprint(w, ": keepalive\n\n"+geminiSSE(
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"Let","thought":true}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"text":" me think","thought":true}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"text":"Hel"}]}}]}`,
		))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, geminiSSE(
			`{"candidates":[{"content":{"parts":[{"text":"lo"}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"tail"}]}}]}`,
			`{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"cachedContentTokenCount":4,"candidatesTokenCount":3,"thoughtsTokenCount":2}}`,
		))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	progress := 0
	ctx = llm.WithProgress(ctx, func() { progress++ })
	deltas := make(chan llm.StreamDelta, 8)
	type result struct {
		response *llm.Response
		err      error
	}
	completed := make(chan result, 1)
	service := &Service{Model: "test", APIKey: "key", URL: server.URL, HTTPC: server.Client()}
	go func() {
		response, err := service.Do(ctx, &llm.Request{
			Messages: []llm.Message{llm.UserStringMessage("Hello")},
			OnStream: func(delta llm.StreamDelta) { deltas <- delta },
		})
		completed <- result{response, err}
	}()
	for _, want := range []llm.StreamDelta{
		{Type: "thinking", Text: "Let", Index: 0},
		{Type: "thinking", Text: " me think", Index: 0},
		{Type: "text", Text: "Hel", Index: 1},
	} {
		select {
		case got := <-deltas:
			if got != want {
				t.Fatalf("delta before EOF = %#v, want %#v", got, want)
			}
		case <-ctx.Done():
			t.Fatal("no streaming callback before EOF")
		}
	}
	close(release)
	var got result
	select {
	case got = <-completed:
	case <-ctx.Done():
		t.Fatal("request did not finish")
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	if len(deltas) != 1 {
		t.Fatalf("remaining deltas = %d, want one final text callback", len(deltas))
	}
	if delta := <-deltas; delta != (llm.StreamDelta{Type: "text", Text: "lo", Index: 1}) {
		t.Errorf("final delta = %#v", delta)
	}
	want := []llm.Content{
		{Type: llm.ContentTypeThinking, Thinking: "Let me think"},
		{Type: llm.ContentTypeText, Text: "Hello"},
		{Type: llm.ContentTypeText, Signature: "tail"},
	}
	if !reflect.DeepEqual(got.response.Content, want) {
		t.Errorf("content = %#v, want %#v", got.response.Content, want)
	}
	if progress != 6 {
		t.Errorf("progress reports = %d, want 6 model events (not comments)", progress)
	}
	if got.response.Usage != (llm.Usage{InputTokens: 6, CacheReadInputTokens: 4, OutputTokens: 5, CostUSD: 0.001}) {
		t.Errorf("lost reported usage or header cost: %#v", got.response.Usage)
	}
	replay, err := service.buildGeminiRequest(&llm.Request{Messages: []llm.Message{got.response.ToMessage()}})
	if err != nil {
		t.Fatal(err)
	}
	parts := replay.Contents[0].Parts
	if len(parts) != 3 || parts[2].ThoughtSignature != "tail" || parts[2].Text != "" || parts[2].Thought {
		t.Errorf("trailing signature changed on replay: %#v", parts)
	}
}

func TestGeminiDoStreamCancellation(t *testing.T) {
	disconnected := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(disconnected)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, geminiSSE(`{"candidates":[{"content":{"parts":[{"text":"first"},{"text":"must not be emitted after cancellation"}]}}]}`))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	service := &Service{Model: "test", APIKey: "key", URL: server.URL, HTTPC: server.Client()}
	calls := 0
	response, err := service.Do(ctx, &llm.Request{
		Messages: []llm.Message{llm.UserStringMessage("Hello")},
		OnStream: func(delta llm.StreamDelta) {
			calls++
			cancel()
		},
	})
	if !errors.Is(err, context.Canceled) || response != nil {
		t.Fatalf("response = %#v, error = %v, want cancellation", response, err)
	}
	if calls != 1 {
		t.Errorf("stream callbacks = %d, want only one before cancellation", calls)
	}
	select {
	case <-disconnected:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not close the HTTP stream")
	}
}

func TestGeminiDoReportsStreamProgressWithoutCallback(t *testing.T) {
	body := ": keepalive\n\n" + geminiSSE(
		`{"candidates":[{"content":{"parts":[{"functionCall":{"id":"one","name":"bash","args":{"command":"pwd"}},"thoughtSignature":"sig"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"id":"two","name":"bash","args":{"command":"ls"}}}]}}]}`,
		`{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":10}}`,
	) + ": another keepalive\n\n"
	service := &Service{
		Model: "test", APIKey: "key",
		HTTPC: &http.Client{Transport: &mockRoundTripper{response: &http.Response{
			StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(body)),
		}}},
	}
	progress := 0
	ctx := llm.WithProgress(t.Context(), func() { progress++ })
	response, err := service.Do(ctx, &llm.Request{Messages: []llm.Message{llm.UserStringMessage("Hello")}})
	if err != nil {
		t.Fatal(err)
	}
	if progress != 3 || response.StopReason != llm.StopReasonToolUse || len(response.Content) != 2 {
		t.Fatalf("progress = %d, response = %#v", progress, response)
	}
	if response.Content[0].ID != "one" || response.Content[0].Signature != "sig" || response.Content[1].ID != "two" {
		t.Errorf("lost streamed tool IDs/signature: %#v", response.Content)
	}
}

func TestConvertResponseEmptySignedPart(t *testing.T) {
	for _, thought := range []bool{false, true} {
		t.Run(fmt.Sprint(thought), func(t *testing.T) {
			content := convertGeminiResponseToContent(&gemini.Response{Candidates: []gemini.Candidate{{
				Content: gemini.Content{Parts: []gemini.Part{{Thought: thought, ThoughtSignature: "signature"}}},
			}}})
			wantType := llm.ContentTypeText
			if thought {
				wantType = llm.ContentTypeThinking
			}
			if len(content) != 1 || content[0].Type != wantType || content[0].Signature != "signature" {
				t.Fatalf("empty signed part lost: %#v", content)
			}
		})
	}
}

type streamRoundTripFunc func(*http.Request) (*http.Response, error)

func (f streamRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGeminiDoDoesNotRetryStreamError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	requests, retries := 0, 0
	body := geminiSSE(
		`{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}`,
		`{"error":{"code":503,"status":"UNAVAILABLE","message":"provider failed"}}`,
	)
	service := &Service{
		Model: "test", APIKey: "key",
		HTTPC: &http.Client{Transport: streamRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			requests++
			return &http.Response{
				StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
				Body: io.NopCloser(strings.NewReader(body)),
			}, nil
		})},
	}
	var deltas []llm.StreamDelta
	response, err := service.Do(ctx, &llm.Request{
		Messages: []llm.Message{llm.UserStringMessage("Hello")},
		OnStream: func(delta llm.StreamDelta) { deltas = append(deltas, delta) },
		OnRetry:  func(llm.RetryEvent) { retries++; cancel() },
	})
	if err == nil || !strings.Contains(err.Error(), "provider failed") || response != nil {
		t.Fatalf("response = %#v, error = %v, want stream failure", response, err)
	}
	if requests != 1 || retries != 0 || len(deltas) != 1 || deltas[0].Text != "partial" {
		t.Errorf("requests=%d, retries=%d, deltas=%#v", requests, retries, deltas)
	}
}

func TestGeminiDoRejectsUnsupportedStreamPart(t *testing.T) {
	body := geminiSSE(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AA=="}},{"text":"must not get the wrong block index"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":10}}`)
	service := &Service{
		Model: "test", APIKey: "key",
		HTTPC: &http.Client{Transport: &mockRoundTripper{response: &http.Response{
			StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(body)),
		}}},
	}
	calls := 0
	response, err := service.Do(t.Context(), &llm.Request{
		Messages: []llm.Message{llm.UserStringMessage("Hello")},
		OnStream: func(llm.StreamDelta) { calls++ },
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported") || response != nil || calls != 0 {
		t.Fatalf("unsupported part accepted: response=%#v error=%v callbacks=%d", response, err, calls)
	}
}
