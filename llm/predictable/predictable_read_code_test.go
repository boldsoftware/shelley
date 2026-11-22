package predictable

import (
	"encoding/json"
	"testing"

	"shelley.exe.dev/llm"
)

func TestPredictableReadCode(t *testing.T) {
	for _, trigger := range []string{"demo read_code", "tool smorgasbord"} {
		t.Run(trigger, func(t *testing.T) {
			resp, err := NewService().Do(t.Context(), &llm.Request{Messages: []llm.Message{
				{Role: llm.MessageRoleUser, Content: llm.TextContent(trigger)},
			}})
			if err != nil {
				t.Fatal(err)
			}
			for _, content := range resp.Content {
				if content.Type == llm.ContentTypeToolUse && content.ToolName == "read_code" {
					var input struct {
						Searches []json.RawMessage `json:"searches"`
						Reads    []json.RawMessage `json:"reads"`
					}
					if err := json.Unmarshal(content.ToolInput, &input); err != nil {
						t.Fatal(err)
					}
					if len(input.Searches) != 1 || len(input.Reads) != 1 {
						t.Fatalf("expected batched search and read: %s", content.ToolInput)
					}
					return
				}
			}
			t.Fatal("missing read_code tool call")
		})
	}
}
