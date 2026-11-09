package ant

import (
	"encoding/json"
	"testing"

	"shelley.exe.dev/llm"
)

func TestMalformedToolHistoryReplay(t *testing.T) {
	const original = `"{\"action\":\"eval\"}"`
	const guidance = "invalid browser call: resend a JSON object"
	history := []llm.Message{
		{Role: llm.MessageRoleAssistant, Content: []llm.Content{{
			Type: llm.ContentTypeToolUse, ID: "bad", ToolName: "browser", ToolInput: json.RawMessage(original),
		}}},
		{Role: llm.MessageRoleUser, Content: []llm.Content{{
			Type: llm.ContentTypeToolResult, ToolUseID: "bad", ToolError: true, ToolResult: llm.TextContent(guidance),
		}}},
	}
	before, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		call := fromLLMMessage(history[0]).Content[0]
		if call.Type != "tool_use" || call.ID != "bad" || call.ToolName != "browser" || string(call.ToolInput) != "{}" {
			t.Fatalf("replayed call = %+v", call)
		}
		result := fromLLMMessage(history[1]).Content[0]
		if result.Type != "tool_result" || result.ToolUseID != "bad" || !result.ToolError || *result.ToolResult[0].Text != guidance {
			t.Fatalf("replayed result = %+v", result)
		}
		after, err := json.Marshal(history)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) || string(history[0].Content[0].ToolInput) != original {
			t.Fatal("serialization changed the original history")
		}
	}
}

func TestFromLLMContentTruncatedToolInput(t *testing.T) {
	const original = `{"action":`
	source := llm.Content{
		Type: llm.ContentTypeToolUse, ID: "bad", ToolName: "browser", ToolInput: json.RawMessage(original),
	}
	call := fromLLMContent(source)
	if call.ID != "bad" || call.ToolName != "browser" || string(call.ToolInput) != "{}" {
		t.Fatalf("replayed call = %+v", call)
	}
	if _, err := json.Marshal(call); err != nil {
		t.Fatalf("marshal replayed call: %v", err)
	}
	if string(source.ToolInput) != original {
		t.Fatal("serialization changed the original input")
	}
}
