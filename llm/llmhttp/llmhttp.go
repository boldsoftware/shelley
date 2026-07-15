// Package llmhttp provides HTTP utilities for LLM requests, namely a
// custom transport that adds Shelley-specific headers and enforces an
// idle/stall timeout on streaming responses.
package llmhttp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	requestTraceKey
	userAgentKey
)

// shelleyRequestIDHeader is the header Shelley sets on every LLM request with a
// locally-generated id. It flows through the exe.dev gateway into the access
// logs, so a user-reported id can be correlated with the server-side trace_id.
const shelleyRequestIDHeader = "Shelley-Request-Id"

// upstreamRequestIDHeaders are response headers, in priority order, that
// providers use to expose their own request/correlation id. The exe.dev
// gateway strips account-identifying headers (Cf-Ray, rate limits, org) but
// forwards these request ids, so Shelley can surface them.
var upstreamRequestIDHeaders = []string{
	"X-Request-Id",
	"Request-Id",
	"Openai-Request-Id",
	"X-Amzn-Requestid",
}

// RequestTrace collects correlation ids for a single LLM request so callers
// can surface them (e.g. in a user-facing error). ShelleyRequestID is always
// set by the Transport; UpstreamRequestID is set only if the provider returned
// one. It is safe for concurrent use.
type RequestTrace struct {
	mu               sync.Mutex
	shelleyRequestID string
	upstreamID       string
}

// ShelleyRequestID returns the Shelley-generated request id, if assigned.
func (t *RequestTrace) ShelleyRequestID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.shelleyRequestID
}

// UpstreamRequestID returns the provider's request id, if one was seen.
func (t *RequestTrace) UpstreamRequestID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.upstreamID
}

// String renders the available ids compactly for inclusion in error messages
// and logs. Returns "" when nothing has been captured.
func (t *RequestTrace) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case t.shelleyRequestID != "" && t.upstreamID != "":
		return fmt.Sprintf("shelley_request_id=%s upstream_request_id=%s", t.shelleyRequestID, t.upstreamID)
	case t.shelleyRequestID != "":
		return "shelley_request_id=" + t.shelleyRequestID
	case t.upstreamID != "":
		return "upstream_request_id=" + t.upstreamID
	default:
		return ""
	}
}

func (t *RequestTrace) setShelleyRequestID(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.shelleyRequestID = id
}

func (t *RequestTrace) setUpstreamID(id string) {
	if id == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.upstreamID = id
}

// WithRequestTrace attaches a fresh RequestTrace to ctx and returns both. The
// Transport populates it as the request proceeds.
func WithRequestTrace(ctx context.Context) (context.Context, *RequestTrace) {
	t := &RequestTrace{}
	return context.WithValue(ctx, requestTraceKey, t), t
}

// RequestTraceFromContext returns the RequestTrace attached to ctx, if any.
func RequestTraceFromContext(ctx context.Context) *RequestTrace {
	if v := ctx.Value(requestTraceKey); v != nil {
		return v.(*RequestTrace)
	}
	return nil
}

// newRequestID returns a short random hex id for correlating a request.
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read essentially never fails; fall back to a timestamp so we
		// still emit *something* correlatable.
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// captureUpstreamRequestID records the provider's request id from response
// headers into trace, if present. No-op when trace is nil.
func captureUpstreamRequestID(trace *RequestTrace, h http.Header) {
	if trace == nil || h == nil {
		return
	}
	for _, name := range upstreamRequestIDHeaders {
		if v := h.Get(name); v != "" {
			trace.setUpstreamID(v)
			return
		}
	}
}

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

// WithUserAgent returns a context that overrides Shelley's default User-Agent.
// An empty value preserves the default.
func WithUserAgent(ctx context.Context, userAgent string) context.Context {
	return context.WithValue(ctx, userAgentKey, userAgent)
}

// UserAgentFromContext returns the configured User-Agent override, if any.
func UserAgentFromContext(ctx context.Context) string {
	if v := ctx.Value(userAgentKey); v != nil {
		return v.(string)
	}
	return ""
}

// ErrIdleTimeout is returned (wrapped) when a response stream makes no
// progress — no bytes received — for longer than the configured idle timeout.
// Callers can test for it with errors.Is. It is deliberately distinct from
// context.DeadlineExceeded so that a stalled stream is not confused with an
// overall request deadline or a user-initiated cancel.
var ErrIdleTimeout = errors.New("llm stream idle timeout: no data received within idle window")

// DefaultIdleTimeout is the idle/stall timeout applied to LLM requests when a
// client is built without an explicit value. It bounds how long we wait
// between bytes (including time-to-first-byte), not the total duration of a
// turn: a slow-but-progressing stream (e.g. a long high-reasoning response)
// runs to completion as long as it keeps making progress.
const DefaultIdleTimeout = 3 * time.Minute

// Transport wraps an http.RoundTripper to add Shelley-specific headers and
// enforce an idle/stall timeout on the response body.
type Transport struct {
	Base http.RoundTripper
	// IdleTimeout, when > 0, aborts a request if no response bytes are
	// received for this long. The timer resets on every successful read, so
	// it measures the gap between chunks (and time-to-first-byte), not total
	// duration. Zero disables the mechanism.
	IdleTimeout time.Duration
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone the request to avoid modifying the original
	req = req.Clone(req.Context())

	// Add the per-model override or Shelley's default User-Agent.
	userAgent := UserAgentFromContext(req.Context())
	if userAgent == "" {
		info := version.GetInfo()
		userAgent = "Shelley"
		if info.Commit != "" {
			userAgent += "/" + info.Commit[:min(8, len(info.Commit))]
		}
	}
	req.Header.Set("User-Agent", userAgent)

	// Assign a Shelley request id (or honor one already set) so every LLM
	// request is correlatable. The id is echoed into any RequestTrace on the
	// context so callers can surface it even when the request fails before a
	// response arrives (e.g. an idle/stall timeout).
	requestID := req.Header.Get(shelleyRequestIDHeader)
	if requestID == "" {
		requestID = newRequestID()
		req.Header.Set(shelleyRequestIDHeader, requestID)
	}
	trace := RequestTraceFromContext(req.Context())
	if trace != nil {
		trace.setShelleyRequestID(requestID)
	}

	// Add conversation ID header if present
	if conversationID := ConversationIDFromContext(req.Context()); conversationID != "" {
		req.Header.Set("Shelley-Conversation-Id", conversationID)

		// Add x-session-affinity header for Fireworks to enable prompt caching
		if ProviderFromContext(req.Context()) == "fireworks" {
			req.Header.Set("x-session-affinity", conversationID)
		}
	}

	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}

	if t.IdleTimeout <= 0 {
		resp, err := base.RoundTrip(req)
		if resp != nil {
			captureUpstreamRequestID(trace, resp.Header)
		}
		return resp, err
	}

	// Install an idle watchdog. We derive a cancelable context so that when
	// the stream stalls we can abort the in-flight read at the transport
	// layer, unblocking a Body.Read that is waiting on the network. The
	// watchdog covers time-to-first-byte too: RoundTrip itself blocks until
	// headers arrive, so we start the timer before the RoundTrip call.
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)

	watch := &idleWatchdog{timeout: t.IdleTimeout, cancel: cancel}
	watch.start()

	resp, err := base.RoundTrip(req)
	if resp != nil {
		captureUpstreamRequestID(trace, resp.Header)
	}
	if err != nil {
		watch.stop()
		cancel()
		return nil, watch.translate(err)
	}

	// Wrap the body so each read resets the idle timer, and so the final
	// read error is translated to ErrIdleTimeout when the watchdog fired.
	resp.Body = &idleReadCloser{
		ReadCloser: resp.Body,
		watch:      watch,
		cancel:     cancel,
	}
	return resp, nil
}

// idleWatchdog cancels a request's context if no progress is reported within
// the timeout. Each call to reset() restarts the countdown.
type idleWatchdog struct {
	timeout time.Duration
	cancel  context.CancelFunc

	mu      sync.Mutex
	timer   *time.Timer
	fired   bool
	stopped bool
}

func (w *idleWatchdog) start() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.timer = time.AfterFunc(w.timeout, w.onFire)
}

func (w *idleWatchdog) onFire() {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	w.fired = true
	w.mu.Unlock()
	w.cancel()
}

// reset restarts the idle countdown. It is a no-op once the watchdog has
// fired or been stopped.
func (w *idleWatchdog) reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped || w.fired || w.timer == nil {
		return
	}
	w.timer.Reset(w.timeout)
}

// stop halts the watchdog. Safe to call multiple times.
func (w *idleWatchdog) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopped = true
	if w.timer != nil {
		w.timer.Stop()
	}
}

func (w *idleWatchdog) hasFired() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.fired
}

// translate converts an error into ErrIdleTimeout when the watchdog fired,
// preserving the underlying cause for debugging.
func (w *idleWatchdog) translate(err error) error {
	if err == nil || !w.hasFired() {
		return err
	}
	return fmt.Errorf("%w after %s: %v", ErrIdleTimeout, w.timeout, err)
}

// idleReadCloser resets the idle watchdog on every read and translates a read
// error caused by the watchdog into ErrIdleTimeout.
type idleReadCloser struct {
	io.ReadCloser
	watch  *idleWatchdog
	cancel context.CancelFunc
}

func (r *idleReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		// Progress: restart the countdown.
		r.watch.reset()
	}
	if err != nil {
		// A natural end of stream is not a stall. Stop the watchdog so a
		// delayed Close (or a read past EOF) can't retroactively translate
		// this into a spurious ErrIdleTimeout.
		if errors.Is(err, io.EOF) {
			r.watch.stop()
			return n, err
		}
		return n, r.watch.translate(err)
	}
	return n, nil
}

func (r *idleReadCloser) Close() error {
	r.watch.stop()
	r.cancel()
	return r.ReadCloser.Close()
}

// NewClient creates an http.Client with Shelley headers applied via Transport
// and the default idle/stall timeout.
func NewClient(base *http.Client) *http.Client {
	return NewClientWithIdleTimeout(base, DefaultIdleTimeout)
}

// NewClientWithIdleTimeout is like NewClient but with an explicit idle/stall
// timeout. A value <= 0 disables the idle timeout.
func NewClientWithIdleTimeout(base *http.Client, idleTimeout time.Duration) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}

	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	return &http.Client{
		Transport: &Transport{Base: transport, IdleTimeout: idleTimeout},
		Timeout:   base.Timeout,
	}
}
