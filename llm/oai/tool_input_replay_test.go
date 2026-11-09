package oai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sashabaranov/go-openai"
	"shelley.exe.dev/claudetool/browse"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
)

func TestMalformedToolHistoryReplay(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"quoted object", `"{\"action\":\"eval\"}"`, `{}`},
		{"wrapped truncated object", `"{\"action\":\"eval\""`, `{}`},
		{"array", `[]`, `{}`},
		{"null", `null`, `{}`},
		{"object bytes", `{ "action":42, "n":1.50 }`, `{ "action":42, "n":1.50 }`},
		{"repeated names", `{"a":1,"a":2}`, `{"a":2}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const guidance = "invalid browser call: resend a JSON object"
			const valid = `{"action":"network_help"}`
			history := []llm.Message{
				{Role: llm.MessageRoleAssistant, Content: []llm.Content{
					{Type: llm.ContentTypeToolUse, ID: "bad", ToolName: "browser", ToolInput: json.RawMessage(tc.input)},
					{Type: llm.ContentTypeToolUse, ID: "good", ToolName: "browser", ToolInput: json.RawMessage(valid)},
				}},
				{Role: llm.MessageRoleUser, Content: []llm.Content{
					{Type: llm.ContentTypeToolResult, ToolUseID: "bad", ToolError: true, ToolResult: llm.TextContent(guidance)},
					{Type: llm.ContentTypeToolResult, ToolUseID: "good", ToolResult: llm.TextContent("help")},
				}},
			}
			before, err := json.Marshal(history)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				chat := fromLLMMessage(history[0])
				if len(chat) != 1 || len(chat[0].ToolCalls) != 2 {
					t.Fatalf("chat calls = %+v", chat)
				}
				for i, want := range []string{tc.want, valid} {
					call := chat[0].ToolCalls[i]
					if call.ID != history[0].Content[i].ID || call.Function.Name != "browser" || call.Function.Arguments != want {
						t.Fatalf("chat call = %+v, want arguments %q", call, want)
					}
				}
				results := fromLLMMessage(history[1])
				if len(results) != 2 || results[0].ToolCallID != "bad" || results[0].Content != "error: "+guidance || results[1].ToolCallID != "good" {
					t.Fatalf("chat results = %+v", results)
				}
				items := fromLLMMessageResponses(history[0], responsesReasoningReplayEncrypted)
				if len(items) != 2 {
					t.Fatalf("Responses calls = %+v", items)
				}
				for i, want := range []string{tc.want, valid} {
					if items[i].Type != "function_call" || items[i].CallID != history[0].Content[i].ID || items[i].Name != "browser" || items[i].Arguments != want {
						t.Fatalf("Responses call = %+v, want arguments %q", items[i], want)
					}
				}
				outputs := fromLLMMessageResponses(history[1], responsesReasoningReplayEncrypted)
				if len(outputs) != 2 || outputs[0].CallID != "bad" || outputs[0].Output != "error: "+guidance || outputs[1].CallID != "good" {
					t.Fatalf("Responses results = %+v", outputs)
				}
				after, err := json.Marshal(history)
				if err != nil {
					t.Fatal(err)
				}
				if string(after) != string(before) || string(history[0].Content[0].ToolInput) != tc.input {
					t.Fatal("serialization changed the original history")
				}
			}
		})
	}
}

func TestMalformedBrowserCallRecoversWithinTurn(t *testing.T) {
	for _, arguments := range []string{`"{\"action\":\"eval\"}"`, `{"action":`} {
		t.Run(arguments, func(t *testing.T) {
			tools := browse.NewBrowseTools(t.Context(), 0)
			t.Cleanup(func() { tools.Close() })
			browser := tools.CombinedTool()
			var executed []string
			run := browser.Run
			browser.Run = func(ctx context.Context, input json.RawMessage) llm.ToolOut {
				executed = append(executed, string(input))
				return run(ctx, input)
			}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req openai.ChatCompletionRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				requests++
				var call *openai.ToolCall
				switch requests {
				case 1:
					call = &openai.ToolCall{ID: "bad", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "browser", Arguments: arguments}}
				case 2, 3:
					bad := req.Messages[1].ToolCalls[0]
					result := req.Messages[2]
					if bad.ID != "bad" || bad.Function.Arguments != "{}" || result.ToolCallID != "bad" || !strings.HasPrefix(result.Content, "error: invalid browser call:") {
						t.Errorf("replay lost the failed call or guidance: %+v", req.Messages)
						http.Error(w, "invalid replay", http.StatusBadRequest)
						return
					}
					if requests == 2 {
						call = &openai.ToolCall{ID: "retry", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "browser", Arguments: `{"action":"network_help"}`}}
					} else if req.Messages[4].ToolCallID != "retry" || req.Messages[4].Content == "" {
						t.Errorf("missing corrected call result: %+v", req.Messages)
					}
				default:
					t.Errorf("unexpected request %d", requests)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				msg := openai.ChatCompletionMessage{Role: "assistant", Content: "recovered"}
				finish := openai.FinishReasonStop
				if call != nil {
					msg.Content = ""
					msg.ToolCalls = []openai.ToolCall{*call}
					finish = openai.FinishReasonToolCalls
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(openai.ChatCompletionResponse{ID: fmt.Sprintf("response-%d", requests), Choices: []openai.ChatCompletionChoice{{Message: msg, FinishReason: finish}}}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()

			var recorded []llm.Message
			record := func(message llm.Message) error {
				data, err := json.Marshal(message)
				if err != nil {
					return err
				}
				var stored llm.Message
				if err := json.Unmarshal(data, &stored); err != nil {
					return err
				}
				recorded = append(recorded, stored)
				return nil
			}
			err := loop.Run(t.Context(), loop.RunConfig{
				LLM:      &Service{APIKey: "test", Model: modelForTest("test"), ModelURL: server.URL},
				Messages: []llm.Message{llm.UserStringMessage("start")}, Tools: []*llm.Tool{browser}, MaxIterations: 3,
				Hooks: loop.Hooks{
					OnResponse:     func(_ context.Context, r loop.Response) error { return record(r.Message) },
					OnToolResponse: func(_ context.Context, r loop.ToolResponse) error { return record(r.Message) },
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			original := string(toolArgumentsInput(arguments))
			if requests != 3 || len(executed) != 2 || executed[0] != original || executed[1] != `{"action":"network_help"}` {
				t.Fatalf("requests = %d, executed = %q", requests, executed)
			}
			if len(recorded) != 5 || string(recorded[0].Content[0].ToolInput) != original || !recorded[1].Content[0].ToolError || recorded[3].Content[0].ToolError || recorded[4].Content[0].Text != "recovered" {
				t.Fatalf("recorded history = %+v", recorded)
			}
		})
	}
}
