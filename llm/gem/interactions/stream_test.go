package interactions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
)

func sseEvent(name, data string) string {
	return "event: " + name + "\ndata: " + data + "\n\n"
}

func TestClientCreateTextAndThinking(t *testing.T) {
	var gotPath, gotKey, gotRevision, gotAccept string
	var gotReq Request

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-goog-api-key")
		gotRevision = r.Header.Get("Api-Revision")
		gotAccept = r.Header.Get("Accept")
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("X-Test-Header", "ok")
		_, _ = io.WriteString(w,
			sseEvent("interaction.created", `{"event_type":"interaction.created","interaction":{"id":"int_1","model":"gemini-4-hydrogen","status":"in_progress"}}`)+
				sseEvent("interaction.status_update", `{"event_type":"interaction.status_update","status":"in_progress"}`)+
				sseEvent("step.start", `{"event_type":"step.start","index":0,"step":{"type":"thought"}}`)+
				sseEvent("step.delta", `{"event_type":"step.delta","index":0,"delta":{"type":"thought_summary","content":{"type":"text","text":"Thinking "}}}`)+
				sseEvent("step.delta", `{"event_type":"step.delta","index":0,"delta":{"type":"thought_summary","content":{"type":"text","text":"hard."}}}`)+
				sseEvent("step.delta", `{"event_type":"step.delta","index":0,"delta":{"type":"thought_signature","signature":"sig_abc"}}`)+
				sseEvent("step.stop", `{"event_type":"step.stop","index":0}`)+
				sseEvent("step.start", `{"event_type":"step.start","index":1,"step":{"type":"model_output"}}`)+
				sseEvent("step.delta", `{"event_type":"step.delta","index":1,"delta":{"type":"text","text":"Hello "}}`)+
				sseEvent("step.delta", `{"event_type":"step.delta","index":1,"delta":{"type":"text","text":"world!"}}`)+
				sseEvent("step.stop", `{"event_type":"step.stop","index":1}`)+
				sseEvent("interaction.completed", `{"event_type":"interaction.completed","interaction":{"id":"int_1","status":"completed","usage":{"total_tokens":30,"total_input_tokens":10,"total_cached_tokens":2,"total_output_tokens":8,"total_thought_tokens":12}}}`)+
				sseEvent("done", `[DONE]`))
	}))
	defer srv.Close()

	var deltas []llm.StreamDelta
	store := false
	client := Client{
		Endpoint: srv.URL + "/v1beta",
		APIKey:   "test-key",
		HTTPC:    srv.Client(),
		OnStream: func(d llm.StreamDelta) { deltas = append(deltas, d) },
	}
	res, err := client.Create(context.Background(), &Request{
		Model:  "gemini-4-hydrogen",
		Store:  &store,
		Stream: true,
		Input:  []Step{{Type: "user_input", Content: []Content{{Type: "text", Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if gotPath != "/v1beta/interactions" {
		t.Errorf("path = %q, want /v1beta/interactions", gotPath)
	}
	if gotKey != "test-key" {
		t.Errorf("x-goog-api-key = %q, want test-key", gotKey)
	}
	if gotRevision != APIRevision {
		t.Errorf("Api-Revision = %q, want %q", gotRevision, APIRevision)
	}
	if gotAccept != "text/event-stream" {
		t.Errorf("Accept = %q, want text/event-stream", gotAccept)
	}
	if gotReq.Store == nil || *gotReq.Store {
		t.Errorf("Store = %v, want false", gotReq.Store)
	}
	if res.Header().Get("X-Test-Header") != "ok" {
		t.Errorf("missing captured HTTP header")
	}
	if res.ID != "int_1" || res.Status != "completed" {
		t.Errorf("res = %+v, want id=int_1 status=completed", res)
	}
	if len(res.Outputs) != 2 {
		t.Fatalf("len(Outputs) = %d, want 2", len(res.Outputs))
	}
	if res.Outputs[0].Type != "thought" || res.Outputs[0].Signature != "sig_abc" || len(res.Outputs[0].Summary) != 1 || res.Outputs[0].Summary[0].Text != "Thinking hard." {
		t.Errorf("unexpected thought step: %+v", res.Outputs[0])
	}
	if res.Outputs[1].Type != "model_output" || len(res.Outputs[1].Content) != 1 || res.Outputs[1].Content[0].Text != "Hello world!" {
		t.Errorf("unexpected model_output step: %+v", res.Outputs[1])
	}
	if len(deltas) != 4 || deltas[0].Type != "thinking" || deltas[0].Text != "Thinking " || deltas[2].Type != "text" || deltas[3].Text != "world!" {
		t.Errorf("unexpected deltas: %+v", deltas)
	}
}

func TestClientCreateFunctionCallChunkedArgs(t *testing.T) {
	stream := sseEvent("interaction.created", `{"event_type":"interaction.created","interaction":{"model":"gemini-4-hydrogen"}}`) +
		sseEvent("step.start", `{"event_type":"step.start","index":0,"step":{"type":"function_call","id":"call_42","name":"get_weather","arguments":{}}}`) +
		sseEvent("step.delta", `{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"city\":"}}`) +
		sseEvent("step.delta", `{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"\"Paris\"}"}}`) +
		sseEvent("step.stop", `{"event_type":"step.stop","index":0}`) +
		sseEvent("interaction.completed", `{"event_type":"interaction.completed","interaction":{"status":"requires_action","usage":{"total_input_tokens":5,"total_output_tokens":4}}}`)

	res, err := parseStreamResponse(context.Background(), strings.NewReader(stream), nil)
	if err != nil {
		t.Fatalf("parseStreamResponse: %v", err)
	}
	if res.Status != "requires_action" {
		t.Errorf("Status = %q, want requires_action", res.Status)
	}
	if len(res.Outputs) != 1 {
		t.Fatalf("len(Outputs) = %d, want 1", len(res.Outputs))
	}
	step := res.Outputs[0]
	if step.Type != "function_call" || step.ID != "call_42" || step.Name != "get_weather" || string(step.Arguments) != `{"city":"Paris"}` {
		t.Errorf("unexpected function_call step: %+v (args=%s)", step, string(step.Arguments))
	}
}

func TestClientCreateGoogleSearchAndAnnotations(t *testing.T) {
	stream := sseEvent("step.start", `{"event_type":"step.start","index":0,"step":{"id":"call_s1","future_start":1,"signature":"","type":"google_search_call","search_type":"web_search"}}`) +
		sseEvent("step.delta", `{"event_type":"step.delta","index":0,"delta":{"type":"google_search_call","signature":"sig_call","future_delta":[3],"arguments":{"queries":["go 1.27"]}}}`) +
		sseEvent("step.stop", `{"event_type":"step.stop","index":0}`) +
		sseEvent("step.start", `{"event_type":"step.start","index":1,"step":{"call_id":"call_s1","signature":"","type":"google_search_result"}}`) +
		sseEvent("step.delta", `{"event_type":"step.delta","index":1,"delta":{"type":"google_search_result","signature":"sig_res","result":[{"search_suggestions":"<div>chip</div>"}],"is_error":false}}`) +
		sseEvent("step.stop", `{"event_type":"step.stop","index":1}`) +
		sseEvent("step.start", `{"event_type":"step.start","index":2,"step":{"type":"model_output"}}`) +
		sseEvent("step.delta", `{"event_type":"step.delta","index":2,"delta":{"type":"text","text":"Go 1.27 is out."}}`) +
		sseEvent("step.delta", `{"event_type":"step.delta","index":2,"delta":{"type":"text_annotation_delta","annotations":[{"type":"url_citation","start_index":0,"end_index":15,"url":"https://go.dev","title":"go.dev"}]}}`) +
		sseEvent("step.stop", `{"event_type":"step.stop","index":2}`) +
		sseEvent("interaction.completed", `{"event_type":"interaction.completed","interaction":{"status":"completed","usage":{"total_input_tokens":10,"total_output_tokens":5}}}`)

	res, err := parseStreamResponse(context.Background(), strings.NewReader(stream), nil)
	if err != nil {
		t.Fatalf("parseStreamResponse: %v", err)
	}
	if len(res.Outputs) != 3 {
		t.Fatalf("len(Outputs) = %d, want 3", len(res.Outputs))
	}
	if res.Outputs[0].Signature != "sig_call" || string(res.Outputs[0].Arguments) != `{"queries":["go 1.27"]}` {
		t.Errorf("unexpected google_search_call: %+v", res.Outputs[0])
	}
	if res.Outputs[1].CallID != "call_s1" || res.Outputs[1].Signature != "sig_res" || !strings.Contains(string(res.Outputs[1].Result), "search_suggestions") {
		t.Errorf("unexpected google_search_result: %+v", res.Outputs[1])
	}
	if len(res.Outputs[2].Content) != 1 || len(res.Outputs[2].Content[0].Annotations) != 1 || res.Outputs[2].Content[0].Annotations[0].URL != "https://go.dev" {
		t.Errorf("unexpected model_output annotations: %+v", res.Outputs[2])
	}

	got, err := json.Marshal(res.Outputs[0])
	if err != nil || !strings.Contains(string(got), `"future_start":1`) || !strings.Contains(string(got), `"future_delta":[3]`) {
		t.Fatalf("streamed search step lost native fields: %s (%v)", got, err)
	}

	// Verify opaque raw JSON preservation on Step round-trip for unknown fields.
	raw := `{"type":"google_search_result","call_id":"call_s1","signature":"sig_res","extra_opaque":"keep_me","result":[{"search_suggestions":"<div>chip</div>"}]}`
	var step Step
	if err := json.Unmarshal([]byte(raw), &step); err != nil {
		t.Fatalf("unmarshal step: %v", err)
	}
	remarshaled, err := json.Marshal(step)
	if err != nil {
		t.Fatalf("marshal step: %v", err)
	}
	if !strings.Contains(string(remarshaled), `"extra_opaque":"keep_me"`) {
		t.Errorf("opaque field lost across Step round-trip: %s", remarshaled)
	}
}

func TestClientCreateStreamErrors(t *testing.T) {
	t.Run("in-band error event", func(t *testing.T) {
		stream := sseEvent("error", `{"event_type":"error","error":{"code":"invalid_request","message":"bad input"}}`)
		_, err := parseStreamResponse(context.Background(), strings.NewReader(stream), nil)
		if err == nil || !strings.Contains(err.Error(), "invalid_request") {
			t.Fatalf("expected invalid_request error, got %v", err)
		}
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			t.Errorf("in-band stream error must not be *APIError")
		}
	})

	t.Run("truncated without completion", func(t *testing.T) {
		stream := sseEvent("step.start", `{"event_type":"step.start","index":0,"step":{"type":"model_output"}}`)
		_, err := parseStreamResponse(context.Background(), strings.NewReader(stream), nil)
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("expected io.ErrUnexpectedEOF, got %v", err)
		}
	})

	t.Run("unterminated SSE data", func(t *testing.T) {
		stream := "event: step.delta\ndata: {\"event_type\":\"step.delta\"}"
		_, err := parseStreamResponse(context.Background(), strings.NewReader(stream), nil)
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("expected io.ErrUnexpectedEOF, got %v", err)
		}
	})

	t.Run("HTTP 429 APIError with Retry-After", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "3")
			http.Error(w, "rate limited", http.StatusTooManyRequests)
		}))
		defer srv.Close()
		client := Client{Endpoint: srv.URL, APIKey: "k", HTTPC: srv.Client()}
		_, err := client.Create(context.Background(), &Request{Model: "gemini-4-hydrogen"})
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != 429 || apiErr.Header.Get("Retry-After") != "3" {
			t.Fatalf("expected 429 *APIError with Retry-After, got %v", err)
		}
	})

	t.Run("wrong content type", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{}`)
		}))
		defer srv.Close()
		client := Client{Endpoint: srv.URL, APIKey: "k", HTTPC: srv.Client()}
		_, err := client.Create(context.Background(), &Request{Model: "gemini-4-hydrogen"})
		if err == nil || !strings.Contains(err.Error(), "text/event-stream") {
			t.Fatalf("expected text/event-stream error, got %v", err)
		}
	})
}

func TestInterleavedFunctionArguments(t *testing.T) {
	stream := sseEvent("step.start", `{"index":0,"step":{"type":"function_call","id":"call_a","name":"a"}}`) +
		sseEvent("step.delta", `{"index":0,"delta":{"type":"arguments_delta","arguments":"{\"v\":"}}`) +
		sseEvent("step.start", `{"index":1,"step":{"type":"function_call","id":"call_b","name":"b","arguments":{}}}`) +
		sseEvent("step.delta", `{"index":0,"delta":{"type":"arguments_delta","arguments":"1}"}}`) +
		sseEvent("interaction.completed", `{"interaction":{"status":"requires_action"}}`)
	res, err := parseStreamResponse(t.Context(), strings.NewReader(stream), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Outputs) != 2 || string(res.Outputs[0].Arguments) != `{"v":1}` || res.Outputs[1].ID != "call_b" {
		t.Fatalf("unexpected outputs: %+v", res.Outputs)
	}
}

func TestStreamRejectsImageOutput(t *testing.T) {
	stream := sseEvent("step.delta", `{"index":0,"delta":{"type":"image","data":"AAAA"}}`)
	_, err := parseStreamResponse(t.Context(), strings.NewReader(stream), nil)
	if err == nil || !strings.Contains(err.Error(), "streamed image output is not supported") {
		t.Fatalf("got error %v", err)
	}
}

func TestNativeStepsRetainDeltasAfterSeededContent(t *testing.T) {
	stream := sseEvent("step.start", `{"index":0,"step":{"type":"thought","summary":[{"type":"text","text":"think"}]}}`) +
		sseEvent("step.delta", `{"index":0,"delta":{"type":"thought_summary","text":" again"}}`) +
		sseEvent("step.start", `{"index":1,"step":{"type":"model_output","content":[{"type":"text","text":"first"}]}}`) +
		sseEvent("step.delta", `{"index":1,"delta":{"type":"text","text":" second"}}`) +
		sseEvent("step.delta", `{"index":1,"delta":{"type":"text_annotation_delta","annotations":[{"type":"url_citation","start_index":6,"end_index":12,"url":"https://go.dev"}]}}`) +
		sseEvent("interaction.completed", `{"interaction":{"status":"completed"}}`)
	res, err := parseStreamResponse(t.Context(), strings.NewReader(stream), nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(res.Outputs)
	if err != nil {
		t.Fatal(err)
	}
	var out []Step
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Summary[0].Text != "think again" || out[1].Content[0].Text != "first second" || len(out[1].Content[0].Annotations) != 1 {
		t.Fatalf("native steps lost deltas: %s", raw)
	}
}
