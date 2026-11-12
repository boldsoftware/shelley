package loop

import (
	"context"
	"os"
	"testing"
	"time"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/ant"
)

// TestLoopWithClaude tests the loop with actual Claude API if key is available
func TestLoopWithClaude(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping Claude integration test - ANTHROPIC_API_KEY not set")
	}

	// Create a simple conversation with Claude service
	loop := NewLoop(Config{
		LLM: &ant.Service{
			APIKey: apiKey,
			Model:  ant.Claude45Haiku, // Use cheaper model for testing
		},
		History: []llm.Message{},
		Tools:   []*llm.Tool{},
		RecordMessage: func(ctx context.Context, message llm.Message, _ string, usage llm.Usage, otherUsage []llm.PurposedUsage) error {
			// In a real app, this would save to database
			t.Logf("Recorded %s message: %s", message.Role, message.Content[0].Text)
			return nil
		},
	})

	// Queue a simple user message
	loop.QueueUserMessage(llm.UserStringMessage("Hello! Please respond with just 'Hi there!' and nothing else."))

	// Run with a reasonable timeout
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	err := loop.Go(ctx)
	if err != context.DeadlineExceeded {
		t.Errorf("expected context deadline exceeded, got %v", err)
	}

	// Check that usage was tracked
	usage := loop.GetUsage()
	if usage.IsZero() {
		t.Error("expected non-zero usage from Claude API")
	}

	t.Logf("Claude API usage: %s", usage.String())

	// Check conversation history
	history := loop.GetHistory()
	if len(history) < 2 {
		t.Errorf("expected at least 2 messages in history, got %d", len(history))
	}

	// First should be user message, second should be assistant
	if history[0].Role != llm.MessageRoleUser {
		t.Errorf("first message should be user, got %v", history[0].Role)
	}

	if len(history) > 1 && history[1].Role != llm.MessageRoleAssistant {
		t.Errorf("second message should be assistant, got %v", history[1].Role)
	}
}
