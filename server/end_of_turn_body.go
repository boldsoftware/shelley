package server

import (
	"encoding/json"
	"fmt"
	"strings"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// finalResponseBody picks a useful notification body line from the tail of
// agent messages in a conversation (newest first). It walks the messages
// looking for one with non-empty text content. If none has any text, it
// summarizes the most recent tool use instead (e.g. "Ran bash: git status").
//
// Returns "" if nothing useful could be extracted; callers fall back to a
// generic "Agent finished" string.
func finalResponseBody(messages []generated.Message) string {
	const maxLen = 10000

	// Pass 1: most recent message with text content wins.
	for _, m := range messages {
		if m.Type != string(db.MessageTypeAgent) || m.LlmData == nil {
			continue
		}
		var llmMsg llm.Message
		if err := json.Unmarshal([]byte(*m.LlmData), &llmMsg); err != nil {
			continue
		}
		text := lastTextContent(llmMsg)
		if text != "" {
			return truncateBody(text, maxLen)
		}
	}

	// Pass 2: no text found — describe the most recent tool use, so the
	// notification at least tells you what the agent was up to (e.g. a
	// turn that ended on a `bash` tool call).
	for _, m := range messages {
		if m.Type != string(db.MessageTypeAgent) || m.LlmData == nil {
			continue
		}
		var llmMsg llm.Message
		if err := json.Unmarshal([]byte(*m.LlmData), &llmMsg); err != nil {
			continue
		}
		if s := summarizeLastToolUse(llmMsg); s != "" {
			return truncateBody(s, maxLen)
		}
	}

	return ""
}

// lastTextContent returns the last non-empty Text content block in msg, or "".
// Matches the existing behavior in publishConversationState.
func lastTextContent(msg llm.Message) string {
	var text string
	for _, c := range msg.Content {
		if c.Type == llm.ContentTypeText && c.Text != "" {
			text = c.Text
		}
	}
	return text
}

// summarizeLastToolUse renders a short "Ran <tool>: <hint>" line for the
// last ToolUse content block in msg, or "" if there isn't one. The hint is
// drawn from a small list of well-known input fields (command/path/url/...)
// and trimmed to a single line.
func summarizeLastToolUse(msg llm.Message) string {
	var last *llm.Content
	for i := range msg.Content {
		if msg.Content[i].Type == llm.ContentTypeToolUse {
			last = &msg.Content[i]
		}
	}
	if last == nil {
		return ""
	}
	name := last.ToolName
	if name == "" {
		name = "tool"
	}
	hint := toolInputHint(last.ToolInput)
	if hint == "" {
		return fmt.Sprintf("Ran %s", name)
	}
	return fmt.Sprintf("Ran %s: %s", name, hint)
}

// toolInputHint extracts a short, single-line hint from a tool's JSON input.
// It looks for common fields ("command", "path", "url", "query",
// "expression", "prompt", "message") in priority order; falls back to the
// first string value it finds.
func toolInputHint(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	for _, key := range []string{"command", "path", "url", "query", "expression", "prompt", "message"} {
		if v, ok := obj[key].(string); ok && v != "" {
			return shortLine(v, 200)
		}
	}
	// Fallback: first string value in the object.
	for _, v := range obj {
		if s, ok := v.(string); ok && s != "" {
			return shortLine(s, 200)
		}
	}
	return ""
}

func shortLine(s string, max int) string {
	// Collapse to first non-empty line so multi-line scripts don't bloat the notif.
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return truncateBody(line, max)
		}
	}
	return truncateBody(strings.TrimSpace(s), max)
}

func truncateBody(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

// pushTitleAndSubtitle picks (title, subtitle) for an iOS push notification.
// We prefer the slug as title because it carries the actionable identity
// of the conversation; hostname goes in the subtitle so iOS renders it on
// a second, smaller line. If there is no slug yet we fall back to using
// hostname as the title (subtitle empty), preserving the prior behavior.
func pushTitleAndSubtitle(hostname, slug string) (title, subtitle string) {
	switch {
	case slug != "" && hostname != "":
		return slug, hostname
	case slug != "":
		return slug, ""
	case hostname != "":
		return hostname, ""
	default:
		return "Shelley", ""
	}
}
