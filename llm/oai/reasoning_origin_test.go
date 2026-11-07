package oai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sashabaranov/go-openai"
	"shelley.exe.dev/llm"
)

func TestChatServiceEnforcesReasoningOrigin(t *testing.T) {
	var request struct {
		Messages []struct {
			ReasoningContent *string           `json:"reasoning_content"`
			ToolCalls        []openai.ToolCall `json:"tool_calls"`
		} `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(openai.ChatCompletionResponse{
			ID: "response", Model: "glm-5p2",
			Choices: []openai.ChatCompletionChoice{{
				Message: openai.ChatCompletionMessage{Role: "assistant", Content: "ok"},
			}},
		})
	}))
	defer server.Close()

	service := &Service{
		APIKey: "key", Model: modelForTest("glm-5p2"), ModelURL: server.URL,
		ProviderName: "fireworks", ReasoningReplay: "reasoning_content",
	}
	response, err := service.Do(t.Context(), &llm.Request{Messages: []llm.Message{{
		Role: llm.MessageRoleAssistant,
		Origin: &llm.MessageOrigin{
			Provider: "fireworks", Transport: "openai-chat:https://other.example/v1", Model: "glm-5p2",
		},
		Content: []llm.Content{
			{Type: llm.ContentTypeThinking, Thinking: "do not replay"},
			{Type: llm.ContentTypeToolUse, ID: "call_1", ToolName: "x", ToolInput: json.RawMessage(`{}`)},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) != 1 || request.Messages[0].ReasoningContent != nil || len(request.Messages[0].ToolCalls) != 1 {
		t.Fatalf("chat messages = %+v", request.Messages)
	}
	if response.Origin == nil || !response.Origin.Matches(service.messageOrigin(service.Model)) {
		t.Fatalf("response origin = %#v", response.Origin)
	}
}

func TestResponsesServiceEnforcesReasoningOrigin(t *testing.T) {
	var request responsesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(responsesResponse{
			ID: "response", Status: "completed", Model: "gpt-5.6-sol",
			Output: []responsesOutputItem{{
				Type: "message", Role: "assistant",
				Content: []responsesContent{{Type: "output_text", Text: "ok"}},
			}},
		})
	}))
	defer server.Close()

	service := &ResponsesService{
		APIKey: "key", Model: modelForTest("gpt-5.6-sol"), ModelURL: server.URL,
		ProviderName: "openai",
	}
	response, err := service.Do(t.Context(), &llm.Request{Messages: []llm.Message{{
		Role: llm.MessageRoleAssistant,
		Origin: &llm.MessageOrigin{
			Provider: "openai", Transport: "openai-responses:https://other.example/v1", Model: "gpt-5.6-sol",
		},
		Content: []llm.Content{
			{
				Type: llm.ContentTypeThinking,
				OpenAIResponsesReasoning: &llm.OpenAIResponsesReasoningMetadata{
					ID: "rs_1", EncryptedContent: "do-not-replay",
				},
			},
			{Type: llm.ContentTypeToolUse, ID: "call_1", ToolName: "x", ToolInput: json.RawMessage(`{}`)},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	var reasoning, toolCalls int
	for _, item := range request.Input {
		switch item.Type {
		case "reasoning":
			reasoning++
		case "function_call":
			toolCalls++
		}
	}
	if reasoning != 0 || toolCalls != 1 {
		t.Fatalf("responses input = %+v", request.Input)
	}
	if response.Origin == nil || !response.Origin.Matches(service.messageOrigin(service.Model)) {
		t.Fatalf("response origin = %#v", response.Origin)
	}
}
