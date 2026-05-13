package loop_test

import (
	"context"
	"fmt"
	"time"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
)

func ExampleLoop() {
	type greetInput struct {
		Name string `json:"name"`
	}

	// Create a simple tool
	testTool := &llm.Tool{
		Name:        "greet",
		Description: "Greets the user with a friendly message",
		InputSchema: llm.MustSchema(`{"type": "object", "properties": {"name": {"type": "string"}}}`),
		Run: llm.RunJSON(func(ctx context.Context, req greetInput) llm.ToolOut {
			return llm.ToolOut{
				LLMContent: llm.TextContent(fmt.Sprintf("Hello, %s! Nice to meet you.", req.Name)),
			}
		}),
	}

	// Message recording function (in real usage, this would save to database)
	recordMessage := func(ctx context.Context, message llm.Message, usage llm.Usage) error {
		roleStr := "user"
		if message.Role == llm.MessageRoleAssistant {
			roleStr = "assistant"
		}
		fmt.Printf("Recorded %s message with %d content items\n", roleStr, len(message.Content))
		return nil
	}

	// Create a loop with initial history
	initialHistory := []llm.Message{
		{
			Role: llm.MessageRoleUser,
			Content: []llm.Content{
				{Type: llm.ContentTypeText, Text: "Hello, I'm Alice"},
			},
		},
	}

	// Set up a predictable service for this example
	service := loop.NewPredictableService()
	myLoop := loop.NewLoop(loop.Config{
		LLM:           service,
		History:       initialHistory,
		Tools:         []*llm.Tool{testTool},
		RecordMessage: recordMessage,
	})

	// Queue a user message that triggers a simple response
	myLoop.QueueUserMessage(llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}},
	})

	// Run the loop for a short time
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	myLoop.Go(ctx)

	// Check usage
	usage := myLoop.GetUsage()
	fmt.Printf("Total usage: %s\n", usage.String())

	// Output:
	// Recorded assistant message with 1 content items
	// Total usage: in: 31, out: 3
}
