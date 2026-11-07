package gemini

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

func streamData(events ...string) string {
	return "data: " + strings.Join(events, "\n\ndata: ") + "\n\n"
}

func streamModel(body io.Reader) Model {
	return Model{
		Model: "models/test", Endpoint: "https://example.com/v1beta", APIKey: "secret",
		HTTPC: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"text/event-stream; charset=utf-8"}},
				Body:       io.NopCloser(body),
			}, nil
		})},
	}
}

func TestGenerateContentSSE(t *testing.T) {
	body := streamData(
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"Think ","thought":true}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1}}`,
		`{"candidates":[{"content":{"parts":[{"text":"carefully.","thought":true}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"Hello "}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"world"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"trailing-signature"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"id":"call_one","name":"bash","args":{"command":"pwd"}},"thoughtSignature":"call-signature"},{"functionCall":{"id":"call_two","name":"bash","args":{"command":"ls"}}}]}}]}`,
		`{"candidates":[{"finishReason":"STOP"}]}`,
		`{"usageMetadata":{"promptTokenCount":12,"cachedContentTokenCount":2,"candidatesTokenCount":4,"thoughtsTokenCount":3}}`,
	)
	model := streamModel(strings.NewReader(body))
	transport := model.HTTPC.Transport
	model.HTTPC.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v1beta/models/test:streamGenerateContent" || req.URL.Query().Get("alt") != "sse" {
			t.Errorf("not using SSE endpoint: %s", req.URL)
		}
		if req.Header.Get("Accept") != "text/event-stream" || req.Header.Get("x-goog-api-key") != "secret" {
			t.Errorf("unexpected request headers: %v", req.Header)
		}
		response, err := transport.RoundTrip(req)
		response.Header.Set("Exedev-Gateway-Cost", "0.001")
		return response, err
	})
	response, err := model.GenerateContent(t.Context(), &Request{})
	if err != nil {
		t.Fatal(err)
	}
	wantParts := []Part{
		{Text: "Think carefully.", Thought: true},
		{Text: "Hello world"},
		{ThoughtSignature: "trailing-signature"},
		{FunctionCall: &FunctionCall{ID: "call_one", Name: "bash", Args: map[string]any{"command": "pwd"}}, ThoughtSignature: "call-signature"},
		{FunctionCall: &FunctionCall{ID: "call_two", Name: "bash", Args: map[string]any{"command": "ls"}}},
	}
	if len(response.Candidates) != 1 {
		t.Fatalf("candidates = %#v", response.Candidates)
	}
	candidate := response.Candidates[0]
	if !reflect.DeepEqual(candidate.Content.Parts, wantParts) {
		t.Errorf("parts = %#v, want %#v", candidate.Content.Parts, wantParts)
	}
	if candidate.Content.Role != "model" || candidate.FinishReason != "STOP" {
		t.Errorf("lost candidate metadata: %#v", candidate)
	}
	if got, want := response.UsageMetadata, (&UsageMetadata{
		PromptTokenCount: 12, CachedContentTokenCount: 2, CandidatesTokenCount: 4, ThoughtsTokenCount: 3,
	}); !reflect.DeepEqual(got, want) {
		t.Errorf("usage = %#v, want final snapshot %#v", got, want)
	}
	if response.Header().Get("Exedev-Gateway-Cost") != "0.001" {
		t.Error("lost response cost header")
	}
}

func TestGenerateContentSSEPreservesSignedBoundaries(t *testing.T) {
	parts := []Part{
		{Text: "unsigned"},
		{Text: "signed one", ThoughtSignature: "sig"},
		{Text: "signed two", ThoughtSignature: "sig"},
		{Text: "unsigned after"},
		{Thought: true, ThoughtSignature: "thinking-tail"},
	}
	var events []string
	for _, part := range parts {
		b, err := json.Marshal(Response{Candidates: []Candidate{{Content: Content{Parts: []Part{part}}}}})
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, string(b))
	}
	events = append(events, `{"candidates":[{"finishReason":"STOP"}]}`)
	response, err := streamModel(strings.NewReader(streamData(events...))).GenerateContent(t.Context(), &Request{})
	if err != nil {
		t.Fatal(err)
	}
	if got := response.Candidates[0].Content.Parts; !reflect.DeepEqual(got, parts) {
		t.Errorf("signed boundaries changed: %#v, want %#v", got, parts)
	}
}

func TestGenerateContentSSEPreservesGroundingPartIndexes(t *testing.T) {
	response, err := streamModel(strings.NewReader(streamData(
		`{"candidates":[{"content":{"parts":[{"text":"First."},{"text":"Second."}]}}]}`,
		`{"candidates":[{"finishReason":"STOP","groundingMetadata":{"groundingSupports":[{"segment":{"partIndex":1,"endIndex":7}}]}}]}`,
	))).GenerateContent(t.Context(), &Request{})
	if err != nil {
		t.Fatal(err)
	}
	candidate := response.Candidates[0]
	if len(candidate.Content.Parts) != 2 || candidate.Content.Parts[1].Text != "Second." ||
		candidate.GroundingMetadata.GroundingSupports[0].Segment.PartIndex != 1 {
		t.Fatalf("distinct content parts must retain their grounding indexes: %+v", candidate)
	}
}

func TestGenerateContentSSEFraming(t *testing.T) {
	text := strings.Repeat("☃", 32*1024)
	data, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(": keepalive\r\nid: ignored\r\nevent: message\r\ndata: {\"candidates\":\r\ndata: [{\"content\":{\"parts\":[{\"text\":%s}]},\"finishReason\":\"STOP\"}]}\r\n\r\n", data)
	response, err := streamModel(strings.NewReader(body)).GenerateContent(t.Context(), &Request{})
	if err != nil {
		t.Fatal(err)
	}
	if got := response.Candidates[0].Content.Parts[0].Text; got != text {
		t.Fatalf("large UTF-8 text changed: length %d, want %d", len(got), len(text))
	}
}

func TestGenerateContentSSEFailures(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
		wantEOF bool
	}{
		{name: "empty", wantEOF: true},
		{name: "comments only", body: ": keepalive\n\n", wantEOF: true},
		{name: "partial response", body: streamData(`{"candidates":[{"content":{"parts":[{"text":"Partial"}]}}]}`), wantEOF: true},
		{name: "unspecified finish", body: streamData(`{"candidates":[{"finishReason":"FINISH_REASON_UNSPECIFIED"}]}`), wantEOF: true},
		{name: "bad JSON", body: streamData(`{"candidates":`), wantErr: "decoding"},
		{name: "error after output", body: streamData(`{"candidates":[{"content":{"parts":[{"text":"Partial"}]}}]}`, `{"error":{"code":503,"message":"provider failed","status":"UNAVAILABLE"}}`), wantErr: "provider failed"},
		{name: "bad event after finish", body: streamData(`{"candidates":[{"finishReason":"STOP"}]}`, `invalid`), wantErr: "decoding"},
		{name: "unexpected candidate", body: streamData(`{"candidates":[{"index":1,"finishReason":"STOP"}]}`), wantErr: "candidate"},
		{name: "unterminated finish frame", body: `data: {"candidates":[{"finishReason":"STOP"}]}`, wantEOF: true},
		{name: "unterminated data after finish", body: streamData(`{"candidates":[{"finishReason":"STOP"}]}`) + `data: {"error":{"message":"unframed error"}}`, wantEOF: true},
		{name: "unsupported response part", body: streamData(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AA=="}}]}}]}`), wantErr: "unsupported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response, err := streamModel(strings.NewReader(tt.body)).GenerateContent(t.Context(), &Request{})
			if err == nil || response != nil {
				t.Fatalf("response = %#v, error = %v, want failure", response, err)
			}
			if tt.wantEOF && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("error = %v, want unexpected EOF", err)
			}
			if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want %q", err, tt.wantErr)
			}
			var apiErr *APIError
			if errors.As(err, &apiErr) {
				t.Errorf("stream failure must not be retried as an HTTP error: %v", apiErr)
			}
		})
	}
}

func TestGenerateContentSSEPromptBlock(t *testing.T) {
	body := streamData(
		`{"promptFeedback":{"blockReason":"SAFETY","blockReasonMessage":"Blocked"}}`,
		`{"usageMetadata":{"promptTokenCount":12}}`,
	)
	response, err := streamModel(strings.NewReader(body)).GenerateContent(t.Context(), &Request{})
	if err != nil {
		t.Fatal(err)
	}
	if response.PromptFeedback == nil || response.PromptFeedback.BlockReason != "SAFETY" || response.UsageMetadata == nil || response.UsageMetadata.PromptTokenCount != 12 {
		t.Fatalf("lost prompt block or trailing usage: %#v", response)
	}
}

func TestGenerateContentRejectsJSONResponse(t *testing.T) {
	model := streamModel(strings.NewReader(`{"candidates":[{"finishReason":"STOP"}]}`))
	transport := model.HTTPC.Transport
	model.HTTPC.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(req)
		response.Header.Set("Content-Type", "application/json")
		return response, err
	})
	if response, err := model.GenerateContent(t.Context(), &Request{}); err == nil || response != nil {
		t.Fatalf("non-streaming response accepted: %#v, %v", response, err)
	}
}

func TestGenerateContentHTTPError(t *testing.T) {
	model := Model{
		Model: "models/test", APIKey: "key",
		HTTPC: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header:     http.Header{"Retry-After": {"120"}},
				Body:       io.NopCloser(strings.NewReader("overloaded")),
			}, nil
		})},
	}
	_, err := model.GenerateContent(t.Context(), &Request{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 503 || apiErr.Header.Get("Retry-After") != "120" || apiErr.Body != "overloaded" {
		t.Fatalf("lost retryable HTTP error: %v", err)
	}
}

func TestGenerateContentSSEReadFailureAfterFinish(t *testing.T) {
	body := io.MultiReader(
		strings.NewReader(streamData(`{"candidates":[{"finishReason":"STOP"}]}`)),
		iotest.ErrReader(io.ErrUnexpectedEOF),
	)
	response, err := streamModel(body).GenerateContent(t.Context(), &Request{})
	if !errors.Is(err, io.ErrUnexpectedEOF) || response != nil {
		t.Fatalf("read failure after finish accepted: %#v, %v", response, err)
	}
}

func TestPartJSONPayload(t *testing.T) {
	for _, part := range []Part{
		{},
		{ThoughtSignature: "text-tail"},
		{Thought: true, ThoughtSignature: "thinking-tail"},
		{FunctionCall: &FunctionCall{ID: "call", Name: "bash"}, ThoughtSignature: "function-signature"},
	} {
		body, err := json.Marshal(part)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Fatal(err)
		}
		text, hasText := fields["text"]
		if hasText != (part.FunctionCall == nil) || hasText && string(text) != `""` {
			t.Errorf("changed text payload type: %s", body)
		}
		var replay Part
		if err := json.Unmarshal(body, &replay); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(replay, part) {
			t.Errorf("part = %#v, want %#v", replay, part)
		}
	}
}
