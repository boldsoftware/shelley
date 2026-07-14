package server

import (
	"encoding/json"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

func TestLlmDataForAPIStripsOpenAIResponsesReasoningMetadata(t *testing.T) {
	stored, err := json.Marshal(llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{{
			Type: llm.ContentTypeThinking,
			Text: "Visible reasoning summary.",
			OpenAIResponsesReasoning: &llm.OpenAIResponsesReasoningMetadata{
				ID:               "rs_private",
				EncryptedContent: "encrypted-private-state",
				Summary: []llm.OpenAIResponsesReasoningSummary{{
					Type: "summary_text",
					Text: "Visible reasoning summary.",
				}},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := string(stored)

	apiData, _ := llmDataForAPI(&raw, string(db.MessageTypeAgent), "msg_1")
	if apiData == nil {
		t.Fatal("llmDataForAPI returned nil")
	}
	var got llm.Message
	if err := json.Unmarshal([]byte(*apiData), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Content) != 1 || got.Content[0].Text != "Visible reasoning summary." {
		t.Fatalf("visible content changed: %+v", got.Content)
	}
	if got.Content[0].OpenAIResponsesReasoning != nil {
		t.Fatalf("provider metadata leaked to API: %+v", got.Content[0].OpenAIResponsesReasoning)
	}
}
