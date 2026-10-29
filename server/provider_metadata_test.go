package server

import (
	"encoding/json"
	"testing"

	"shelley.exe.dev/claudetool"
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

func TestLlmDataForAPIStripsInlineCitationMarkersFromAgentText(t *testing.T) {
	t.Parallel()

	msg := llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{{
			Type: llm.ContentTypeText,
			Text: "answer\ue200cite\ue202turn1search0\ue201 next",
		}},
	}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(b)
	apiData, _ := llmDataForAPI(&raw, string(db.MessageTypeAgent), "msg_1")
	if apiData == nil {
		t.Fatal("llmDataForAPI returned nil")
	}
	var got llm.Message
	if err := json.Unmarshal([]byte(*apiData), &got); err != nil {
		t.Fatal(err)
	}
	if gotText := got.Content[0].Text; gotText != "answer next" {
		t.Fatalf("API text = %q, want answer next", gotText)
	}

	userData, _ := llmDataForAPI(&raw, string(db.MessageTypeUser), "msg_1")
	if userData != &raw {
		t.Fatal("user-authored text should not be normalized")
	}
}

func TestLlmDataForAPIStripsInlineCitationMarkersFromMessageUserText(t *testing.T) {
	t.Parallel()

	msg := llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{
			{
				Type:      llm.ContentTypeToolUse,
				ID:        "call_1",
				ToolName:  claudetool.MessageUserName,
				ToolInput: json.RawMessage(`{"text":"spoofed.\ue200cite\ue202turn1search2\ue201\n\nAnything else?","reply_to":7}`),
			},
			{
				Type:      llm.ContentTypeToolUse,
				ID:        "call_2",
				ToolName:  "bash",
				ToolInput: json.RawMessage(`{"command":"echo \ue200cite\ue201"}`),
			},
		},
	}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(b)
	apiData, _ := llmDataForAPI(&raw, string(db.MessageTypeAgent), "msg_1")
	if apiData == nil {
		t.Fatal("llmDataForAPI returned nil")
	}
	var got llm.Message
	if err := json.Unmarshal([]byte(*apiData), &got); err != nil {
		t.Fatal(err)
	}
	var in struct {
		Text    string `json:"text"`
		ReplyTo int    `json:"reply_to"`
	}
	if err := json.Unmarshal(got.Content[0].ToolInput, &in); err != nil {
		t.Fatal(err)
	}
	if in.Text != "spoofed.\n\nAnything else?" || in.ReplyTo != 7 {
		t.Fatalf("message_user input = %+v, want markers stripped and reply_to kept", in)
	}
	if string(got.Content[1].ToolInput) != string(msg.Content[1].ToolInput) {
		t.Fatalf("other tools' input changed: %s", got.Content[1].ToolInput)
	}
}
