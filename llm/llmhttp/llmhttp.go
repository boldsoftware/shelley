// Package llmhttp provides HTTP utilities for LLM requests including
// custom headers and database recording.
package llmhttp

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"shelley.exe.dev/version"
)

// contextKey is the type for context keys in this package.
type contextKey int

const (
	conversationIDKey contextKey = iota
	modelIDKey
	providerKey
)

// WithConversationID returns a context with the conversation ID attached.
func WithConversationID(ctx context.Context, conversationID string) context.Context {
	return context.WithValue(ctx, conversationIDKey, conversationID)
}

// ConversationIDFromContext returns the conversation ID from the context, if any.
func ConversationIDFromContext(ctx context.Context) string {
	if v := ctx.Value(conversationIDKey); v != nil {
		return v.(string)
	}
	return ""
}

// WithModelID returns a context with the model ID attached.
func WithModelID(ctx context.Context, modelID string) context.Context {
	return context.WithValue(ctx, modelIDKey, modelID)
}

// ModelIDFromContext returns the model ID from the context, if any.
func ModelIDFromContext(ctx context.Context) string {
	if v := ctx.Value(modelIDKey); v != nil {
		return v.(string)
	}
	return ""
}

// WithProvider returns a context with the provider name attached.
func WithProvider(ctx context.Context, provider string) context.Context {
	return context.WithValue(ctx, providerKey, provider)
}

// ProviderFromContext returns the provider name from the context, if any.
func ProviderFromContext(ctx context.Context) string {
	if v := ctx.Value(providerKey); v != nil {
		return v.(string)
	}
	return ""
}

// Recorder is called after each LLM HTTP request with the request/response details.
type Recorder func(ctx context.Context, url string, requestBody, responseBody []byte, statusCode int, err error, duration time.Duration)

// Transport wraps an http.RoundTripper to add Shelley-specific headers
// and optionally record requests to a database.
type Transport struct {
	Base     http.RoundTripper
	Recorder Recorder
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()

	// Clone the request to avoid modifying the original
	req = req.Clone(req.Context())

	// Add User-Agent with Shelley version
	info := version.GetInfo()
	userAgent := "Shelley"
	if info.Commit != "" {
		userAgent += "/" + info.Commit[:min(8, len(info.Commit))]
	}
	req.Header.Set("User-Agent", userAgent)

	// Add conversation ID header if present
	if conversationID := ConversationIDFromContext(req.Context()); conversationID != "" {
		req.Header.Set("Shelley-Conversation-Id", conversationID)

		// Add x-session-affinity header for Fireworks to enable prompt caching
		if ProviderFromContext(req.Context()) == "fireworks" {
			req.Header.Set("x-session-affinity", conversationID)
		}
	}

	// Read and store the request body for recording
	var requestBody []byte
	if t.Recorder != nil && req.Body != nil {
		var err error
		requestBody, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(requestBody))
	}

	// Perform the actual request
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}

	resp, err := base.RoundTrip(req)

	// Record the request if we have a recorder
	if t.Recorder != nil && resp == nil {
		// Transport-level error (DNS failure, connection refused, etc.) — no response to stream.
		t.Recorder(req.Context(), req.URL.String(), requestBody, nil, 0, err, time.Since(start))
	}
	if t.Recorder != nil && resp != nil {
		contentType := resp.Header.Get("Content-Type")
		isStreaming := strings.HasPrefix(contentType, "text/event-stream")

		if isStreaming {
			// For SSE streams, wrap the body so we record after the caller
			// finishes reading. This avoids buffering the entire stream
			// upfront, which would destroy real-time streaming.
			rb := &recordingBody{
				ReadCloser: resp.Body,
				ctx:        req.Context(),
				url:        req.URL.String(),
				reqBody:    requestBody,
				statusCode: resp.StatusCode,
				start:      start,
				recorder:   t.Recorder,
			}
			resp.Body = rb
		} else {
			// For non-streaming responses, tee the body so callers see any read
			// errors. Recording after Close preserves truncated/empty-response
			// failures for retry logic instead of converting them to EOF here.
			rb := &recordingBody{
				ReadCloser: resp.Body,
				ctx:        req.Context(),
				url:        req.URL.String(),
				reqBody:    requestBody,
				statusCode: resp.StatusCode,
				start:      start,
				recorder:   t.Recorder,
			}
			resp.Body = rb
		}
	}

	return resp, err
}

// recordingBody wraps an io.ReadCloser to accumulate the response body
// as it is read, then calls the recorder when Close is called.
// This allows SSE streams to be read in real-time while still recording
// the full response body for the database.
type recordingBody struct {
	io.ReadCloser
	ctx        context.Context
	url        string
	reqBody    []byte
	buf        bytes.Buffer
	readErr    error
	statusCode int
	start      time.Time
	recorder   Recorder
	once       sync.Once
}

func (rb *recordingBody) Read(p []byte) (int, error) {
	n, err := rb.ReadCloser.Read(p)
	if n > 0 {
		rb.buf.Write(p[:n])
	}
	if rb.readErr == nil && err != nil && err != io.EOF {
		rb.readErr = err
	}
	return n, err
}

func (rb *recordingBody) Close() error {
	err := rb.ReadCloser.Close()
	rb.once.Do(func() {
		recordErr := rb.readErr
		if recordErr == nil {
			recordErr = err
		}
		rb.recorder(rb.ctx, rb.url, rb.reqBody, rb.buf.Bytes(), rb.statusCode, recordErr, time.Since(rb.start))
	})
	return err
}

// NewClient creates an http.Client with Shelley headers and optional recording.
func NewClient(base *http.Client, recorder Recorder) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}

	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	return &http.Client{
		Transport: &Transport{
			Base:     transport,
			Recorder: recorder,
		},
		Timeout: base.Timeout,
	}
}
