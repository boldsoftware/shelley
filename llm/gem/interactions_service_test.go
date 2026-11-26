package gem

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/gem/interactions"
)

func TestInteractionsBuildRequest(t *testing.T) {
	svc := &InteractionsService{
		Model: "gemini-3.8-flash",
	}
	req := &llm.Request{
		System: []llm.SystemContent{{Text: "Be concise."}, {Text: "Use tools."}},
		Tools: []*llm.Tool{
			{
				Name:        "read_img",
				Description: "Read an image",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
			},
			{
				Type:       "google_search",
				ServerSide: true,
			},
		},
		ThinkingLevel: llm.ThinkingLevelHigh,
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{Type: llm.ContentTypeText, Text: "Look at this"},
					{Type: llm.ContentTypeText, MediaType: "image/png", Data: "AAAA"},
				},
			},
			{
				Role: llm.MessageRoleAssistant,
				Origin: &llm.MessageOrigin{
					Provider:  "gemini",
					Transport: "gemini-interactions",
					Model:     "gemini-4-hydrogen",
				},
				Content: []llm.Content{
					{Type: llm.ContentTypeThinking, Thinking: "unsent summary", Signature: "sig_1"},
					{Type: llm.ContentTypeThinking, Thinking: "unsigned thought should be skipped"},
					{Type: llm.ContentTypeToolUse, ID: "call_1", ToolName: "read_img", ToolInput: json.RawMessage(`{"path":"a.png"}`)},
				},
			},
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{
						Type:      llm.ContentTypeToolResult,
						ToolUseID: "call_1",
						ToolError: true,
						ToolResult: []llm.Content{
							{Type: llm.ContentTypeText, Text: "partial image"},
							{Type: llm.ContentTypeText, MediaType: "image/jpeg", Data: "BBBB"},
						},
					},
				},
			},
		},
	}

	got, err := svc.buildInteractionsRequest(req)
	if err != nil {
		t.Fatalf("buildInteractionsRequest: %v", err)
	}
	if got.Model != "gemini-3.8-flash" {
		t.Errorf("Model = %q", got.Model)
	}
	if got.Store == nil || *got.Store {
		t.Errorf("Store = %v, want false", got.Store)
	}
	if !got.Stream {
		t.Errorf("Stream = false, want true")
	}
	if got.SystemInstruction != "Be concise.\nUse tools." {
		t.Errorf("SystemInstruction = %q", got.SystemInstruction)
	}
	if got.GenerationConfig == nil || got.GenerationConfig.ThinkingLevel != "high" || got.GenerationConfig.ThinkingSummaries != "auto" {
		t.Errorf("GenerationConfig = %+v", got.GenerationConfig)
	}
	if len(got.Tools) != 2 || got.Tools[0].Type != "function" || got.Tools[0].Name != "read_img" || got.Tools[1].Type != "google_search" {
		t.Errorf("Tools = %+v", got.Tools)
	}
	if len(got.Input) != 4 {
		t.Fatalf("len(Input) = %d, want 4: %+v", len(got.Input), got.Input)
	}
	if got.Input[0].Type != "user_input" || len(got.Input[0].Content) != 2 || got.Input[0].Content[1].Type != "image" {
		t.Errorf("Input[0] = %+v", got.Input[0])
	}
	if got.Input[1].Type != "thought" || got.Input[1].Signature != "sig_1" || len(got.Input[1].Summary) != 0 {
		t.Errorf("Input[1] = %+v", got.Input[1])
	}
	if got.Input[2].Type != "function_call" || got.Input[2].ID != "call_1" || got.Input[2].Name != "read_img" {
		t.Errorf("Input[2] = %+v", got.Input[2])
	}
	if got.Input[3].Type != "function_result" || got.Input[3].CallID != "call_1" || got.Input[3].Name != "read_img" || !got.Input[3].IsError {
		t.Errorf("Input[3] = %+v", got.Input[3])
	}
	var items []interactions.Content
	if err := json.Unmarshal(got.Input[3].Result, &items); err != nil {
		t.Fatalf("unmarshal function_result payload: %v", err)
	}
	if len(items) != 2 || items[0].Text != "partial image" || items[1].Type != "image" || items[1].MimeType != "image/jpeg" {
		t.Errorf("unexpected multimodal function_result payload: %+v", items)
	}
}

func TestInteractionsEmptyToolResult(t *testing.T) {
	svc := &InteractionsService{Model: "gemini-4-hydrogen"}
	req := &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}},
			{Role: llm.MessageRoleAssistant, Content: []llm.Content{{Type: llm.ContentTypeToolUse, ID: "call_1", ToolName: "noop", ToolInput: json.RawMessage(`{}`)}}},
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeToolResult, ToolUseID: "call_1", ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: ""}}}}},
		},
	}
	got, err := svc.buildInteractionsRequest(req)
	if err != nil {
		t.Fatalf("buildInteractionsRequest: %v", err)
	}
	if string(got.Input[2].Result) != "[]" {
		t.Errorf("empty function_result Result = %s, want []", got.Input[2].Result)
	}
}

func TestInteractionsCrossTransportIsolation(t *testing.T) {
	// 1. generateContent origin passed to InteractionsService: strips thinking, signature, and GenerateContent GeminiServerTool.
	interSvc := &InteractionsService{Model: "gemini-4-hydrogen"}
	gcServerTool := json.RawMessage(`{"toolCall":{"toolType":"GOOGLE_SEARCH_WEB","id":"s1","args":{"queries":["q"]}},"thoughtSignature":"gc_sig"}`)
	interReq, err := interSvc.buildInteractionsRequest(&llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}},
			{
				Role:   llm.MessageRoleAssistant,
				Origin: &llm.MessageOrigin{Provider: "gemini", Transport: "gemini-generate-content", Model: "gemini-3.8-flash"},
				Content: []llm.Content{
					{Type: llm.ContentTypeThinking, Thinking: "old thought", Signature: "gc_sig"},
					{Type: llm.ContentTypeServerToolUse, ID: "s1", ToolName: "web_search", GeminiServerTool: gcServerTool},
					{Type: llm.ContentTypeText, Text: "answer from gc", Signature: "gc_sig"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("InteractionsService buildInteractionsRequest failed on generateContent history: %v", err)
	}
	if len(interReq.Input) != 2 || interReq.Input[1].Type != "model_output" || len(interReq.Input[1].Content) != 1 || interReq.Input[1].Content[0].Text != "answer from gc" {
		t.Errorf("expected only answer text from generateContent turn, got: %+v", interReq.Input)
	}

	// 2. gemini-interactions origin passed to generateContent Service: strips thinking, signature, and Interactions GeminiServerTool.
	gcSvc := &Service{Model: "gemini-3.8-flash"}
	interServerTool := json.RawMessage(`{"type":"google_search_call","id":"call_1","signature":"int_sig","search_type":"web_search","arguments":{"queries":["q"]}}`)
	gcReq, err := gcSvc.buildGeminiRequest(&llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}},
			{
				Role:   llm.MessageRoleAssistant,
				Origin: &llm.MessageOrigin{Provider: "gemini", Transport: "gemini-interactions", Model: "gemini-4-hydrogen"},
				Content: []llm.Content{
					{Type: llm.ContentTypeThinking, Thinking: "int thought", Signature: "int_sig"},
					{Type: llm.ContentTypeServerToolUse, ID: "call_1", ToolName: "web_search", GeminiServerTool: interServerTool},
					{Type: llm.ContentTypeText, Text: "answer from interactions", Signature: "int_sig"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Service buildGeminiRequest failed on gemini-interactions history: %v", err)
	}
	if len(gcReq.Contents) != 2 || len(gcReq.Contents[1].Parts) != 1 || gcReq.Contents[1].Parts[0].Text != "answer from interactions" || gcReq.Contents[1].Parts[0].ThoughtSignature != "" {
		t.Errorf("expected stripped text part on generateContent, got: %+v", gcReq.Contents)
	}

	// 3. Same-transport gemini-interactions origin preserves thought signature & google_search_call step.
	sameReq, err := interSvc.buildInteractionsRequest(&llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}},
			{
				Role:   llm.MessageRoleAssistant,
				Origin: &llm.MessageOrigin{Provider: "gemini", Transport: "gemini-interactions", Model: "gemini-4-hydrogen"},
				Content: []llm.Content{
					{Type: llm.ContentTypeThinking, Thinking: "int thought", Signature: "int_sig"},
					{Type: llm.ContentTypeServerToolUse, ID: "call_1", ToolName: "web_search", GeminiServerTool: interServerTool},
					{Type: llm.ContentTypeText, Text: "answer"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("same-transport buildInteractionsRequest: %v", err)
	}
	if len(sameReq.Input) != 4 || sameReq.Input[1].Type != "thought" || sameReq.Input[2].Type != "google_search_call" || sameReq.Input[3].Type != "model_output" {
		t.Errorf("expected preserved thought + google_search_call + model_output, got: %+v", sameReq.Input)
	}
}

func TestInteractionsResponseConversionAndUTF16Citations(t *testing.T) {
	// "😀 ok" -> emoji is 4 UTF-8 bytes and 2 UTF-16 code units; total UTF-8 len = 7, UTF-16 len = 5.
	text := "😀 ok"
	res := &interactions.Response{
		ID:     "int_9",
		Status: "completed",
		Usage: &interactions.Usage{
			TotalInputTokens:   100,
			TotalCachedTokens:  20,
			TotalToolUseTokens: 5,
			TotalOutputTokens:  15,
			TotalThoughtTokens: 30,
		},
		Outputs: []interactions.Step{
			{Type: "thought", Signature: "sig_t", Summary: []interactions.Content{{Type: "text", Text: "plan"}}},
			{Type: "google_search_call", ID: "gs_1", Signature: "sig_gc", SearchType: "web_search", Arguments: json.RawMessage(`{"queries":["emoji"]}`)},
			{Type: "google_search_result", CallID: "gs_1", Signature: "sig_gr", Result: json.RawMessage(`[{"search_suggestions":"<div>chips</div>"}]`)},
			{
				Type: "model_output",
				Content: []interactions.Content{{
					Type: "text",
					Text: text,
					Annotations: []interactions.Annotation{{
						Type:       "url_citation",
						StartIndex: 0,
						EndIndex:   len(text),
						URL:        "https://example.com",
						Title:      "Example",
					}},
				}},
			},
		},
	}

	got, err := convertInteractionsResponse(res)
	if err != nil {
		t.Fatalf("convertInteractionsResponse: %v", err)
	}
	if got.StopReason != llm.StopReasonEndTurn {
		t.Errorf("StopReason = %v, want EndTurn", got.StopReason)
	}
	if got.Usage.InputTokens != 85 || got.Usage.CacheReadInputTokens != 20 || got.Usage.OutputTokens != 45 {
		t.Errorf("Usage = %+v, want input=85 cache=20 output=45", got.Usage)
	}
	if len(got.Content) != 4 {
		t.Fatalf("len(Content) = %d, want 4", len(got.Content))
	}
	if got.Content[0].Type != llm.ContentTypeThinking || got.Content[0].Thinking != "plan" || got.Content[0].Signature != "sig_t" {
		t.Errorf("Content[0] = %+v", got.Content[0])
	}
	if got.Content[1].Type != llm.ContentTypeServerToolUse || got.Content[1].ToolName != "web_search" || len(got.Content[1].GeminiServerTool) == 0 {
		t.Errorf("Content[1] = %+v", got.Content[1])
	}
	if got.Content[2].Type != llm.ContentTypeWebSearchToolResult || len(got.Content[2].ToolResult) != 1 || got.Content[2].ToolResult[0].GoogleSearchSuggestions != "<div>chips</div>" {
		t.Errorf("Content[2] = %+v", got.Content[2])
	}
	var cites []groundingCitation
	if err := json.Unmarshal(got.Content[3].Citations, &cites); err != nil {
		t.Fatalf("unmarshal citations: %v", err)
	}
	if len(cites) != 1 || cites[0].EndUTF16 != 5 || cites[0].URL != "https://example.com" || cites[0].CitedText != text {
		t.Errorf("unexpected citations: %+v", cites)
	}
}

func TestInteractionsStatusMapping(t *testing.T) {
	baseUsage := &interactions.Usage{TotalInputTokens: 1, TotalOutputTokens: 1}
	t.Run("requires_action", func(t *testing.T) {
		got, err := convertInteractionsResponse(&interactions.Response{
			Status:  "requires_action",
			Usage:   baseUsage,
			Outputs: []interactions.Step{{Type: "function_call", ID: "c1", Name: "fn", Arguments: json.RawMessage(`{}`)}},
		})
		if err != nil || got.StopReason != llm.StopReasonToolUse {
			t.Fatalf("got=(%v, %v), want StopReasonToolUse", got, err)
		}
	})
	t.Run("incomplete", func(t *testing.T) {
		got, err := convertInteractionsResponse(&interactions.Response{
			Status:  "incomplete",
			Usage:   baseUsage,
			Outputs: []interactions.Step{{Type: "model_output", Content: []interactions.Content{{Type: "text", Text: "cut off"}}}},
		})
		if err != nil || got.StopReason != llm.StopReasonMaxTokens {
			t.Fatalf("got=(%v, %v), want StopReasonMaxTokens", got, err)
		}
	})
	t.Run("failed", func(t *testing.T) {
		_, err := convertInteractionsResponse(&interactions.Response{Status: "failed", Usage: baseUsage})
		if err == nil || !strings.Contains(err.Error(), "failed") {
			t.Fatalf("expected failed error, got %v", err)
		}
	})
}

func TestInteractionsDoEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/interactions" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Api-Revision") != interactions.APIRevision {
			http.Error(w, "missing revision", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w,
			"event: step.start\ndata: {\"event_type\":\"step.start\",\"index\":0,\"step\":{\"type\":\"model_output\"}}\n\n"+
				"event: step.delta\ndata: {\"event_type\":\"step.delta\",\"index\":0,\"delta\":{\"type\":\"text\",\"text\":\"pong\"}}\n\n"+
				"event: step.stop\ndata: {\"event_type\":\"step.stop\",\"index\":0}\n\n"+
				"event: interaction.completed\ndata: {\"event_type\":\"interaction.completed\",\"interaction\":{\"id\":\"int_e2e\",\"status\":\"completed\",\"usage\":{\"total_input_tokens\":4,\"total_output_tokens\":1}}}\n\n")
	}))
	defer srv.Close()

	svc := &InteractionsService{
		URL:    srv.URL + "/v1beta",
		APIKey: "k",
		Model:  "gemini-4-hydrogen",
		HTTPC:  srv.Client(),
	}
	var streamed string
	resp, err := svc.Do(context.Background(), &llm.Request{
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "ping"}}}},
		OnStream: func(d llm.StreamDelta) {
			if d.Type == "text" {
				streamed += d.Text
			}
		},
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if streamed != "pong" {
		t.Errorf("streamed = %q, want pong", streamed)
	}
	if resp.Origin == nil || resp.Origin.Provider != "gemini" || resp.Origin.Transport != "gemini-interactions" || resp.Origin.Model != "gemini-4-hydrogen" {
		t.Errorf("Origin = %+v", resp.Origin)
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != "pong" {
		t.Errorf("Content = %+v", resp.Content)
	}
}

func TestInteractionsReplayNativeStepsFromStoredMessage(t *testing.T) {
	const native = `[{"type":"thought","summary":[{"type":"text","text":"first"}]},{"type":"google_search_call","id":"search_1","future_field":"keep"},{"type":"google_search_result","call_id":"search_1","is_error":false},{"type":"thought","signature":"signed"},{"type":"model_output","content":[{"type":"text","text":"answer","annotations":[{"type":"url_citation","start_index":0,"end_index":6,"url":"https://go.dev"}]}]}]`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: "+`{"event_type":"interaction.completed","interaction":{"status":"completed","usage":{"total_input_tokens":5},"outputs":`+native+`}}`+"\n\n")
	}))
	defer upstream.Close()
	svc := &InteractionsService{URL: upstream.URL}
	first := llm.UserStringMessage("first")
	res, err := svc.Do(t.Context(), &llm.Request{Messages: []llm.Message{first}})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := json.Marshal(res.ToMessage())
	if err != nil {
		t.Fatal(err)
	}
	var msg llm.Message
	if err := json.Unmarshal(stored, &msg); err != nil {
		t.Fatal(err)
	}
	msg.Content = llm.TextContent("different display text")
	messages := []llm.Message{first, msg, llm.UserStringMessage("follow up")}
	req, err := svc.buildInteractionsRequest(&llm.Request{Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Input) != 7 {
		t.Fatalf("got %d input steps; want 7", len(req.Input))
	}
	got, err := json.Marshal(req.Input[1:6])
	if err != nil || string(got) != native {
		t.Fatalf("native steps changed: %s (%v)", got, err)
	}
	messages[1].Origin = nil
	messages[1].Content = append(messages[1].Content, llm.Content{Type: llm.ContentTypeThinking, Signature: "foreign"})
	req, err = svc.buildInteractionsRequest(&llm.Request{Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Input) != 3 || req.Input[1].Type != "model_output" || req.Input[1].Content[0].Text != "different display text" {
		t.Fatalf("originless message replayed native data: %+v", req.Input)
	}
}

func TestInteractionsNativeReplayDoesNotResurrectPrunedCalls(t *testing.T) {
	msg := llm.Message{
		Role: llm.MessageRoleAssistant, Origin: &llm.MessageOrigin{Provider: "gemini", Transport: "gemini-interactions", Model: "gemini-3.8-flash"},
		Content:                 []llm.Content{{Type: llm.ContentTypeToolUse, ID: "keep", ToolName: "read"}},
		GeminiInteractionsSteps: json.RawMessage(`[{"type":"thought","signature":"signed"},{"type":"function_call","id":"pruned","name":"read","arguments":{}},{"type":"function_call","id":"keep","name":"read","arguments":{},"provider_field":"kept"}]`),
	}
	result := llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeToolResult, ToolUseID: "keep", ToolResult: llm.TextContent("result")}}}
	req, err := (&InteractionsService{}).buildInteractionsRequest(&llm.Request{Messages: []llm.Message{llm.UserStringMessage("read"), msg, result}})
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Input) != 4 || req.Input[1].Type != "thought" || req.Input[2].ID != "keep" || req.Input[3].CallID != "keep" {
		t.Fatalf("wrong replay: %+v", req.Input)
	}
}

func TestInteractionsNativeReplayRepairsToolArguments(t *testing.T) {
	for _, tc := range []struct{ name, original, effective, want string }{
		{"duplicate", `{"x":1,"x":2}`, `{"x":1,"x":2}`, `{"x":2}`},
		{"array", `[1]`, `[1]`, `{}`},
		{"repaired", `{"x":"2"}`, `{"x":2}`, `{"x":2}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := llm.Message{
				Role:                    llm.MessageRoleAssistant,
				Origin:                  &llm.MessageOrigin{Provider: "gemini", Transport: "gemini-interactions", Model: "gemini-3.8-flash"},
				Content:                 []llm.Content{{Type: llm.ContentTypeToolUse, ID: "call", ToolName: "read", ToolInput: json.RawMessage(tc.effective)}},
				GeminiInteractionsSteps: json.RawMessage(`[{"type":"function_call","id":"call","name":"read","arguments":` + tc.original + `,"provider_field":"kept"}]`),
			}
			req, err := (&InteractionsService{}).buildInteractionsRequest(&llm.Request{Messages: []llm.Message{llm.UserStringMessage("read"), msg}})
			if err != nil {
				t.Fatal(err)
			}
			if len(req.Input) != 2 {
				t.Fatalf("want one replay step, got %v", req.Input)
			}
			raw, err := json.Marshal(req.Input[1])
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			if string(fields["arguments"]) != tc.want || string(fields["provider_field"]) != `"kept"` || string(fields["id"]) != `"call"` {
				t.Fatalf("unexpected replay step: %s", raw)
			}
		})
	}
}
