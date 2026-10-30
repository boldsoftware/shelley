package ant

import (
	"net/http"
	"testing"

	"shelley.exe.dev/llm"
)

// Every streamed event except ping is model progress.
func TestDoReportsProgressPerEvent(t *testing.T) {
	stream := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n" +
		"event: ping\ndata: {\"type\":\"ping\"}\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: ping\ndata: {\"type\":\"ping\"}\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	s := &Service{
		APIKey: "test-key",
		HTTPC:  &http.Client{Transport: &mockHTTPTransport{responseBody: stream, statusCode: 200}},
	}
	progress := 0
	ctx := llm.WithProgress(t.Context(), func() { progress++ })
	_, err := s.Do(ctx, &llm.Request{
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if progress != 6 {
		t.Errorf("progress reports = %d, want 6 (one per non-ping event)", progress)
	}
}
