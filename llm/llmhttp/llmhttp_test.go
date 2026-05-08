package llmhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestContextFunctions(t *testing.T) {
	ctx := context.Background()

	// Test ConversationID
	ctx = WithConversationID(ctx, "conv-123")
	if got := ConversationIDFromContext(ctx); got != "conv-123" {
		t.Errorf("ConversationIDFromContext() = %q, want %q", got, "conv-123")
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

	// Test empty context
	emptyCtx := context.Background()
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

	// Create client with our transport
	client := NewClient(nil, nil)

	// Make a request with conversation ID in context
	ctx := WithConversationID(context.Background(), "test-conv-id")
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
}

func TestTransportAddsSessionAffinityForFireworks(t *testing.T) {
	// Create a test server that echoes request headers
	var receivedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	// Create client with our transport
	client := NewClient(nil, nil)

	// Make a request with conversation ID and provider=fireworks in context
	ctx := context.Background()
	ctx = WithConversationID(ctx, "test-conv-id")
	ctx = WithProvider(ctx, "fireworks")
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	resp.Body.Close()

	// Verify x-session-affinity header was added for fireworks
	if got := receivedHeaders.Get("x-session-affinity"); got != "test-conv-id" {
		t.Errorf("x-session-affinity = %q, want %q", got, "test-conv-id")
	}

	// Verify Shelley-Conversation-Id header was also added
	if got := receivedHeaders.Get("Shelley-Conversation-Id"); got != "test-conv-id" {
		t.Errorf("Shelley-Conversation-Id = %q, want %q", got, "test-conv-id")
	}
}

func TestTransportRecordsRequest(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("response body: " + string(body)))
	}))
	defer server.Close()

	// Track recorded values
	var (
		recordedURL         string
		recordedRequestBody []byte
		recordedRespBody    []byte
		recordedStatusCode  int
		recordedDuration    time.Duration
		recorderCalled      bool
	)

	recorder := func(ctx context.Context, url string, requestBody, responseBody []byte, statusCode int, err error, duration time.Duration) {
		recorderCalled = true
		recordedURL = url
		recordedRequestBody = requestBody
		recordedRespBody = responseBody
		recordedStatusCode = statusCode
		recordedDuration = duration
	}

	// Create client with recorder
	client := NewClient(nil, recorder)

	// Make a request with body
	req, _ := http.NewRequest("POST", server.URL, strings.NewReader("test body"))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}

	// Read response body to ensure it's still accessible
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if string(respBody) != "response body: test body" {
		t.Errorf("Response body = %q, want %q", string(respBody), "response body: test body")
	}

	// Verify recorder was called with correct values
	if !recorderCalled {
		t.Fatal("Recorder was not called")
	}

	if recordedURL != server.URL {
		t.Errorf("Recorded URL = %q, want %q", recordedURL, server.URL)
	}

	if string(recordedRequestBody) != "test body" {
		t.Errorf("Recorded request body = %q, want %q", string(recordedRequestBody), "test body")
	}

	if string(recordedRespBody) != "response body: test body" {
		t.Errorf("Recorded response body = %q, want %q", string(recordedRespBody), "response body: test body")
	}

	if recordedStatusCode != http.StatusOK {
		t.Errorf("Recorded status code = %d, want %d", recordedStatusCode, http.StatusOK)
	}

	if recordedDuration <= 0 {
		t.Error("Recorded duration should be positive")
	}
}

func TestTransportWithoutRecorder(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	// Create client without recorder
	client := NewClient(nil, nil)

	// Make a request
	req, _ := http.NewRequest("GET", server.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Status code = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestTransportRecordsStreamingResponseOnClose(t *testing.T) {
	// Create a test server that returns an SSE stream
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: chunk1\n\n"))
		w.Write([]byte("data: chunk2\n\n"))
		w.Write([]byte("data: chunk3\n\n"))
	}))
	defer server.Close()

	var (
		recordedRespBody []byte
		recorderCalled   bool
	)

	recorder := func(ctx context.Context, url string, requestBody, responseBody []byte, statusCode int, err error, duration time.Duration) {
		recorderCalled = true
		recordedRespBody = responseBody
	}

	client := NewClient(nil, recorder)

	req, _ := http.NewRequest("POST", server.URL, strings.NewReader("test"))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}

	// Recorder should NOT have been called yet (streaming)
	if recorderCalled {
		t.Fatal("Recorder was called before body was read — streaming is broken")
	}

	// Read the body incrementally
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// Now the recorder should have been called
	if !recorderCalled {
		t.Fatal("Recorder was not called after body close")
	}

	want := "data: chunk1\n\ndata: chunk2\n\ndata: chunk3\n\n"
	if string(body) != want {
		t.Errorf("Body = %q, want %q", string(body), want)
	}

	if string(recordedRespBody) != want {
		t.Errorf("Recorded body = %q, want %q", string(recordedRespBody), want)
	}
}

func TestTransportRecordsNonStreamingReadError(t *testing.T) {
	recorderCalled := false
	var recordedErr error
	var recordedRespBody []byte

	client := &http.Client{Transport: &Transport{
		Base: erringRoundTripper{},
		Recorder: func(ctx context.Context, url string, requestBody, responseBody []byte, statusCode int, err error, duration time.Duration) {
			recorderCalled = true
			recordedRespBody = responseBody
			recordedErr = err
		},
	}}

	resp, err := client.Get("http://example.test")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("ReadAll err = %v, want ErrUnexpectedEOF", err)
	}
	if string(body) != "partial" {
		t.Fatalf("body = %q, want partial", string(body))
	}
	if !recorderCalled {
		t.Fatal("recorder was not called")
	}
	if !errors.Is(recordedErr, io.ErrUnexpectedEOF) {
		t.Fatalf("recorded err = %v, want ErrUnexpectedEOF", recordedErr)
	}
	if string(recordedRespBody) != "partial" {
		t.Fatalf("recorded response = %q, want partial", string(recordedRespBody))
	}
}

type erringRoundTripper struct{}

func (erringRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       erringBody{Reader: strings.NewReader("partial")},
	}, nil
}

type erringBody struct {
	*strings.Reader
}

func (b erringBody) Close() error { return nil }

func (b erringBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err == io.EOF {
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}
