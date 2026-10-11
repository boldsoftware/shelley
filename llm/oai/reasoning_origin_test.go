package oai

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sashabaranov/go-openai"
	"shelley.exe.dev/llm"
)

func reasoningBlockCount(msg llm.Message) int {
	count := 0
	for _, content := range msg.Content {
		if content.Type == llm.ContentTypeThinking || content.Type == llm.ContentTypeRedactedThinking {
			count++
		}
	}
	return count
}

func TestFilterReasoningForOrigin(t *testing.T) {
	current := llm.MessageOrigin{
		Provider:  "openai",
		Transport: "openai-responses:https://api.openai.com/v1",
		Model:     "gpt-5.6-sol",
	}
	for _, tt := range []struct {
		name   string
		origin *llm.MessageOrigin
		want   int
	}{
		{name: "same origin", origin: &current, want: 2},
		{name: "cross provider", origin: &llm.MessageOrigin{Provider: "fireworks", Transport: current.Transport, Model: current.Model}},
		{name: "cross transport", origin: &llm.MessageOrigin{Provider: current.Provider, Transport: "openai-chat:https://api.openai.com/v1", Model: current.Model}},
		{name: "cross model", origin: &llm.MessageOrigin{Provider: current.Provider, Transport: current.Transport, Model: "gpt-5.6-terra"}},
		{name: "legacy unknown", want: 2},
		{name: "incomplete legacy", origin: &llm.MessageOrigin{Provider: "openai"}, want: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			original := llm.Message{
				Role:   llm.MessageRoleAssistant,
				Origin: tt.origin,
				Content: []llm.Content{
					{Type: llm.ContentTypeThinking, Thinking: "private reasoning"},
					{Type: llm.ContentTypeRedactedThinking, Data: "opaque reasoning"},
					{Type: llm.ContentTypeToolUse, ID: "call_1", ToolName: "x", ToolInput: json.RawMessage(`{}`)},
				},
			}
			before, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			filtered := filterReasoningForOrigin(original, current)
			if got := reasoningBlockCount(filtered); got != tt.want {
				t.Fatalf("reasoning blocks = %d, want %d", got, tt.want)
			}
			if len(filtered.Content) == 0 || filtered.Content[len(filtered.Content)-1].Type != llm.ContentTypeToolUse {
				t.Fatal("portable tool call was removed")
			}
			after, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("origin filtering mutated stored history")
			}
		})
	}
}

func TestChatServiceEnforcesReasoningOrigin(t *testing.T) {
	var request struct {
		Messages []struct {
			ReasoningContent string            `json:"reasoning_content"`
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
	// The empty reasoning_content placeholder is DeepSeek-only; cross-origin
	// reasoning is simply dropped for everyone else.
	if len(request.Messages) != 1 || request.Messages[0].ReasoningContent != "" || len(request.Messages[0].ToolCalls) != 1 {
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
