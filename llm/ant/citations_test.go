package ant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
)

const (
	openAICitation       = `{"type":"url_citation","start_index":0,"end_index":6,"url":"https://example.com/source","title":"Source title"}`
	anthropicWebCitation = `{"type":"web_search_result_location","url":"https://example.com/web","title":"Web title","cited_text":"Original cited passage","encrypted_index":"opaque-signed-index","extra":{"keep":true}}`
)

func citationRequest(raw string) *llm.Request {
	return &llm.Request{Messages: []llm.Message{{Role: llm.MessageRoleAssistant, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Answer", Citations: json.RawMessage(raw)}}}}}
}

func TestAdaptCitation(t *testing.T) {
	native := " [ \n" + anthropicWebCitation + `, {"type":"char_location","opaque":true}, {"type":"page_location"}, {"type":"content_block_location"}, {"type":"search_result_location"} ] `
	for _, tt := range []struct{ name, raw, wantRaw, wantText string }{
		{"original OpenAI bug", "[" + openAICitation + "]", "", "Answer\n\nSource: Source title — https://example.com/source"},
		{"Gemini grounding", `[{"type":"google_grounding","url":"https://example.com","title":"Source","cited_text":"Evidence","end_utf16":6}]`, "", "Answer\n\nSource: Source — https://example.com\nCited text: Evidence"},
		{"native byte preservation and provider validation boundary", native, native, "Answer"},
		{"mixed retains native", "[" + anthropicWebCitation + "," + openAICitation + "]", "[" + anthropicWebCitation + "]", "Answer\n\nSource: Source title — https://example.com/source"},
		{"omitted local zero ranges and title", `[{"type":"url_citation","url":"https://example.com"}]`, "", "Answer\n\nSource: https://example.com"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := citationRequest(tt.raw)
			got, err := llm.PrepareRequestCitations(context.Background(), req, "ant", adaptCitation)
			if err != nil {
				t.Fatal(err)
			}
			c := got.Messages[0].Content[0]
			if c.Text != tt.wantText || string(c.Citations) != tt.wantRaw {
				t.Fatalf("got text %q citations %s; want text %q citations %s", c.Text, c.Citations, tt.wantText, tt.wantRaw)
			}
		})
	}
}

func TestAdaptCitationErrors(t *testing.T) {
	for _, tt := range []struct{ name, raw, reason string }{
		{"unknown", `[{"type":"future_citation"}]`, "unsupported citation type"},
		{"missing URL", `[{"type":"url_citation"}]`, "url must"},
		{"empty URL", `[{"type":"url_citation","url":" "}]`, "url must"},
		{"URL shape", `[{"type":"url_citation","url":5}]`, "url must"},
		{"title shape", `[{"type":"url_citation","url":"x","title":[]}]`, "title must"},
		{"quote shape", `[{"type":"url_citation","url":"x","cited_text":false}]`, "cited_text must"},
		{"range shape", `[{"type":"url_citation","url":"x","start_index":"0"}]`, "start_index must"},
		{"null range", `[{"type":"url_citation","url":"x","end_index":null}]`, "end_index must"},
		{"null title", `[{"type":"url_citation","url":"x","title":null}]`, "title must"},
		{"null quote", `[{"type":"url_citation","url":"x","cited_text":null}]`, "cited_text must"},
		{"negative range", `[{"type":"url_citation","url":"x","start_index":-1}]`, "start_index must"},
		{"fractional range", `[{"type":"url_citation","url":"x","end_index":1.5}]`, "end_index must"},
		{"encrypted index shape", `[{"type":"url_citation","url":"x","encrypted_index":null}]`, "encrypted_index must"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := llm.PrepareRequestCitations(context.Background(), citationRequest(tt.raw), "ant", adaptCitation)
			if err == nil || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("error = %v, want %q", err, tt.reason)
			}
		})
	}
}
