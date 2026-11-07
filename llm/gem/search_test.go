package gem

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/gem/gemini"
)

func TestGroundedSearchRequest(t *testing.T) {
	s := &Service{Model: "gemini-3.8-flash"}
	req := &llm.Request{Tools: []*llm.Tool{
		{Name: "bash", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "web_search", Type: "google_search", ServerSide: true},
	}}
	got, err := s.buildGeminiRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tools) != 2 || len(got.Tools[0].FunctionDeclarations) != 1 ||
		got.Tools[0].FunctionDeclarations[0].Name != "bash" || got.Tools[1].GoogleSearch == nil {
		t.Fatalf("search must coexist with function tools: %+v", got.Tools)
	}
	if got.ToolConfig == nil || !got.ToolConfig.IncludeServerSideToolInvocations ||
		got.ToolConfig.FunctionCallingConfig == nil || got.ToolConfig.FunctionCallingConfig.Mode != "VALIDATED" {
		t.Fatalf("missing mixed-tool config: %+v", got.ToolConfig)
	}
	s.Model = "gemini-2.5-pro"
	if _, err := s.buildGeminiRequest(req); err == nil {
		t.Fatal("unsupported mixed search must fail")
	}
}

func TestGroundedSearchStreamingReplay(t *testing.T) {
	rawTool := `{"toolResponse":{"id":"search","toolType":"GOOGLE_SEARCH_WEB","response":{"search_suggestions":"<p>Paris</p>","opaque":"keep"}},"thoughtSignature":"signed","partMetadata":{"opaque":true}}`
	body := geminiSSE(
		`{"candidates":[{"content":{"parts":[`+rawTool+`]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"Paris "}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"hosted."}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"answer-tail"}]},"finishReason":"STOP","groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://example.org/paris","title":"Paris"}}],"groundingSupports":[{"segment":{"partIndex":1,"endIndex":13,"text":"Paris hosted."},"groundingChunkIndices":[0]}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`,
	)
	s := &Service{Model: "gemini-3.8-flash", APIKey: "key", HTTPC: &http.Client{
		Transport: &mockRoundTripper{response: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}},
	}}
	response, err := s.Do(t.Context(), &llm.Request{Messages: []llm.Message{llm.UserStringMessage("Search")}})
	if err != nil {
		t.Fatal(err)
	}
	msg := response.ToMessage()
	before, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.buildGeminiRequest(&llm.Request{Messages: []llm.Message{msg}})
	if err != nil {
		t.Fatal(err)
	}
	parts := replay.Contents[0].Parts
	if len(parts) != 4 || parts[1].Text != "Paris hosted." || parts[2].Text != "" ||
		parts[2].ThoughtSignature != "answer-tail" || !strings.Contains(parts[3].Text, "https://example.org/paris") {
		t.Fatalf("signed answer or separate references changed: %+v", parts)
	}
	rawReplay, err := json.Marshal(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal([]byte(rawTool), &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawReplay, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("server-tool replay lost opaque fields: %s", rawReplay)
	}
	after, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("request preparation mutated saved history")
	}
}

func TestGroundedSearchForeignCitationReplay(t *testing.T) {
	s := &Service{Model: "gemini-3.8-flash"}
	req := &llm.Request{Messages: []llm.Message{{
		Role:   llm.MessageRoleAssistant,
		Origin: &llm.MessageOrigin{Provider: "anthropic", Transport: "anthropic-messages", Model: "claude-sonnet-4.5"},
		Content: []llm.Content{
			{Type: llm.ContentTypeThinking, Thinking: "private", Signature: "foreign-thinking"},
			{Type: llm.ContentTypeText, Text: "Answer", Signature: "foreign-answer", Citations: json.RawMessage(`[{"type":"web_search_result_location","url":"https://example.org","title":"Source","cited_text":"Evidence"}]`)},
		},
	}}}
	got, err := s.buildGeminiRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	parts := got.Contents[0].Parts
	if len(parts) != 2 || parts[0].Text != "Answer" || parts[0].ThoughtSignature != "" ||
		!strings.Contains(parts[1].Text, "Source: Source — https://example.org\nCited text: Evidence") {
		t.Fatalf("foreign replay lost evidence or leaked signatures: %+v", parts)
	}
	if req.Messages[0].Content[1].Signature != "foreign-answer" || req.Messages[0].Content[1].Text != "Answer" {
		t.Fatal("request preparation mutated foreign history")
	}
}

func TestGroundedSearchContentPartIndex(t *testing.T) {
	for _, prefix := range []gemini.Part{
		{Text: "Let me check.", Thought: true},
		{ToolCall: &gemini.ToolCall{ToolType: "GOOGLE_SEARCH_WEB", ID: "search"}},
	} {
		res := &gemini.Response{Candidates: []gemini.Candidate{{
			Content: gemini.Content{Parts: []gemini.Part{prefix, {Text: "Paris."}}},
			GroundingMetadata: &gemini.GroundingMetadata{
				GroundingChunks: []gemini.GroundingChunk{{Web: &gemini.GroundingWeb{URI: "https://example.org/paris"}}},
				GroundingSupports: []gemini.GroundingSupport{{
					Segment:               &gemini.GroundingSegment{PartIndex: 1, EndIndex: 6},
					GroundingChunkIndices: []int{0},
				}},
			},
		}}}
		content, err := convertSearchResponse(res)
		if err != nil {
			t.Fatal(err)
		}
		if len(content) != 2 || len(content[0].Citations) != 0 || len(content[1].Citations) == 0 {
			t.Fatalf("citation must address the answer at content part 1: %+v", content)
		}
		res.Candidates[0].GroundingMetadata.GroundingSupports[0].Segment.PartIndex = 0
		if _, err := convertSearchResponse(res); err == nil {
			t.Fatal("grounding must not address thought or tool parts")
		}
	}
}

func TestGroundedSearchToolTurn(t *testing.T) {
	var res gemini.Response
	err := json.Unmarshal([]byte(`{"candidates":[{"content":{"parts":[
		{"toolResponse":{"id":"search","toolType":"GOOGLE_SEARCH_WEB","response":{"search_suggestions":"<p>Paris</p>","opaque":"keep"}},"thoughtSignature":"signed"},
		{"functionCall":{"name":"weather","args":{"city":"Paris"}}}
	]}}]}`), &res)
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := convertSearchResponse(&res)
	if err != nil {
		t.Fatal(err)
	}
	if got := blocks[0]; len(got.ToolResult) != 1 || got.ToolUseID != "search" || got.ToolResult[0].GoogleSearchSuggestions != "<p>Paris</p>" {
		t.Fatalf("search result lost suggestions: %+v", got)
	}
	var replay struct {
		ThoughtSignature string
		ToolResponse     struct{ Response map[string]string }
	}
	if err := json.Unmarshal(blocks[0].GeminiServerTool, &replay); err != nil {
		t.Fatal(err)
	}
	if replay.ThoughtSignature != "signed" || replay.ToolResponse.Response["opaque"] != "keep" {
		t.Fatal("search replay lost provider fields")
	}

	candidate := &res.Candidates[0]
	candidate.Content.Parts = append(candidate.Content.Parts, gemini.Part{Text: "First."}, gemini.Part{Text: "🏟 Paris hosted. Next.", ThoughtSignature: "answer-signature"})
	candidate.GroundingMetadata = &gemini.GroundingMetadata{
		SearchEntryPoint:  &gemini.SearchEntryPoint{RenderedContent: "<p>Paris</p>"},
		GroundingChunks:   []gemini.GroundingChunk{{Web: &gemini.GroundingWeb{URI: "https://example.org/paris"}}},
		GroundingSupports: []gemini.GroundingSupport{{Segment: &gemini.GroundingSegment{PartIndex: 3, EndIndex: len("🏟 Paris hosted."), Text: "🏟 Paris hosted."}, GroundingChunkIndices: []int{0}}},
	}
	blocks, err = convertSearchResponse(&res)
	if err != nil {
		t.Fatal(err)
	}
	answer := blocks[3]
	var cites []groundingCitation
	if err := json.Unmarshal(answer.Citations, &cites); err != nil {
		t.Fatal(err)
	}
	if len(cites) != 1 || cites[0].EndUTF16 != 16 || answer.Signature != "answer-signature" || answer.Text != candidate.Content.Parts[3].Text || answer.GoogleSearchSuggestions != "" {
		t.Fatalf("incorrect grounded answer: %+v", answer)
	}
}
