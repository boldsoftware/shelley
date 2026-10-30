package oai

import (
	"strings"
	"testing"

	"shelley.exe.dev/llm"
)

func TestParseResponsesSSEFinishesMalformedCitationContent(t *testing.T) {
	stream := strings.Join([]string{
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"before\ue200cite\ue202turn1search0"}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"other"}`,
		``,
		`event: response.output_text.done`,
		`data: {"type":"response.output_text.done","output_index":0,"content_index":0}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"status":"completed"}}`,
		``,
	}, "\n")

	var got strings.Builder
	if _, err := parseResponsesSSEStream(t.Context(), strings.NewReader(stream), func(delta llm.StreamDelta) {
		got.WriteString(delta.Text)
	}); err != nil {
		t.Fatal(err)
	}
	if want := "beforeotherciteturn1search0"; got.String() != want {
		t.Fatalf("streamed = %q, want %q", got.String(), want)
	}
}

func TestParseResponsesSSEFinishesCitationAtStreamEnd(t *testing.T) {
	// No response.output_text.done: the held payload must still be emitted
	// rather than swallowed when the stream ends.
	stream := strings.Join([]string{
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"before\ue200cite\ue202turn1search0"}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"status":"completed"}}`,
		``,
	}, "\n")

	var got strings.Builder
	if _, err := parseResponsesSSEStream(t.Context(), strings.NewReader(stream), func(delta llm.StreamDelta) {
		got.WriteString(delta.Text)
	}); err != nil {
		t.Fatal(err)
	}
	if want := "beforeciteturn1search0"; got.String() != want {
		t.Fatalf("streamed = %q, want %q", got.String(), want)
	}
}

func TestResponsesConversionPreservesRawCitationMarkers(t *testing.T) {
	raw := "answer\ue200cite\ue202turn1search0\ue201"
	resp := (&ResponsesService{}).toLLMResponseFromResponses(&responsesResponse{
		Output: []responsesOutputItem{{
			Type: "message",
			Content: []responsesContent{{
				Type: "output_text",
				Text: raw,
			}},
		}},
	}, nil)
	if got := resp.Content[0].Text; got != raw {
		t.Fatalf("stored response text = %q, want raw %q", got, raw)
	}
}
