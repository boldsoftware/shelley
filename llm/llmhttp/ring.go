package llmhttp

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// Recent records the most recent LLM HTTP exchanges made by clients built
// with NewClient. It backs Shelley's /debug/llm page.
var Recent = NewRing(100, 32<<20)

// maxBodyCapture caps how much of a single request or response body is
// retained.
const maxBodyCapture = 8 << 20

// Ring is a bounded, in-memory log of LLM HTTP exchanges. It keeps at most
// maxEntries exchanges and evicts the oldest once the retained request and
// response bodies exceed maxBytes (the newest exchange is always kept).
type Ring struct {
	maxEntries int
	maxBytes   int

	mu      sync.Mutex
	nextID  int64
	entries []*exchange // oldest first
	bytes   int         // retained body bytes across entries
}

// NewRing returns an empty Ring with the given bounds.
func NewRing(maxEntries, maxBytes int) *Ring {
	return &Ring{maxEntries: maxEntries, maxBytes: maxBytes}
}

// Exchange is a snapshot of one recorded request/response. Durations are in
// milliseconds; DurationMs is the time so far while the exchange is in
// flight. Headers and bodies are filled in only by Get. A body is truncated
// at maxBodyCapture, and a request body is not captured at all if the request
// has no working GetBody.
type Exchange struct {
	ID                int64       `json:"id"`
	Start             time.Time   `json:"start"`
	Method            string      `json:"method"`
	URL               string      `json:"url"`
	Provider          string      `json:"provider"`
	Model             string      `json:"model"`
	ConversationID    string      `json:"conversation_id"`
	RequestID         string      `json:"request_id"`
	Status            int         `json:"status"`
	Error             string      `json:"error"`
	Done              bool        `json:"done"`
	TTFBMs            int64       `json:"ttfb_ms"`
	DurationMs        int64       `json:"duration_ms"`
	RequestBytes      int         `json:"request_bytes"`
	ResponseBytes     int         `json:"response_bytes"`
	RequestTruncated  bool        `json:"request_truncated"`
	ResponseTruncated bool        `json:"response_truncated"`
	RequestHeader     http.Header `json:"request_header,omitempty"`
	ResponseHeader    http.Header `json:"response_header,omitempty"`
	RequestBody       string      `json:"request_body,omitempty"`
	ResponseBody      string      `json:"response_body,omitempty"`
	// ResponseTimeline records when the captured response body arrived, as
	// [end byte offset, ms since Start] pairs, at most one per millisecond.
	// The debug page maps streamed events onto it to derive TTFT, the time
	// between tokens, and tokens per second. Filled in only by Get.
	ResponseTimeline [][2]int64 `json:"response_timeline,omitempty"`
}

// exchange is the live record behind an Exchange; it is guarded by Ring.mu.
// requestBody is never mutated and responseBody is append-only, so slices of
// them may be read after unlocking.
type exchange struct {
	Exchange
	requestBody, responseBody []byte
	headersAt, end            time.Time
	evicted                   bool
}

func (e *exchange) snapshot() Exchange {
	x := e.Exchange
	x.Done = !e.end.IsZero()
	x.RequestBytes, x.ResponseBytes = len(e.requestBody), len(e.responseBody)
	if !e.headersAt.IsZero() {
		x.TTFBMs = e.headersAt.Sub(e.Start).Milliseconds()
	}
	end := e.end
	if !x.Done {
		end = time.Now()
	}
	x.DurationMs = end.Sub(e.Start).Milliseconds()
	return x
}

// List returns all retained exchanges, newest first, without headers or bodies.
func (r *Ring) List() []Exchange {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Exchange, len(r.entries))
	for i, e := range r.entries {
		x := e.snapshot()
		x.RequestHeader, x.ResponseHeader, x.ResponseTimeline = nil, nil, nil
		out[len(out)-1-i] = x
	}
	return out
}

// Get returns the exchange with the given id, if still retained.
func (r *Ring) Get(id int64) (Exchange, bool) {
	r.mu.Lock()
	i := slices.IndexFunc(r.entries, func(e *exchange) bool { return e.ID == id })
	if i < 0 {
		r.mu.Unlock()
		return Exchange{}, false
	}
	e := r.entries[i]
	x, req, resp := e.snapshot(), e.requestBody, e.responseBody
	x.ResponseTimeline = slices.Clone(e.ResponseTimeline) // its last pair is updated in place
	r.mu.Unlock()
	x.RequestBody, x.ResponseBody = string(req), string(resp) // copy outside the lock
	return x, true
}

// begin records the start of req and adds the exchange to the ring.
func (r *Ring) begin(req *http.Request) *exchange {
	body, truncated := captureRequestBody(req)
	ctx := req.Context()
	e := &exchange{Exchange: Exchange{
		Start:            time.Now(),
		Method:           req.Method,
		URL:              redactURL(req.URL),
		Provider:         ProviderFromContext(ctx),
		Model:            ModelIDFromContext(ctx),
		ConversationID:   ConversationIDFromContext(ctx),
		RequestID:        req.Header.Get(shelleyRequestIDHeader),
		RequestHeader:    redactHeader(req.Header),
		RequestTruncated: truncated,
	}, requestBody: body}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	e.ID = r.nextID
	r.entries = append(r.entries, e)
	r.bytes += len(body)
	r.trimLocked()
	return e
}

// captureRequestBody copies req's body via req.GetBody, which LLM clients get
// for free by building requests from in-memory readers, so req itself is left
// untouched. Recording must never fail a request, so a body that can't be
// replayed is simply not captured.
func captureRequestBody(req *http.Request) (body []byte, truncated bool) {
	if req.GetBody == nil {
		return nil, false
	}
	rc, err := req.GetBody()
	if err != nil {
		return nil, false
	}
	defer rc.Close()
	body, err = io.ReadAll(io.LimitReader(rc, maxBodyCapture+1))
	if err != nil {
		return nil, false
	}
	if len(body) > maxBodyCapture {
		body, truncated = body[:maxBodyCapture], true
	}
	return slices.Clone(body), truncated // Clone drops ReadAll's spare capacity
}

// finish records the outcome of RoundTrip. On success it wraps resp.Body so
// the response is captured as the caller reads it.
func (r *Ring) finish(e *exchange, resp *http.Response, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		e.Error = err.Error()
		e.end = time.Now()
		return
	}
	e.Status = resp.StatusCode
	e.ResponseHeader = redactHeader(resp.Header)
	e.headersAt = time.Now()
	resp.Body = &captureBody{ReadCloser: resp.Body, ring: r, ex: e}
}

// trimLocked evicts the oldest exchanges until the ring is within bounds.
// Evicted exchanges drop their bodies and stop capturing.
func (r *Ring) trimLocked() {
	n := 0
	for len(r.entries)-n > 1 && (len(r.entries)-n > r.maxEntries || r.bytes > r.maxBytes) {
		e := r.entries[n]
		r.bytes -= len(e.requestBody) + len(e.responseBody)
		e.requestBody, e.responseBody, e.ResponseTimeline, e.evicted = nil, nil, nil, true
		n++
	}
	r.entries = slices.Delete(r.entries, 0, n)
}

// captureBody tees a response body into its exchange.
type captureBody struct {
	io.ReadCloser
	ring *Ring
	ex   *exchange
}

func (b *captureBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.ring.mu.Lock()
	defer b.ring.mu.Unlock()
	e := b.ex
	if e.evicted || !e.end.IsZero() {
		return n, err
	}
	keep := min(n, maxBodyCapture-len(e.responseBody))
	e.responseBody = append(e.responseBody, p[:keep]...)
	e.ResponseTruncated = e.ResponseTruncated || keep < n
	b.ring.bytes += keep
	if keep > 0 {
		e.recordArrival(time.Now())
	}
	if err != nil {
		if !errors.Is(err, io.EOF) {
			e.Error = err.Error()
		}
		e.end = time.Now()
		e.responseBody = slices.Clone(e.responseBody) // drop append's spare capacity
	}
	b.ring.trimLocked()
	return n, err
}

// recordArrival notes that the response body captured so far had arrived by
// now, merging arrivals within the same millisecond.
func (e *exchange) recordArrival(now time.Time) {
	p := [2]int64{int64(len(e.responseBody)), now.Sub(e.Start).Milliseconds()}
	if n := len(e.ResponseTimeline); n > 0 && e.ResponseTimeline[n-1][1] == p[1] {
		e.ResponseTimeline[n-1] = p
		return
	}
	e.ResponseTimeline = append(e.ResponseTimeline, p)
}

func (b *captureBody) Close() error {
	err := b.ReadCloser.Close()
	b.ring.mu.Lock()
	defer b.ring.mu.Unlock()
	if b.ex.end.IsZero() {
		b.ex.end = time.Now()
	}
	return err
}

// isSecret reports whether a header or query parameter name likely carries a
// credential. "token" must end the name so rate-limit "...-tokens-..." survive.
func isSecret(name string) bool {
	name = strings.ToLower(name)
	return strings.HasSuffix(name, "token") || slices.ContainsFunc(
		[]string{"auth", "key", "secret", "cookie", "password"},
		func(s string) bool { return strings.Contains(name, s) },
	)
}

const redacted = "[redacted]"

func redactHeader(h http.Header) http.Header {
	h = h.Clone()
	for name := range h {
		if isSecret(name) {
			h[name] = []string{redacted}
		}
	}
	return h
}

// redactURL drops userinfo and redacts secret-looking query parameters.
func redactURL(u *url.URL) string {
	q, _ := url.ParseQuery(u.RawQuery) // drops unparseable pairs, so they can't leak
	for name := range q {
		if isSecret(name) {
			q.Set(name, redacted)
		}
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path, RawQuery: q.Encode()}).String()
}
