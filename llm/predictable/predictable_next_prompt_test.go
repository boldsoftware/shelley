package predictable

import (
	"testing"

	"shelley.exe.dev/llm"
)

// The next-prompt suggestion follows the reply before it. A question gets
// "Yes" and "No" as quick replies; a last line "Quick replies: a,b" gets
// those, split at commas but otherwise verbatim, so tests can try any list.
func TestNextPromptSuggestion(t *testing.T) {
	for reply, want := range map[string]string{
		"water the basil":     `{"quick_replies":[],"suggestion":"echo: More about water the basil"}`,
		"no suggestion":       `{"quick_replies":[],"suggestion":null}`,
		"Prune the rosemary?": `{"quick_replies":["Yes","No"],"suggestion":"echo: More about Prune the rosemary?"}`,
		"Which?\nQuick replies: Option A, Option B,, Yes": `{"quick_replies":["Option A"," Option B",""," Yes"],"suggestion":"echo: More about Which? Quick replies: Option A, Option B,, Yes"}`,
	} {
		resp, err := NewService().Do(t.Context(), &llm.Request{Messages: []llm.Message{
			llm.UserStringMessage("echo: " + reply),
			{Role: llm.MessageRoleAssistant, Content: llm.TextContent(reply)},
			llm.UserStringMessage("<next_prompt_suggestion>\nWhat next?\n</next_prompt_suggestion>"),
		}})
		if err != nil {
			t.Fatal(err)
		}
		if got := llm.FirstText(resp); got != want {
			t.Errorf("after %q: got %s, want %s", reply, got, want)
		}
	}
}
