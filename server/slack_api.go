package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/slug"
)

// SlackConversationAPI adapts the Server to the slack.ConversationAPI interface.
type SlackConversationAPI struct {
	server *Server
}

// NewSlackConversationAPI creates a new SlackConversationAPI.
func NewSlackConversationAPI(s *Server) *SlackConversationAPI {
	return &SlackConversationAPI{server: s}
}

// NewConversation creates a new Shelley conversation and sends the first message.
func (a *SlackConversationAPI) NewConversation(ctx context.Context, message, model string) (string, error) {
	if model == "" {
		model = a.server.defaultModel
	}

	llmService, err := a.server.llmManager.GetService(model)
	if err != nil {
		return "", fmt.Errorf("get llm service: %w", err)
	}

	conversation, err := a.server.db.CreateConversation(ctx, nil, true, nil, &model)
	if err != nil {
		return "", fmt.Errorf("create conversation: %w", err)
	}
	convID := conversation.ConversationID

	go a.server.publishConversationListUpdate(ConversationListUpdate{
		Type:         "update",
		Conversation: conversation,
	})

	manager, err := a.server.getOrCreateConversationManager(ctx, convID, "")
	if err != nil {
		return "", fmt.Errorf("get conversation manager: %w", err)
	}

	userMessage := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: message}},
	}

	firstMessage, err := manager.AcceptUserMessage(ctx, llmService, model, userMessage)
	if err != nil {
		return "", fmt.Errorf("accept user message: %w", err)
	}

	if firstMessage {
		go func() {
			ctx := context.WithoutCancel(ctx)
			slugCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			_, err := slug.GenerateSlug(slugCtx, a.server.llmManager, a.server.db, a.server.logger, convID, message, model)
			if err != nil {
				a.server.logger.Warn("failed to generate slug", "conversation_id", convID, "error", err)
			} else {
				go a.server.notifySubscribers(ctx, convID)
			}
		}()
	}

	return convID, nil
}

// SendMessage sends a message to an existing conversation.
func (a *SlackConversationAPI) SendMessage(ctx context.Context, conversationID, message, model string) error {
	if model == "" {
		model = a.server.defaultModel
	}

	llmService, err := a.server.llmManager.GetService(model)
	if err != nil {
		return fmt.Errorf("get llm service: %w", err)
	}

	manager, err := a.server.getOrCreateConversationManager(ctx, conversationID, "")
	if err != nil {
		return fmt.Errorf("get conversation manager: %w", err)
	}

	userMessage := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: message}},
	}

	_, err = manager.AcceptUserMessage(ctx, llmService, model, userMessage)
	if err != nil {
		return fmt.Errorf("accept user message: %w", err)
	}

	return nil
}

// GetLatestAgentResponse returns the message ID and text of the latest agent message.
func (a *SlackConversationAPI) GetLatestAgentResponse(conversationID string) (messageID string, text string) {
	msg, err := a.server.db.GetLatestMessage(context.Background(), conversationID)
	if err != nil {
		a.server.logger.Error("failed to get latest message", "conversation_id", conversationID, "error", err)
		return "", ""
	}

	if msg.Type != string(db.MessageTypeAgent) || msg.LlmData == nil {
		return "", ""
	}

	var llmMsg llm.Message
	if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
		return "", ""
	}

	for _, c := range llmMsg.Content {
		if c.Type == llm.ContentTypeText && c.Text != "" {
			text = c.Text
		}
	}

	return msg.MessageID, text
}
