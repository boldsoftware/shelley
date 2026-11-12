package loop

import (
	"context"
	"testing"
	"time"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
)

func TestLoopWithClaudeTools(t *testing.T) {
	var recordedMessages []llm.Message

	recordFunc := func(ctx context.Context, message llm.Message, _ string, usage llm.Usage, otherUsage []llm.PurposedUsage) error {
		recordedMessages = append(recordedMessages, message)
		return nil
	}

	// Use some actual claudetools
	tools := []*llm.Tool{
		// TODO: Add actual tools when needed
	}

	service := predictable.NewService()

	// Create loop with the configured service
	loop := NewLoop(Config{
		LLM:           service,
		History:       []llm.Message{},
		Tools:         tools,
		RecordMessage: recordFunc,
	})

	// Queue a user message that will trigger a specific predictable response
	userMessage := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}},
	}
	loop.QueueUserMessage(userMessage)

	// Run the loop with a short timeout
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()

	err := loop.Go(ctx)
	if err != context.DeadlineExceeded {
		t.Errorf("expected context deadline exceeded, got %v", err)
	}

	// Verify that messages were recorded
	// Note: User messages are recorded by ConversationManager, not by Loop,
	// so we only expect assistant messages to be recorded here
	if len(recordedMessages) < 1 {
		t.Errorf("expected at least 1 recorded message (assistant), got %d", len(recordedMessages))
	}

	// Check that usage was accumulated
	usage := loop.GetUsage()
	if usage.IsZero() {
		t.Error("expected non-zero usage")
	}

	// Verify conversation history includes user and assistant messages
	history := loop.GetHistory()
	if len(history) < 2 {
		t.Errorf("expected at least 2 history messages, got %d", len(history))
	}

	// Check for expected response
	found := false
	for _, msg := range history {
		if msg.Role == llm.MessageRoleAssistant {
			for _, content := range msg.Content {
				if content.Type == llm.ContentTypeText && content.Text == "Well, hi there!" {
					found = true
					break
				}
			}
		}
	}
	if !found {
		t.Error("expected to find 'Well, hi there!' response")
	}
}

func TestLoopContextCancellation(t *testing.T) {
	service := predictable.NewService()
	loop := NewLoop(Config{
		LLM:     service,
		History: []llm.Message{},
		Tools:   []*llm.Tool{},
		RecordMessage: func(ctx context.Context, message llm.Message, _ string, usage llm.Usage, otherUsage []llm.PurposedUsage) error {
			return nil
		},
	})

	// Cancel context immediately
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := loop.Go(ctx)
	if err != context.Canceled {
		t.Errorf("expected context canceled, got %v", err)
	}
}

func TestLoopSystemMessages(t *testing.T) {
	// Set system messages
	system := []llm.SystemContent{
		{Text: "You are a helpful assistant.", Type: "text"},
	}

	loop := NewLoop(Config{
		LLM:     predictable.NewService(),
		History: []llm.Message{},
		Tools:   []*llm.Tool{},
		System:  system,
		RecordMessage: func(ctx context.Context, message llm.Message, _ string, usage llm.Usage, otherUsage []llm.PurposedUsage) error {
			return nil
		},
	})

	// The system messages are stored and would be passed to LLM
	loop.mu.Lock()
	if len(loop.system) != 1 {
		t.Errorf("expected 1 system message, got %d", len(loop.system))
	}
	if loop.system[0].Text != "You are a helpful assistant." {
		t.Errorf("unexpected system message text: %s", loop.system[0].Text)
	}
	loop.mu.Unlock()
}
