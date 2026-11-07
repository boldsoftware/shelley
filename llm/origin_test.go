package llm

import (
	"bytes"
	"encoding/json"
	"testing"
)

func reasoningBlockCount(msg Message) int {
	count := 0
	for _, content := range msg.Content {
		if content.Type == ContentTypeThinking || content.Type == ContentTypeRedactedThinking {
			count++
		}
	}
	return count
}

func TestStripForeignThinking(t *testing.T) {
	current := MessageOrigin{
		Provider:  "openai",
		Transport: "openai-responses:https://api.openai.com/v1",
		Model:     "gpt-5.6-sol",
	}
	for _, tt := range []struct {
		name   string
		origin *MessageOrigin
		want   int
	}{
		{name: "same origin", origin: &current, want: 2},
		{name: "cross provider", origin: &MessageOrigin{Provider: "fireworks", Transport: current.Transport, Model: current.Model}},
		{name: "cross transport", origin: &MessageOrigin{Provider: current.Provider, Transport: "openai-chat:https://api.openai.com/v1", Model: current.Model}},
		{name: "cross model", origin: &MessageOrigin{Provider: current.Provider, Transport: current.Transport, Model: "gpt-5.6-terra"}},
		{name: "legacy unknown", want: 2},
		{name: "incomplete legacy", origin: &MessageOrigin{Provider: "openai"}, want: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			original := Message{
				Role:   MessageRoleAssistant,
				Origin: tt.origin,
				Content: []Content{
					{Type: ContentTypeThinking, Thinking: "private reasoning"},
					{Type: ContentTypeRedactedThinking, Data: "opaque reasoning"},
					{Type: ContentTypeToolUse, ID: "call_1", ToolName: "x", ToolInput: json.RawMessage(`{}`)},
				},
			}
			before, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			filtered := StripForeignThinking(original, current)
			if got := reasoningBlockCount(filtered); got != tt.want {
				t.Fatalf("reasoning blocks = %d, want %d", got, tt.want)
			}
			if len(filtered.Content) == 0 || filtered.Content[len(filtered.Content)-1].Type != ContentTypeToolUse {
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
