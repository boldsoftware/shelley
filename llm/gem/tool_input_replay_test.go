package gem

import (
	"encoding/json"
	"testing"

	"shelley.exe.dev/llm"
)

func TestMalformedToolHistoryReplay(t *testing.T) {
	const original = `"{\"action\":\"eval\"}"`
	const guidance = "invalid browser call: resend a JSON object"
	req := &llm.Request{Messages: []llm.Message{
		{Role: llm.MessageRoleAssistant, Content: []llm.Content{{
			Type: llm.ContentTypeToolUse, ID: "bad", ToolName: "browser", ToolInput: json.RawMessage(original), Signature: "keep-signature",
		}}},
		{Role: llm.MessageRoleUser, Content: []llm.Content{{
			Type: llm.ContentTypeToolResult, ToolUseID: "bad", ToolName: "browser", ToolError: true, ToolResult: llm.TextContent(guidance),
		}}},
	}}
	before, err := json.Marshal(req.Messages)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		replayed, err := (&Service{Model: DefaultModel}).buildGeminiRequest(req)
		if err != nil {
			t.Fatal(err)
		}
		call := replayed.Contents[0].Parts[0]
		if call.FunctionCall == nil || call.FunctionCall.Name != "browser" || call.FunctionCall.Args == nil || len(call.FunctionCall.Args) != 0 || call.ThoughtSignature != "keep-signature" {
			t.Fatalf("replayed call = %+v", call)
		}
		result := replayed.Contents[1].Parts[0].FunctionResponse
		if result == nil || result.Name != "browser" || result.Response["error"] != true || result.Response["result"] != guidance {
			t.Fatalf("replayed result = %+v", result)
		}
		after, err := json.Marshal(req.Messages)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) || string(req.Messages[0].Content[0].ToolInput) != original {
			t.Fatal("serialization changed the original history")
		}
	}
}
