package claudetool

import (
	"context"
	"encoding/json"
	"strings"

	"shelley.exe.dev/llm"
)

const messageParentName = "message_parent"

// ParentMessenger delivers a subagent's message to its parent conversation.
// This is implemented by the server package to avoid import cycles.
type ParentMessenger interface {
	// MessageParent queues text in the parent of conversationID, attributed
	// to that subagent. A busy parent sees it at its next LLM request; an
	// idle parent starts a turn. final marks the subagent's final report
	// (end_turn=true), as opposed to a progress report.
	MessageParent(ctx context.Context, conversationID, text string, final bool) error
}

// MessageParentTool lets a subagent send progress or a final report.
type MessageParentTool struct {
	Messenger      ParentMessenger
	ConversationID string
}

func (t *MessageParentTool) Tool() *llm.Tool {
	return &llm.Tool{
		Name:        messageParentName,
		Description: "Send progress to the parent (end_turn=false) or a final report and end this turn (end_turn=true). Call alone when ending the turn.",
		InputSchema: llm.MustSchema(`{
  "type": "object",
  "required": ["text", "end_turn"],
  "properties": {
    "text": {"type": "string", "description": "The message for the parent."},
    "end_turn": {"type": "boolean", "description": "True when this is the final report; end the subagent turn after sending it. False for progress reports."}
  }
}`),
		EndsTurnWhen: func(input json.RawMessage) bool {
			var req struct {
				EndTurn bool `json:"end_turn"`
			}
			return json.Unmarshal(input, &req) == nil && req.EndTurn
		},
		Run: t.run,
	}
}

func (t *MessageParentTool) run(ctx context.Context, input json.RawMessage) llm.ToolOut {
	var req struct {
		Text    string `json:"text"`
		EndTurn *bool  `json:"end_turn"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return llm.ErrorfToolOut("parse input: %w", err)
	}
	if strings.TrimSpace(req.Text) == "" {
		return llm.ErrorfToolOut("text is required")
	}
	if req.EndTurn == nil {
		return llm.ErrorfToolOut("end_turn is required")
	}
	if err := t.Messenger.MessageParent(ctx, t.ConversationID, req.Text, *req.EndTurn); err != nil {
		return llm.ErrorfToolOut("message parent: %w", err)
	}
	return llm.ToolOut{LLMContent: llm.TextContent("Sent to parent."), EndTurn: *req.EndTurn}
}
