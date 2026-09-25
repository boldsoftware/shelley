package db

import (
	"context"
	"encoding/json"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// RecordInterruptedToolResults closes tool calls left without results by a
// restart. turn_interrupted is only set when no live turn owns the
// conversation (or a startup resume holds its claim), so pending calls are
// orphans. Returns the committed result, or nil if nothing was
// written. Repeated attempts are idempotent.
func (db *DB) RecordInterruptedToolResults(ctx context.Context, conversationID string) (*generated.Message, error) {
	conversation, err := db.GetConversationByID(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	if !conversation.TurnInterrupted || conversation.ParentConversationID != nil {
		return nil, nil
	}
	var result *generated.Message
	err = db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		conversation, err := q.GetConversation(ctx, conversationID)
		if err != nil {
			return err
		}
		if !conversation.TurnInterrupted || conversation.ParentConversationID != nil {
			return nil
		}
		messages, err := q.ListMessagesForContext(ctx, conversationID)
		if err != nil {
			return err
		}
		pending := make(map[string]bool)
		var ids []string
		for _, message := range messages {
			if (message.Type != string(MessageTypeAgent) && message.Type != string(MessageTypeUser)) || message.LlmData == nil {
				continue
			}
			var data llm.Message
			if json.Unmarshal([]byte(*message.LlmData), &data) != nil {
				continue
			}
			for _, content := range data.Content {
				switch content.Type {
				case llm.ContentTypeToolUse:
					if content.ID != "" && !pending[content.ID] {
						ids = append(ids, content.ID)
						pending[content.ID] = true
					}
				case llm.ContentTypeToolResult:
					delete(pending, content.ToolUseID)
				}
			}
		}
		var results []llm.Content
		for _, id := range ids {
			if pending[id] {
				results = append(results, llm.Content{
					Type: llm.ContentTypeToolResult, ToolUseID: id, ToolError: true,
					ToolResult: llm.TextContent("Interrupted"),
				})
			}
		}
		if len(results) == 0 {
			return nil
		}
		message, err := insertMessageTx(ctx, q, CreateMessageParams{
			ConversationID: conversationID,
			Type:           MessageTypeUser,
			LLMData:        llm.Message{Role: llm.MessageRoleUser, Content: results},
			UserData:       map[string]bool{"interrupted_tool_result": true},
		})
		if err != nil {
			return err
		}
		result = &message
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
