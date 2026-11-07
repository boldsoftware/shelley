package gem

import (
	"encoding/json"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/gem/gemini"
)

func TestRoundTripToolCallIDs(t *testing.T) {
	var response gemini.Response
	err := json.Unmarshal([]byte(`{
		"candidates": [{"content": {"role": "model", "parts": [
			{"functionCall": {"id": "call-first", "name": "bash", "args": {"command": "echo first"}}, "thoughtSignature": "signature-first"},
			{"functionCall": {"id": "call-second", "name": "bash", "args": {"command": "echo second"}}}
		]}}]
	}`), &response)
	if err != nil {
		t.Fatal(err)
	}

	content := convertGeminiResponseToContent(&response)
	if len(content) != 2 {
		t.Fatalf("got %d content blocks, want 2", len(content))
	}
	for i, id := range []string{"call-first", "call-second"} {
		if content[i].ID != id {
			t.Errorf("content[%d].ID = %q, want %q", i, content[i].ID, id)
		}
	}

	stored, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	var restored []llm.Content
	if err := json.Unmarshal(stored, &restored); err != nil {
		t.Fatal(err)
	}

	service := &Service{Model: DefaultModel}
	req, err := service.buildGeminiRequest(&llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleAssistant, Content: restored},
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{Type: llm.ContentTypeToolResult, ToolUseID: restored[0].ID, ToolResult: llm.TextContent("first")},
					{Type: llm.ContentTypeToolResult, ToolUseID: restored[1].ID, ToolResult: llm.TextContent("second")},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Contents) != 2 || len(req.Contents[0].Parts) != 2 || len(req.Contents[1].Parts) != 2 {
		t.Fatalf("unexpected request contents: %+v", req.Contents)
	}
	for i, id := range []string{"call-first", "call-second"} {
		call := req.Contents[0].Parts[i].FunctionCall
		if call == nil || call.ID != id || call.Name != "bash" {
			t.Errorf("call[%d] = %+v, want bash with ID %q", i, call, id)
		}
		result := req.Contents[1].Parts[i].FunctionResponse
		if result == nil || result.ID != id || result.Name != "bash" {
			t.Errorf("result[%d] = %+v, want bash with ID %q", i, result, id)
		}
	}
	if got := req.Contents[0].Parts[0].ThoughtSignature; got != "signature-first" {
		t.Errorf("first signature = %q, want signature-first", got)
	}
	if got := req.Contents[0].Parts[1].ThoughtSignature; got != "" {
		t.Errorf("second signature = %q, want empty", got)
	}

	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"call-first", "call-second"} {
		if count := strings.Count(string(body), `"id":"`+id+`"`); count != 2 {
			t.Errorf("wire ID %q occurs %d times, want once on call and once on result: %s", id, count, body)
		}
	}
}

func TestBuildGeminiRequestUnknownToolResult(t *testing.T) {
	service := &Service{Model: DefaultModel}
	_, err := service.buildGeminiRequest(&llm.Request{
		Messages: []llm.Message{{
			Role: llm.MessageRoleUser,
			Content: []llm.Content{{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: "unknown-call",
			}},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown-call") {
		t.Fatalf("got error %v, want error identifying unknown-call", err)
	}
}
