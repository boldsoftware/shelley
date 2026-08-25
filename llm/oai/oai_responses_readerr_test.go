package oai

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/llm"
)

// resetAfterHeaders writes response headers plus a Content-Length that promises
// more body than it delivers, then aborts the TCP connection so the client's
// body read fails with ECONNRESET rather than a clean io.ErrUnexpectedEOF.
//
// It runs on the httptest server's goroutine, so it reports setup failures via
// t.Errorf rather than t.Fatalf: FailNow from a non-test goroutine is undefined
// and would leave the client blocked until its timeout instead of failing fast.
func resetAfterHeaders(t *testing.T, w http.ResponseWriter, status int, contentType, body string) {
	t.Helper()
	hj, ok := w.(http.Hijacker)
	if !ok {
		t.Errorf("ResponseWriter is not a Hijacker")
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		t.Errorf("Hijack() error = %v", err)
		return
	}
	defer conn.Close()
	if tcp, ok := conn.(*net.TCPConn); ok {
		// Linger 0 makes Close() send RST instead of FIN.
		if err := tcp.SetLinger(0); err != nil {
			t.Errorf("SetLinger() error = %v", err)
			return
		}
	}
	// Content-Length overstates the body, so the client is still reading when
	// the RST arrives.
	fmt.Fprintf(buf, "HTTP/1.1 %d %s\r\nContent-Type: %s\r\nContent-Length: 4096\r\n\r\n", status, http.StatusText(status), contentType)
	fmt.Fprint(buf, body)
	if err := buf.Flush(); err != nil {
		t.Errorf("Flush() error = %v", err)
	}
}

// respondOK writes a minimal successful Responses reply.
func respondOK(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(responsesResponse{
		ID:     "retry-ok",
		Status: "completed",
		Output: []responsesOutputItem{{Type: "message", Role: "assistant", Content: []responsesContent{{Type: "output_text", Text: "ok"}}}},
		Usage:  responsesUsage{InputTokens: 1, OutputTokens: 1},
	}); err != nil {
		t.Errorf("Encode() error = %v", err)
	}
}

// A 5xx whose body read is severed by a connection reset is still a 5xx: the
// gateway/provider failed transiently, so it must be retried rather than
// surfaced as a terminal error just because we couldn't read the explanation.
func TestResponsesServiceRetriesServerErrorWithResetBody(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			resetAfterHeaders(t, w, http.StatusBadGateway, "text/plain", "gateway proxy: upstream request failed (trace: abc123)")
			return
		}
		respondOK(t, w)
	}))
	defer server.Close()

	var retries []llm.RetryEvent
	svc := &ResponsesService{APIKey: "test-api-key", Model: GPT41, ModelURL: server.URL, Backoff: []time.Duration{0}}
	resp, err := svc.Do(context.Background(), &llm.Request{
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
		OnRetry:  func(event llm.RetryEvent) { retries = append(retries, event) },
	})
	if err != nil {
		t.Fatalf("Do() error after %d attempt(s) = %v", attempts, err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if len(retries) != 1 {
		t.Fatalf("retry events = %d, want 1", len(retries))
	}
	if got := retries[0].Status; got != http.StatusBadGateway {
		t.Errorf("retry status = %d, want 502", got)
	}
	// The partial body that did arrive is the useful part of the diagnosis; the
	// truncation note explains why it looks cut off.
	if got := retries[0].Err; !strings.Contains(got, "trace: abc123") || !strings.Contains(got, "body truncated") {
		t.Errorf("retry summary = %q, want partial body and truncation note", got)
	}
	if got := resp.Content[0].Text; got != "ok" {
		t.Fatalf("response text = %q, want ok", got)
	}
}

// A 4xx whose body read is severed stays terminal: retrying a client error
// just burns the retry budget on a request that will never succeed.
func TestResponsesServiceDoesNotRetryClientErrorWithResetBody(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		resetAfterHeaders(t, w, http.StatusBadRequest, "text/plain", "bad request")
	}))
	defer server.Close()

	svc := &ResponsesService{APIKey: "test-api-key", Model: GPT41, ModelURL: server.URL, Backoff: []time.Duration{0}}
	_, err := svc.Do(context.Background(), &llm.Request{
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
	})
	if err == nil {
		t.Fatalf("Do() error = nil, want client error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error = %v, want it to mention status 400", err)
	}
	// The truncation note must not smuggle transport phrases into a terminal
	// error: loop.IsRetryableLLMError pattern-matches them and would offer a
	// pointless Retry for a request that can never succeed.
	for _, phrase := range []string{"connection reset", "reset by peer", "broken pipe", "i/o timeout", "eof"} {
		if strings.Contains(strings.ToLower(err.Error()), phrase) {
			t.Errorf("error = %v, must not contain transport phrase %q", err, phrase)
		}
	}
}

// Every retryable branch must report the status of the attempt that actually
// failed. A retry banner inherits whatever the previous attempt left behind, so
// a branch that forgets to reset the status makes the banner assert a stale one
// -- e.g. claiming "status 502" for an attempt that returned 200.
func TestResponsesServiceRetryBannerStatusMatchesAttempt(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		switch attempts {
		case 1:
			http.Error(w, "gateway boom", http.StatusBadGateway)
		case 2:
			// 200 with an empty body: retried via the decode path, not a status.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
		default:
			respondOK(t, w)
		}
	}))
	defer server.Close()

	var retries []llm.RetryEvent
	svc := &ResponsesService{APIKey: "test-api-key", Model: GPT41, ModelURL: server.URL, Backoff: []time.Duration{0}}
	if _, err := svc.Do(context.Background(), &llm.Request{
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
		OnRetry:  func(event llm.RetryEvent) { retries = append(retries, event) },
	}); err != nil {
		t.Fatalf("Do() error after %d attempt(s) = %v", attempts, err)
	}
	if len(retries) != 2 {
		t.Fatalf("retry events = %d, want 2", len(retries))
	}
	if got := retries[0].Status; got != http.StatusBadGateway {
		t.Errorf("retry[0].Status = %d, want 502", got)
	}
	if got := retries[1].Status; got != 0 {
		t.Errorf("retry[1].Status = %d, want 0 (the attempt returned 200)", got)
	}
	if got := retries[1].Err; !strings.Contains(got, "decode") {
		t.Errorf("retry[1].Err = %q, want it to describe the decode failure", got)
	}
}

// A severed read of a 200 body is a transport hiccup, not a verdict on the
// request: retry it. The SSE branch already retries any stream error, so the
// JSON branch must not be stricter — the two differ only in framing.
func TestResponsesServiceRetriesOKBodyReset(t *testing.T) {
	for _, tt := range []struct {
		name        string
		contentType string
		partial     string
	}{
		{name: "json", contentType: "application/json", partial: `{"id":"x","status":"comp`},
		{name: "sse", contentType: "text/event-stream", partial: "event: response.output_text.delta\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				if attempts == 1 {
					resetAfterHeaders(t, w, http.StatusOK, tt.contentType, tt.partial)
					return
				}
				respondOK(t, w)
			}))
			defer server.Close()

			svc := &ResponsesService{APIKey: "test-api-key", Model: GPT41, ModelURL: server.URL, Backoff: []time.Duration{0}}
			resp, err := svc.Do(context.Background(), &llm.Request{
				Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
			})
			if err != nil {
				t.Fatalf("Do() error after %d attempt(s) = %v", attempts, err)
			}
			if attempts != 2 {
				t.Fatalf("attempts = %d, want 2", attempts)
			}
			if got := resp.Content[0].Text; got != "ok" {
				t.Fatalf("response text = %q, want ok", got)
			}
		})
	}
}
