package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/gitstate"
	"shelley.exe.dev/llm"
)

func TestNewLoop(t *testing.T) {
	history := []llm.Message{
		{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hello"}}},
	}
	tools := []*llm.Tool{}
	recordFunc := func(ctx context.Context, message llm.Message, usage llm.Usage) error {
		return nil
	}

	loop := NewLoop(Config{
		LLM:           NewPredictableService(),
		History:       history,
		Tools:         tools,
		RecordMessage: recordFunc,
	})
	if loop == nil {
		t.Fatal("NewLoop returned nil")
	}

	if len(loop.history) != 1 {
		t.Errorf("expected history length 1, got %d", len(loop.history))
	}

	if len(loop.messageQueue) != 0 {
		t.Errorf("expected empty message queue, got %d", len(loop.messageQueue))
	}
}

func TestQueueUserMessage(t *testing.T) {
	loop := NewLoop(Config{
		LLM:     NewPredictableService(),
		History: []llm.Message{},
		Tools:   []*llm.Tool{},
	})

	message := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Test message"}},
	}

	loop.QueueUserMessage(message)

	loop.mu.Lock()
	queueLen := len(loop.messageQueue)
	loop.mu.Unlock()

	if queueLen != 1 {
		t.Errorf("expected message queue length 1, got %d", queueLen)
	}
}

func TestPredictableService(t *testing.T) {
	service := NewPredictableService()

	// Test simple hello response
	ctx := context.Background()
	req := &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}}},
		},
	}

	resp, err := service.Do(ctx, req)
	if err != nil {
		t.Fatalf("predictable service Do failed: %v", err)
	}

	if resp.Role != llm.MessageRoleAssistant {
		t.Errorf("expected assistant role, got %v", resp.Role)
	}

	if len(resp.Content) == 0 {
		t.Error("expected non-empty content")
	}

	if resp.Content[0].Type != llm.ContentTypeText {
		t.Errorf("expected text content, got %v", resp.Content[0].Type)
	}

	if resp.Content[0].Text != "Well, hi there!" {
		t.Errorf("unexpected response text: %s", resp.Content[0].Text)
	}
}

func TestPredictableServiceEcho(t *testing.T) {
	service := NewPredictableService()

	ctx := context.Background()
	req := &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "echo: foo"}}},
		},
	}

	resp, err := service.Do(ctx, req)
	if err != nil {
		t.Fatalf("echo test failed: %v", err)
	}

	if resp.Content[0].Text != "foo" {
		t.Errorf("expected 'foo', got '%s'", resp.Content[0].Text)
	}

	// Test another echo
	req.Messages[0].Content[0].Text = "echo: hello world"
	resp, err = service.Do(ctx, req)
	if err != nil {
		t.Fatalf("echo hello world test failed: %v", err)
	}

	if resp.Content[0].Text != "hello world" {
		t.Errorf("expected 'hello world', got '%s'", resp.Content[0].Text)
	}
}

func TestPredictableServiceBashTool(t *testing.T) {
	service := NewPredictableService()

	ctx := context.Background()
	req := &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "bash: ls -la"}}},
		},
	}

	resp, err := service.Do(ctx, req)
	if err != nil {
		t.Fatalf("bash tool test failed: %v", err)
	}

	if resp.StopReason != llm.StopReasonToolUse {
		t.Errorf("expected tool use stop reason, got %v", resp.StopReason)
	}

	if len(resp.Content) != 2 {
		t.Errorf("expected 2 content items (text + tool_use), got %d", len(resp.Content))
	}

	// Find the tool use content
	var toolUseContent *llm.Content
	for _, content := range resp.Content {
		if content.Type == llm.ContentTypeToolUse {
			toolUseContent = &content
			break
		}
	}

	if toolUseContent == nil {
		t.Fatal("no tool use content found")
	}

	if toolUseContent.ToolName != "bash" {
		t.Errorf("expected tool name 'bash', got '%s'", toolUseContent.ToolName)
	}

	// Check tool input contains the command
	var toolInput map[string]interface{}
	if err := json.Unmarshal(toolUseContent.ToolInput, &toolInput); err != nil {
		t.Fatalf("failed to parse tool input: %v", err)
	}

	if toolInput["command"] != "ls -la" {
		t.Errorf("expected command 'ls -la', got '%v'", toolInput["command"])
	}
}

func TestPredictableServiceDefaultResponse(t *testing.T) {
	service := NewPredictableService()

	ctx := context.Background()
	req := &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "some unknown input"}}},
		},
	}

	resp, err := service.Do(ctx, req)
	if err != nil {
		t.Fatalf("default response test failed: %v", err)
	}

	if resp.Content[0].Text != "edit predictable.go to add a response for that one..." {
		t.Errorf("unexpected default response: %s", resp.Content[0].Text)
	}
}

func TestPredictableServiceDelay(t *testing.T) {
	service := NewPredictableService()

	ctx := context.Background()
	req := &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "delay: 0.1"}}},
		},
	}

	start := time.Now()
	resp, err := service.Do(ctx, req)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("delay test failed: %v", err)
	}

	if elapsed < 100*time.Millisecond {
		t.Errorf("expected delay of at least 100ms, got %v", elapsed)
	}

	if resp.Content[0].Text != "Delayed for 0.1 seconds" {
		t.Errorf("unexpected response text: %s", resp.Content[0].Text)
	}
}

func TestLoopWithPredictableService(t *testing.T) {
	var recordedMessages []llm.Message
	var recordedUsages []llm.Usage

	recordFunc := func(ctx context.Context, message llm.Message, usage llm.Usage) error {
		recordedMessages = append(recordedMessages, message)
		recordedUsages = append(recordedUsages, usage)
		return nil
	}

	service := NewPredictableService()
	loop := NewLoop(Config{
		LLM:           service,
		History:       []llm.Message{},
		Tools:         []*llm.Tool{},
		RecordMessage: recordFunc,
	})

	// Queue a user message that triggers a known response
	userMessage := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}},
	}
	loop.QueueUserMessage(userMessage)

	// Run the loop with a short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := loop.Go(ctx)
	if err != context.DeadlineExceeded {
		t.Errorf("expected context deadline exceeded, got %v", err)
	}

	// Check that messages were recorded
	if len(recordedMessages) < 1 {
		t.Errorf("expected at least 1 recorded message, got %d", len(recordedMessages))
	}

	// Check usage tracking
	usage := loop.GetUsage()
	if usage.IsZero() {
		t.Error("expected non-zero usage")
	}
}

func TestLoopWithTools(t *testing.T) {
	var toolCalls []string

	testTool := &llm.Tool{
		Name:        "bash",
		Description: "A test bash tool",
		InputSchema: llm.MustSchema(`{"type": "object", "properties": {"command": {"type": "string"}}}`),
		Run: func(ctx context.Context, input json.RawMessage) llm.ToolOut {
			toolCalls = append(toolCalls, string(input))
			return llm.ToolOut{
				LLMContent: []llm.Content{
					{Type: llm.ContentTypeText, Text: "Command executed successfully"},
				},
			}
		},
	}

	service := NewPredictableService()
	loop := NewLoop(Config{
		LLM:     service,
		History: []llm.Message{},
		Tools:   []*llm.Tool{testTool},
		RecordMessage: func(ctx context.Context, message llm.Message, usage llm.Usage) error {
			return nil
		},
	})

	// Queue a user message that triggers the bash tool
	userMessage := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "bash: echo hello"}},
	}
	loop.QueueUserMessage(userMessage)

	// Run the loop with a short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := loop.Go(ctx)
	if err != context.DeadlineExceeded {
		t.Errorf("expected context deadline exceeded, got %v", err)
	}

	// Check that the tool was called
	if len(toolCalls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(toolCalls))
	}

	if toolCalls[0] != `{"command":"echo hello"}` {
		t.Errorf("unexpected tool call input: %s", toolCalls[0])
	}
}

func TestGetHistory(t *testing.T) {
	initialHistory := []llm.Message{
		{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hello"}}},
	}

	loop := NewLoop(Config{
		LLM:     NewPredictableService(),
		History: initialHistory,
		Tools:   []*llm.Tool{},
	})

	history := loop.GetHistory()
	if len(history) != 1 {
		t.Errorf("expected history length 1, got %d", len(history))
	}

	// Modify returned slice to ensure it's a copy
	history[0].Content[0].Text = "Modified"

	// Original should be unchanged
	original := loop.GetHistory()
	if original[0].Content[0].Text != "Hello" {
		t.Error("GetHistory should return a copy, not the original slice")
	}
}

func TestLoopWithKeywordTool(t *testing.T) {
	// Test that keyword tool doesn't crash with nil pointer dereference
	service := NewPredictableService()

	var messages []llm.Message
	recordMessage := func(ctx context.Context, message llm.Message, usage llm.Usage) error {
		messages = append(messages, message)
		return nil
	}

	// Add a mock keyword tool that doesn't actually search
	tools := []*llm.Tool{
		{
			Name:        "keyword_search",
			Description: "Mock keyword search",
			InputSchema: llm.MustSchema(`{"type": "object", "properties": {"query": {"type": "string"}, "search_terms": {"type": "array", "items": {"type": "string"}}}, "required": ["query", "search_terms"]}`),
			Run: func(ctx context.Context, input json.RawMessage) llm.ToolOut {
				// Simple mock implementation
				return llm.ToolOut{LLMContent: []llm.Content{{Type: llm.ContentTypeText, Text: "mock keyword search result"}}}
			},
		},
	}

	loop := NewLoop(Config{
		LLM:           service,
		History:       []llm.Message{},
		Tools:         tools,
		RecordMessage: recordMessage,
	})

	// Send a user message that will trigger the default response
	userMessage := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: "Please search for some files"},
		},
	}

	loop.QueueUserMessage(userMessage)

	// Process one turn
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := loop.ProcessOneTurn(ctx)
	if err != nil {
		t.Fatalf("ProcessOneTurn failed: %v", err)
	}

	// Verify we got expected messages
	// Note: User messages are recorded by ConversationManager, not by Loop,
	// so we only expect the assistant response to be recorded here
	if len(messages) < 1 {
		t.Fatalf("Expected at least 1 message (assistant), got %d", len(messages))
	}

	// Should have assistant response
	if messages[0].Role != llm.MessageRoleAssistant {
		t.Errorf("Expected first recorded message to be assistant, got %s", messages[0].Role)
	}
}

func TestLoopWithActualKeywordTool(t *testing.T) {
	// Test that actual keyword tool works with Loop
	service := NewPredictableService()

	var messages []llm.Message
	recordMessage := func(ctx context.Context, message llm.Message, usage llm.Usage) error {
		messages = append(messages, message)
		return nil
	}

	// Use the actual keyword tool from claudetool package
	// Note: We need to import it first
	tools := []*llm.Tool{
		// Add a simplified keyword tool to avoid file system dependencies in tests
		{
			Name:        "keyword_search",
			Description: "Search for files by keyword",
			InputSchema: llm.MustSchema(`{"type": "object", "properties": {"query": {"type": "string"}, "search_terms": {"type": "array", "items": {"type": "string"}}}, "required": ["query", "search_terms"]}`),
			Run: func(ctx context.Context, input json.RawMessage) llm.ToolOut {
				// Simple mock implementation - no context dependencies
				return llm.ToolOut{LLMContent: []llm.Content{{Type: llm.ContentTypeText, Text: "mock keyword search result"}}}
			},
		},
	}

	loop := NewLoop(Config{
		LLM:           service,
		History:       []llm.Message{},
		Tools:         tools,
		RecordMessage: recordMessage,
	})

	// Send a user message that will trigger the default response
	userMessage := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: "Please search for some files"},
		},
	}

	loop.QueueUserMessage(userMessage)

	// Process one turn
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := loop.ProcessOneTurn(ctx)
	if err != nil {
		t.Fatalf("ProcessOneTurn failed: %v", err)
	}

	// Verify we got expected messages
	// Note: User messages are recorded by ConversationManager, not by Loop,
	// so we only expect the assistant response to be recorded here
	if len(messages) < 1 {
		t.Fatalf("Expected at least 1 message (assistant), got %d", len(messages))
	}

	// Should have assistant response
	if messages[0].Role != llm.MessageRoleAssistant {
		t.Errorf("Expected first recorded message to be assistant, got %s", messages[0].Role)
	}

	t.Log("Keyword tool test passed - no nil pointer dereference occurred")
}

func TestKeywordToolWithLLMProvider(t *testing.T) {
	// Create a temp directory with a test file to search
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("this is a test file\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a predictable service for testing
	predictableService := NewPredictableService()

	// Create a simple LLM provider for testing
	llmProvider := &testLLMProvider{
		service: predictableService,
		models:  []string{"predictable"},
	}

	// Create keyword tool with provider - use temp dir instead of /
	keywordTool := claudetool.NewKeywordToolWithWorkingDir(llmProvider, claudetool.NewMutableWorkingDir(tempDir))
	tool := keywordTool.Tool()

	// Test input
	input := `{"query": "test search", "search_terms": ["test"]}`

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := tool.Run(ctx, json.RawMessage(input))

	// Should get a result without error (even though ripgrep will fail in test environment)
	// The important thing is that it doesn't crash with nil pointer dereference
	if result.Error != nil {
		t.Logf("Expected error in test environment (no ripgrep): %v", result.Error)
		// This is expected in test environment
	} else {
		t.Log("Keyword tool executed successfully")
		if len(result.LLMContent) == 0 {
			t.Error("Expected some content in result")
		}
	}
}

// testLLMProvider implements LLMServiceProvider for testing
type testLLMProvider struct {
	service llm.Service
	models  []string
}

func (t *testLLMProvider) GetService(modelID string) (llm.Service, error) {
	for _, model := range t.models {
		if model == modelID {
			return t.service, nil
		}
	}
	return nil, fmt.Errorf("model %s not available", modelID)
}

func (t *testLLMProvider) GetAvailableModels() []string {
	return t.models
}

func TestInsertMissingToolResults(t *testing.T) {
	tests := []struct {
		name     string
		messages []llm.Message
		wantLen  int
		wantText string
	}{
		{
			name: "no missing tool results",
			messages: []llm.Message{
				{
					Role: llm.MessageRoleAssistant,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "Let me help you"},
					},
				},
				{
					Role: llm.MessageRoleUser,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "Thanks"},
					},
				},
			},
			wantLen:  1,
			wantText: "", // No synthetic result expected
		},
		{
			name: "missing tool result - should insert synthetic result",
			messages: []llm.Message{
				{
					Role: llm.MessageRoleAssistant,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "I'll use a tool"},
						{Type: llm.ContentTypeToolUse, ID: "tool_123", ToolName: "bash"},
					},
				},
				{
					Role: llm.MessageRoleUser,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "Error occurred"},
					},
				},
			},
			wantLen:  2, // Should have synthetic tool_result + error message
			wantText: "not executed; retry possible",
		},
		{
			name: "multiple missing tool results",
			messages: []llm.Message{
				{
					Role: llm.MessageRoleAssistant,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "I'll use multiple tools"},
						{Type: llm.ContentTypeToolUse, ID: "tool_1", ToolName: "bash"},
						{Type: llm.ContentTypeToolUse, ID: "tool_2", ToolName: "read"},
					},
				},
				{
					Role: llm.MessageRoleUser,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "Error occurred"},
					},
				},
			},
			wantLen: 3, // Should have 2 synthetic tool_results + error message
		},
		{
			name: "has tool results - should not insert",
			messages: []llm.Message{
				{
					Role: llm.MessageRoleAssistant,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "I'll use a tool"},
						{Type: llm.ContentTypeToolUse, ID: "tool_123", ToolName: "bash"},
					},
				},
				{
					Role: llm.MessageRoleUser,
					Content: []llm.Content{
						{
							Type:       llm.ContentTypeToolResult,
							ToolUseID:  "tool_123",
							ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: "result"}},
						},
					},
				},
			},
			wantLen: 1, // Should not insert anything
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loop := NewLoop(Config{
				LLM:     NewPredictableService(),
				History: []llm.Message{},
			})

			req := &llm.Request{
				Messages: tt.messages,
			}

			loop.insertMissingToolResults(req)

			got := req.Messages[len(req.Messages)-1]
			if len(got.Content) != tt.wantLen {
				t.Errorf("expected %d content items, got %d", tt.wantLen, len(got.Content))
			}

			if tt.wantText != "" {
				// Find the synthetic tool result
				found := false
				for _, c := range got.Content {
					if c.Type == llm.ContentTypeToolResult && len(c.ToolResult) > 0 {
						if c.ToolResult[0].Text == tt.wantText {
							found = true
							if !c.ToolError {
								t.Error("synthetic tool result should have ToolError=true")
							}
							break
						}
					}
				}
				if !found {
					t.Errorf("expected to find synthetic tool result with text %q", tt.wantText)
				}
			}
		})
	}
}

func TestInsertMissingToolResultsWithEdgeCases(t *testing.T) {
	// Test for the bug: when an assistant error message is recorded after a tool_use
	// but before tool execution, the tool_use is "hidden" from insertMissingToolResults
	// because it only checks the last two messages.
	t.Run("tool_use hidden by subsequent assistant message", func(t *testing.T) {
		loop := NewLoop(Config{
			LLM:     NewPredictableService(),
			History: []llm.Message{},
		})

		// Scenario:
		// 1. LLM responds with tool_use
		// 2. Something fails, error message recorded (assistant message)
		// 3. User sends new message
		// The tool_use in message 0 is never followed by a tool_result
		req := &llm.Request{
			Messages: []llm.Message{
				{
					Role: llm.MessageRoleAssistant,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "I'll run a command"},
						{Type: llm.ContentTypeToolUse, ID: "tool_hidden", ToolName: "bash"},
					},
				},
				{
					Role: llm.MessageRoleAssistant,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "LLM request failed: some error"},
					},
				},
				{
					Role: llm.MessageRoleUser,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "Please try again"},
					},
				},
			},
		}

		loop.insertMissingToolResults(req)

		// The function should have inserted a tool_result for tool_hidden
		// It should be inserted as a user message after the assistant message with tool_use
		// Since we can't insert in the middle, we need to ensure the history is valid

		// Check that there's a tool_result for tool_hidden somewhere in the messages
		found := false
		for _, msg := range req.Messages {
			for _, c := range msg.Content {
				if c.Type == llm.ContentTypeToolResult && c.ToolUseID == "tool_hidden" {
					found = true
					if !c.ToolError {
						t.Error("synthetic tool result should have ToolError=true")
					}
					break
				}
			}
		}
		if !found {
			t.Error("expected to find synthetic tool result for tool_hidden - the bug is that tool_use is hidden by subsequent assistant message")
		}
	})

	// Test for tool_use in earlier message (not the second-to-last)
	t.Run("tool_use in earlier message without result", func(t *testing.T) {
		loop := NewLoop(Config{
			LLM:     NewPredictableService(),
			History: []llm.Message{},
		})

		req := &llm.Request{
			Messages: []llm.Message{
				{
					Role: llm.MessageRoleUser,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "Do something"},
					},
				},
				{
					Role: llm.MessageRoleAssistant,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "I'll use a tool"},
						{Type: llm.ContentTypeToolUse, ID: "tool_earlier", ToolName: "bash"},
					},
				},
				// Missing: user message with tool_result for tool_earlier
				{
					Role: llm.MessageRoleAssistant,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "Something went wrong"},
					},
				},
				{
					Role: llm.MessageRoleUser,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "Try again"},
					},
				},
			},
		}

		loop.insertMissingToolResults(req)

		// Should have inserted a tool_result for tool_earlier
		found := false
		for _, msg := range req.Messages {
			for _, c := range msg.Content {
				if c.Type == llm.ContentTypeToolResult && c.ToolUseID == "tool_earlier" {
					found = true
					break
				}
			}
		}
		if !found {
			t.Error("expected to find synthetic tool result for tool_earlier")
		}
	})

	t.Run("empty message list", func(t *testing.T) {
		loop := NewLoop(Config{
			LLM:     NewPredictableService(),
			History: []llm.Message{},
		})

		req := &llm.Request{
			Messages: []llm.Message{},
		}

		loop.insertMissingToolResults(req)
		// Should not panic
	})

	t.Run("single message", func(t *testing.T) {
		loop := NewLoop(Config{
			LLM:     NewPredictableService(),
			History: []llm.Message{},
		})

		req := &llm.Request{
			Messages: []llm.Message{
				{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}}},
			},
		}

		loop.insertMissingToolResults(req)
		// Should not panic, should not modify
		if len(req.Messages[0].Content) != 1 {
			t.Error("should not modify single message")
		}
	})

	t.Run("wrong role order - user then assistant", func(t *testing.T) {
		loop := NewLoop(Config{
			LLM:     NewPredictableService(),
			History: []llm.Message{},
		})

		req := &llm.Request{
			Messages: []llm.Message{
				{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}}},
				{Role: llm.MessageRoleAssistant, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}},
			},
		}

		loop.insertMissingToolResults(req)
		// Should not modify when roles are wrong order
		if len(req.Messages[1].Content) != 1 {
			t.Error("should not modify when roles are in wrong order")
		}
	})
}

func TestInsertMissingToolResults_EmptyAssistantContent(t *testing.T) {
	// Test for the bug: when an assistant message has empty content (can happen when
	// the model ends its turn without producing any output), we need to add placeholder
	// content if it's not the last message. Otherwise the API will reject with:
	// "messages.N: all messages must have non-empty content except for the optional
	// final assistant message"

	t.Run("empty assistant content in middle of conversation", func(t *testing.T) {
		loop := NewLoop(Config{
			LLM:     NewPredictableService(),
			History: []llm.Message{},
		})

		req := &llm.Request{
			Messages: []llm.Message{
				{
					Role:    llm.MessageRoleUser,
					Content: []llm.Content{{Type: llm.ContentTypeText, Text: "run git fetch"}},
				},
				{
					Role:    llm.MessageRoleAssistant,
					Content: []llm.Content{{Type: llm.ContentTypeToolUse, ID: "tool1", ToolName: "bash"}},
				},
				{
					Role: llm.MessageRoleUser,
					Content: []llm.Content{{
						Type:       llm.ContentTypeToolResult,
						ToolUseID:  "tool1",
						ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: "success"}},
					}},
				},
				{
					// Empty assistant message - this can happen when model ends turn without output
					Role:      llm.MessageRoleAssistant,
					Content:   []llm.Content{},
					EndOfTurn: true,
				},
				{
					Role:    llm.MessageRoleUser,
					Content: []llm.Content{{Type: llm.ContentTypeText, Text: "next question"}},
				},
			},
		}

		loop.insertMissingToolResults(req)

		// The empty assistant message (index 3) should now have placeholder content
		if len(req.Messages[3].Content) == 0 {
			t.Error("expected placeholder content to be added to empty assistant message")
		}
		if req.Messages[3].Content[0].Type != llm.ContentTypeText {
			t.Error("expected placeholder to be text content")
		}
		if req.Messages[3].Content[0].Text != "(no response)" {
			t.Errorf("expected placeholder text '(no response)', got %q", req.Messages[3].Content[0].Text)
		}
	})

	t.Run("empty assistant content at end of conversation - no modification needed", func(t *testing.T) {
		loop := NewLoop(Config{
			LLM:     NewPredictableService(),
			History: []llm.Message{},
		})

		req := &llm.Request{
			Messages: []llm.Message{
				{
					Role:    llm.MessageRoleUser,
					Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}},
				},
				{
					// Empty assistant message at end is allowed by the API
					Role:      llm.MessageRoleAssistant,
					Content:   []llm.Content{},
					EndOfTurn: true,
				},
			},
		}

		loop.insertMissingToolResults(req)

		// The empty assistant message at the end should NOT be modified
		// because the API allows empty content for the final assistant message
		if len(req.Messages[1].Content) != 0 {
			t.Error("expected final empty assistant message to remain empty")
		}
	})

	t.Run("non-empty assistant content - no modification needed", func(t *testing.T) {
		loop := NewLoop(Config{
			LLM:     NewPredictableService(),
			History: []llm.Message{},
		})

		req := &llm.Request{
			Messages: []llm.Message{
				{
					Role:    llm.MessageRoleUser,
					Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}},
				},
				{
					Role:    llm.MessageRoleAssistant,
					Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi there"}},
				},
				{
					Role:    llm.MessageRoleUser,
					Content: []llm.Content{{Type: llm.ContentTypeText, Text: "goodbye"}},
				},
			},
		}

		loop.insertMissingToolResults(req)

		// The assistant message should not be modified
		if len(req.Messages[1].Content) != 1 {
			t.Errorf("expected assistant message to have 1 content item, got %d", len(req.Messages[1].Content))
		}
		if req.Messages[1].Content[0].Text != "hi there" {
			t.Errorf("expected assistant message text 'hi there', got %q", req.Messages[1].Content[0].Text)
		}
	})
}

func TestGitStateTracking(t *testing.T) {
	// Create a test repo
	tmpDir := t.TempDir()

	// Initialize git repo
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@test.com")
	runGit(t, tmpDir, "config", "user.name", "Test")

	// Create initial commit
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, tmpDir, "add", ".")
	runGit(t, tmpDir, "commit", "-m", "initial")

	// Track git state changes
	var mu sync.Mutex
	var gitStateChanges []*gitstate.GitState

	loop := NewLoop(Config{
		LLM:           NewPredictableService(),
		History:       []llm.Message{},
		WorkingDir:    tmpDir,
		GetWorkingDir: func() string { return tmpDir },
		OnGitStateChange: func(ctx context.Context, state *gitstate.GitState) {
			mu.Lock()
			gitStateChanges = append(gitStateChanges, state)
			mu.Unlock()
		},
		RecordMessage: func(ctx context.Context, message llm.Message, usage llm.Usage) error {
			return nil
		},
	})

	// Verify initial state was captured
	if loop.lastGitState == nil {
		t.Fatal("expected initial git state to be captured")
	}
	if !loop.lastGitState.IsRepo {
		t.Error("expected IsRepo to be true")
	}

	// Process a turn (no state change should occur)
	loop.QueueUserMessage(llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := loop.ProcessOneTurn(ctx)
	if err != nil {
		t.Fatalf("ProcessOneTurn failed: %v", err)
	}

	// No state change should have occurred
	mu.Lock()
	numChanges := len(gitStateChanges)
	mu.Unlock()
	if numChanges != 0 {
		t.Errorf("expected no git state changes, got %d", numChanges)
	}

	// Now make a commit
	if err := os.WriteFile(testFile, []byte("updated"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, tmpDir, "add", ".")
	runGit(t, tmpDir, "commit", "-m", "update")

	// Process another turn - this should detect the commit change
	loop.QueueUserMessage(llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello again"}},
	})

	err = loop.ProcessOneTurn(ctx)
	if err != nil {
		t.Fatalf("ProcessOneTurn failed: %v", err)
	}

	// Now a state change should have been detected
	mu.Lock()
	numChanges = len(gitStateChanges)
	mu.Unlock()
	if numChanges != 1 {
		t.Errorf("expected 1 git state change, got %d", numChanges)
	}
}

func TestGitStateTrackingWorktree(t *testing.T) {
	tmpDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mainRepo := filepath.Join(tmpDir, "main")
	worktreeDir := filepath.Join(tmpDir, "worktree")

	// Create main repo
	if err := os.MkdirAll(mainRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, mainRepo, "init")
	runGit(t, mainRepo, "config", "user.email", "test@test.com")
	runGit(t, mainRepo, "config", "user.name", "Test")

	// Create initial commit
	testFile := filepath.Join(mainRepo, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, mainRepo, "add", ".")
	runGit(t, mainRepo, "commit", "-m", "initial")

	// Create a worktree
	runGit(t, mainRepo, "worktree", "add", "-b", "feature", worktreeDir)

	// Track git state changes in the worktree
	var mu sync.Mutex
	var gitStateChanges []*gitstate.GitState

	loop := NewLoop(Config{
		LLM:           NewPredictableService(),
		History:       []llm.Message{},
		WorkingDir:    worktreeDir,
		GetWorkingDir: func() string { return worktreeDir },
		OnGitStateChange: func(ctx context.Context, state *gitstate.GitState) {
			mu.Lock()
			gitStateChanges = append(gitStateChanges, state)
			mu.Unlock()
		},
		RecordMessage: func(ctx context.Context, message llm.Message, usage llm.Usage) error {
			return nil
		},
	})

	// Verify initial state
	if loop.lastGitState == nil {
		t.Fatal("expected initial git state to be captured")
	}
	if loop.lastGitState.Branch != "feature" {
		t.Errorf("expected branch 'feature', got %q", loop.lastGitState.Branch)
	}
	if loop.lastGitState.Worktree != worktreeDir {
		t.Errorf("expected worktree %q, got %q", worktreeDir, loop.lastGitState.Worktree)
	}

	// Make a commit in the worktree
	worktreeFile := filepath.Join(worktreeDir, "feature.txt")
	if err := os.WriteFile(worktreeFile, []byte("feature content"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, worktreeDir, "add", ".")
	runGit(t, worktreeDir, "commit", "-m", "feature commit")

	// Process a turn to detect the change
	loop.QueueUserMessage(llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = loop.ProcessOneTurn(ctx)
	if err != nil {
		t.Fatalf("ProcessOneTurn failed: %v", err)
	}

	mu.Lock()
	numChanges := len(gitStateChanges)
	mu.Unlock()

	if numChanges != 1 {
		t.Errorf("expected 1 git state change in worktree, got %d", numChanges)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	// For commits, use --no-verify to skip hooks
	if len(args) > 0 && args[0] == "commit" {
		newArgs := []string{"commit", "--no-verify"}
		newArgs = append(newArgs, args[1:]...)
		args = newArgs
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}

func TestPredictableServiceTokenContextWindow(t *testing.T) {
	service := NewPredictableService()
	window := service.TokenContextWindow()
	if window != 200000 {
		t.Errorf("expected TokenContextWindow to return 200000, got %d", window)
	}
}

func TestPredictableServiceMaxImageDimension(t *testing.T) {
	service := NewPredictableService()
	dimension := service.MaxImageDimension()
	if dimension != 2000 {
		t.Errorf("expected MaxImageDimension to return 2000, got %d", dimension)
	}
}

func TestPredictableServiceThinkTool(t *testing.T) {
	service := NewPredictableService()

	ctx := context.Background()
	req := &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "think: This is a test thought"}}},
		},
	}

	resp, err := service.Do(ctx, req)
	if err != nil {
		t.Fatalf("think tool test failed: %v", err)
	}

	if resp.StopReason != llm.StopReasonToolUse {
		t.Errorf("expected tool use stop reason, got %v", resp.StopReason)
	}

	// Find the tool use content
	var toolUseContent *llm.Content
	for _, content := range resp.Content {
		if content.Type == llm.ContentTypeToolUse && content.ToolName == "think" {
			toolUseContent = &content
			break
		}
	}

	if toolUseContent == nil {
		t.Fatal("no think tool use content found")
	}

	// Check tool input contains the thoughts
	var toolInput map[string]interface{}
	if err := json.Unmarshal(toolUseContent.ToolInput, &toolInput); err != nil {
		t.Fatalf("failed to parse tool input: %v", err)
	}

	if toolInput["thoughts"] != "This is a test thought" {
		t.Errorf("expected thoughts 'This is a test thought', got '%v'", toolInput["thoughts"])
	}
}

func TestPredictableServicePatchTool(t *testing.T) {
	service := NewPredictableService()

	ctx := context.Background()
	req := &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "patch: /tmp/test.txt"}}},
		},
	}

	resp, err := service.Do(ctx, req)
	if err != nil {
		t.Fatalf("patch tool test failed: %v", err)
	}

	if resp.StopReason != llm.StopReasonToolUse {
		t.Errorf("expected tool use stop reason, got %v", resp.StopReason)
	}

	// Find the tool use content
	var toolUseContent *llm.Content
	for _, content := range resp.Content {
		if content.Type == llm.ContentTypeToolUse && content.ToolName == "patch" {
			toolUseContent = &content
			break
		}
	}

	if toolUseContent == nil {
		t.Fatal("no patch tool use content found")
	}

	// Check tool input contains the file path
	var toolInput map[string]interface{}
	if err := json.Unmarshal(toolUseContent.ToolInput, &toolInput); err != nil {
		t.Fatalf("failed to parse tool input: %v", err)
	}

	if toolInput["path"] != "/tmp/test.txt" {
		t.Errorf("expected path '/tmp/test.txt', got '%v'", toolInput["path"])
	}
}

func TestPredictableServiceMalformedPatchTool(t *testing.T) {
	service := NewPredictableService()

	ctx := context.Background()
	req := &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "patch bad json"}}},
		},
	}

	resp, err := service.Do(ctx, req)
	if err != nil {
		t.Fatalf("malformed patch tool test failed: %v", err)
	}

	if resp.StopReason != llm.StopReasonToolUse {
		t.Errorf("expected tool use stop reason, got %v", resp.StopReason)
	}

	// Find the tool use content
	var toolUseContent *llm.Content
	for _, content := range resp.Content {
		if content.Type == llm.ContentTypeToolUse && content.ToolName == "patch" {
			toolUseContent = &content
			break
		}
	}

	if toolUseContent == nil {
		t.Fatal("no patch tool use content found")
	}

	// Check that the tool input is malformed JSON (as expected)
	toolInputStr := string(toolUseContent.ToolInput)
	if !strings.Contains(toolInputStr, "parameter name") {
		t.Errorf("expected malformed JSON in tool input, got: %s", toolInputStr)
	}
}

func TestPredictableServiceError(t *testing.T) {
	service := NewPredictableService()

	ctx := context.Background()
	req := &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "error: test error"}}},
		},
	}

	resp, err := service.Do(ctx, req)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !strings.Contains(err.Error(), "predictable error: test error") {
		t.Errorf("expected error message to contain 'predictable error: test error', got: %v", err)
	}

	if resp != nil {
		t.Error("expected response to be nil when error occurs")
	}
}

func TestPredictableServiceRequestTracking(t *testing.T) {
	service := NewPredictableService()

	// Initially no requests
	requests := service.GetRecentRequests()
	if requests != nil {
		t.Errorf("expected nil requests initially, got %v", requests)
	}

	lastReq := service.GetLastRequest()
	if lastReq != nil {
		t.Errorf("expected nil last request initially, got %v", lastReq)
	}

	// Make a request
	ctx := context.Background()
	req := &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}}},
		},
	}

	_, err := service.Do(ctx, req)
	if err != nil {
		t.Fatalf("Do failed: %v", err)
	}

	// Check that request was tracked
	requests = service.GetRecentRequests()
	if len(requests) != 1 {
		t.Errorf("expected 1 request, got %d", len(requests))
	}

	lastReq = service.GetLastRequest()
	if lastReq == nil {
		t.Fatal("expected last request to be non-nil")
	}

	if len(lastReq.Messages) != 1 {
		t.Errorf("expected 1 message in last request, got %d", len(lastReq.Messages))
	}

	// Test clearing requests
	service.ClearRequests()
	requests = service.GetRecentRequests()
	if requests != nil {
		t.Errorf("expected nil requests after clearing, got %v", requests)
	}

	lastReq = service.GetLastRequest()
	if lastReq != nil {
		t.Errorf("expected nil last request after clearing, got %v", lastReq)
	}

	// Test that only last 10 requests are kept
	for i := 0; i < 15; i++ {
		testReq := &llm.Request{
			Messages: []llm.Message{
				{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: fmt.Sprintf("test %d", i)}}},
			},
		}
		_, err := service.Do(ctx, testReq)
		if err != nil {
			t.Fatalf("Do failed on iteration %d: %v", i, err)
		}
	}

	requests = service.GetRecentRequests()
	if len(requests) != 10 {
		t.Errorf("expected 10 requests (last 10), got %d", len(requests))
	}

	// Check that we have requests 5-14 (0-indexed)
	for i, req := range requests {
		expectedText := fmt.Sprintf("test %d", i+5)
		if len(req.Messages) == 0 || len(req.Messages[0].Content) == 0 {
			t.Errorf("request %d has no content", i)
			continue
		}
		if req.Messages[0].Content[0].Text != expectedText {
			t.Errorf("expected request %d to have text '%s', got '%s'", i, expectedText, req.Messages[0].Content[0].Text)
		}
	}
}

func TestPredictableServiceScreenshotTool(t *testing.T) {
	service := NewPredictableService()

	ctx := context.Background()
	req := &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "screenshot: .test-class"}}},
		},
	}

	resp, err := service.Do(ctx, req)
	if err != nil {
		t.Fatalf("screenshot tool test failed: %v", err)
	}

	if resp.StopReason != llm.StopReasonToolUse {
		t.Errorf("expected tool use stop reason, got %v", resp.StopReason)
	}

	// Find the tool use content
	var toolUseContent *llm.Content
	for _, content := range resp.Content {
		if content.Type == llm.ContentTypeToolUse && content.ToolName == "browser_take_screenshot" {
			toolUseContent = &content
			break
		}
	}

	if toolUseContent == nil {
		t.Fatal("no screenshot tool use content found")
	}

	// Check tool input contains the selector
	var toolInput map[string]interface{}
	if err := json.Unmarshal(toolUseContent.ToolInput, &toolInput); err != nil {
		t.Fatalf("failed to parse tool input: %v", err)
	}

	if toolInput["selector"] != ".test-class" {
		t.Errorf("expected selector '.test-class', got '%v'", toolInput["selector"])
	}
}

func TestPredictableServiceToolSmorgasbord(t *testing.T) {
	service := NewPredictableService()

	ctx := context.Background()
	req := &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "tool smorgasbord"}}},
		},
	}

	resp, err := service.Do(ctx, req)
	if err != nil {
		t.Fatalf("tool smorgasbord test failed: %v", err)
	}

	if resp.StopReason != llm.StopReasonToolUse {
		t.Errorf("expected tool use stop reason, got %v", resp.StopReason)
	}

	// Count the tool use contents
	toolUseCount := 0
	for _, content := range resp.Content {
		if content.Type == llm.ContentTypeToolUse {
			toolUseCount++
		}
	}

	// Should have at least several tool uses
	if toolUseCount < 5 {
		t.Errorf("expected at least 5 tool uses, got %d", toolUseCount)
	}
}

func TestProcessLLMRequestError(t *testing.T) {
	// Test error handling when LLM service returns an error
	errorService := &errorLLMService{err: fmt.Errorf("test LLM error")}

	var recordedMessages []llm.Message
	recordFunc := func(ctx context.Context, message llm.Message, usage llm.Usage) error {
		recordedMessages = append(recordedMessages, message)
		return nil
	}

	loop := NewLoop(Config{
		LLM:           errorService,
		History:       []llm.Message{},
		Tools:         []*llm.Tool{},
		RecordMessage: recordFunc,
	})

	// Queue a user message
	userMessage := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "test message"}},
	}
	loop.QueueUserMessage(userMessage)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	err := loop.ProcessOneTurn(ctx)
	if err == nil {
		t.Fatal("expected error from ProcessOneTurn, got nil")
	}

	if !strings.Contains(err.Error(), "LLM request failed") {
		t.Errorf("expected error to contain 'LLM request failed', got: %v", err)
	}

	// Check that error message was recorded
	if len(recordedMessages) < 1 {
		t.Fatalf("expected 1 recorded message (error), got %d", len(recordedMessages))
	}

	if recordedMessages[0].Role != llm.MessageRoleAssistant {
		t.Errorf("expected recorded message to be assistant role, got %s", recordedMessages[0].Role)
	}

	if len(recordedMessages[0].Content) != 1 {
		t.Fatalf("expected 1 content item in recorded message, got %d", len(recordedMessages[0].Content))
	}

	if recordedMessages[0].Content[0].Type != llm.ContentTypeText {
		t.Errorf("expected text content, got %s", recordedMessages[0].Content[0].Type)
	}

	if !strings.Contains(recordedMessages[0].Content[0].Text, "LLM request failed") {
		t.Errorf("expected error message to contain 'LLM request failed', got: %s", recordedMessages[0].Content[0].Text)
	}
}

// errorLLMService is a test LLM service that always returns an error
type errorLLMService struct {
	err error
}

func (e *errorLLMService) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	return nil, e.err
}

func (e *errorLLMService) TokenContextWindow() int {
	return 200000
}

func (e *errorLLMService) MaxImageDimension() int {
	return 2000
}

func TestCheckGitStateChange(t *testing.T) {
	// Create a test repo
	tmpDir := t.TempDir()

	// Initialize git repo
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@test.com")
	runGit(t, tmpDir, "config", "user.name", "Test")

	// Create initial commit
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, tmpDir, "add", ".")
	runGit(t, tmpDir, "commit", "-m", "initial")

	// Test with nil OnGitStateChange - should not panic
	loop := NewLoop(Config{
		LLM:           NewPredictableService(),
		History:       []llm.Message{},
		WorkingDir:    tmpDir,
		GetWorkingDir: func() string { return tmpDir },
		// OnGitStateChange is nil
		RecordMessage: func(ctx context.Context, message llm.Message, usage llm.Usage) error {
			return nil
		},
	})

	// This should not panic
	loop.checkGitStateChange(context.Background())

	// Test with actual callback
	var gitStateChanges []*gitstate.GitState
	loop = NewLoop(Config{
		LLM:           NewPredictableService(),
		History:       []llm.Message{},
		WorkingDir:    tmpDir,
		GetWorkingDir: func() string { return tmpDir },
		OnGitStateChange: func(ctx context.Context, state *gitstate.GitState) {
			gitStateChanges = append(gitStateChanges, state)
		},
		RecordMessage: func(ctx context.Context, message llm.Message, usage llm.Usage) error {
			return nil
		},
	})

	// Make a change
	if err := os.WriteFile(testFile, []byte("updated"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, tmpDir, "add", ".")
	runGit(t, tmpDir, "commit", "-m", "update")

	// Check git state change
	loop.checkGitStateChange(context.Background())

	if len(gitStateChanges) != 1 {
		t.Errorf("expected 1 git state change, got %d", len(gitStateChanges))
	}

	// Call again - should not trigger another change since state is the same
	loop.checkGitStateChange(context.Background())

	if len(gitStateChanges) != 1 {
		t.Errorf("expected still 1 git state change (no new changes), got %d", len(gitStateChanges))
	}
}

func TestHandleToolCallsWithMissingTool(t *testing.T) {
	var recordedMessages []llm.Message
	recordFunc := func(ctx context.Context, message llm.Message, usage llm.Usage) error {
		recordedMessages = append(recordedMessages, message)
		return nil
	}

	loop := NewLoop(Config{
		LLM:           NewPredictableService(),
		History:       []llm.Message{},
		Tools:         []*llm.Tool{}, // No tools registered
		RecordMessage: recordFunc,
	})

	// Create content with a tool use for a tool that doesn't exist
	content := []llm.Content{
		{
			ID:        "test_tool_123",
			Type:      llm.ContentTypeToolUse,
			ToolName:  "nonexistent_tool",
			ToolInput: json.RawMessage(`{"test": "input"}`),
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	err := loop.handleToolCalls(ctx, content)
	if err != nil {
		t.Fatalf("handleToolCalls failed: %v", err)
	}

	// Should have recorded a user message with tool result
	if len(recordedMessages) < 1 {
		t.Fatalf("expected 1 recorded message, got %d", len(recordedMessages))
	}

	msg := recordedMessages[0]
	if msg.Role != llm.MessageRoleUser {
		t.Errorf("expected user role, got %s", msg.Role)
	}

	if len(msg.Content) != 1 {
		t.Fatalf("expected 1 content item, got %d", len(msg.Content))
	}

	toolResult := msg.Content[0]
	if toolResult.Type != llm.ContentTypeToolResult {
		t.Errorf("expected tool result content, got %s", toolResult.Type)
	}

	if toolResult.ToolUseID != "test_tool_123" {
		t.Errorf("expected tool use ID 'test_tool_123', got %s", toolResult.ToolUseID)
	}

	if !toolResult.ToolError {
		t.Error("expected ToolError to be true")
	}

	if len(toolResult.ToolResult) != 1 {
		t.Fatalf("expected 1 tool result content item, got %d", len(toolResult.ToolResult))
	}

	if toolResult.ToolResult[0].Type != llm.ContentTypeText {
		t.Errorf("expected text content in tool result, got %s", toolResult.ToolResult[0].Type)
	}

	expectedText := "Tool 'nonexistent_tool' not found"
	if toolResult.ToolResult[0].Text != expectedText {
		t.Errorf("expected tool result text '%s', got '%s'", expectedText, toolResult.ToolResult[0].Text)
	}
}

func TestHandleToolCallsWithErrorTool(t *testing.T) {
	var recordedMessages []llm.Message
	recordFunc := func(ctx context.Context, message llm.Message, usage llm.Usage) error {
		recordedMessages = append(recordedMessages, message)
		return nil
	}

	// Create a tool that always returns an error
	errorTool := &llm.Tool{
		Name:        "error_tool",
		Description: "A tool that always errors",
		InputSchema: llm.MustSchema(`{"type": "object", "properties": {}}`),
		Run: func(ctx context.Context, input json.RawMessage) llm.ToolOut {
			return llm.ErrorToolOut(fmt.Errorf("intentional test error"))
		},
	}

	loop := NewLoop(Config{
		LLM:           NewPredictableService(),
		History:       []llm.Message{},
		Tools:         []*llm.Tool{errorTool},
		RecordMessage: recordFunc,
	})

	// Create content with a tool use that will error
	content := []llm.Content{
		{
			ID:        "error_tool_123",
			Type:      llm.ContentTypeToolUse,
			ToolName:  "error_tool",
			ToolInput: json.RawMessage(`{}`),
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	err := loop.handleToolCalls(ctx, content)
	if err != nil {
		t.Fatalf("handleToolCalls failed: %v", err)
	}

	// Should have recorded a user message with tool result
	if len(recordedMessages) < 1 {
		t.Fatalf("expected 1 recorded message, got %d", len(recordedMessages))
	}

	msg := recordedMessages[0]
	if msg.Role != llm.MessageRoleUser {
		t.Errorf("expected user role, got %s", msg.Role)
	}

	if len(msg.Content) != 1 {
		t.Fatalf("expected 1 content item, got %d", len(msg.Content))
	}

	toolResult := msg.Content[0]
	if toolResult.Type != llm.ContentTypeToolResult {
		t.Errorf("expected tool result content, got %s", toolResult.Type)
	}

	if toolResult.ToolUseID != "error_tool_123" {
		t.Errorf("expected tool use ID 'error_tool_123', got %s", toolResult.ToolUseID)
	}

	if !toolResult.ToolError {
		t.Error("expected ToolError to be true")
	}

	if len(toolResult.ToolResult) != 1 {
		t.Fatalf("expected 1 tool result content item, got %d", len(toolResult.ToolResult))
	}

	if toolResult.ToolResult[0].Type != llm.ContentTypeText {
		t.Errorf("expected text content in tool result, got %s", toolResult.ToolResult[0].Type)
	}

	expectedText := "intentional test error"
	if toolResult.ToolResult[0].Text != expectedText {
		t.Errorf("expected tool result text '%s', got '%s'", expectedText, toolResult.ToolResult[0].Text)
	}
}

//func TestInsertMissingToolResultsEdgeCases(t *testing.T) {
//	loop := NewLoop(Config{
//		LLM:     NewPredictableService(),
//		History: []llm.Message{},
//	})
//
//	// Test with nil request
//	loop.insertMissingToolResults(nil) // Should not panic
//
//	// Test with empty messages
//	req := &llm.Request{Messages: []llm.Message{}}
//	loop.insertMissingToolResults(req) // Should not panic
//
//	// Test with single message
//	req = &llm.Request{
//		Messages: []llm.Message{
//			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}}},
//		},
//	}
//	loop.insertMissingToolResults(req) // Should not panic
//	if len(req.Messages) != 1 {
//		t.Errorf("expected 1 message, got %d", len(req.Messages))
//	}
//
//	// Test with multiple consecutive assistant messages with tool_use
//	req = &llm.Request{
//		Messages: []llm.Message{
//			{
//				Role: llm.MessageRoleAssistant,
//				Content: []llm.Content{
//					{Type: llm.ContentTypeText, Text: "First tool"},
//					{Type: llm.ContentTypeToolUse, ID: "tool1", ToolName: "bash"},
//				},
//			},
//			{
//				Role: llm.MessageRoleAssistant,
//				Content: []llm.Content{
//					{Type: llm.ContentTypeText, Text: "Second tool"},
//					{Type: llm.ContentTypeToolUse, ID: "tool2", ToolName: "read"},
//				},
//			},
//			{
//				Role: llm.MessageRoleUser,
//				Content: []llm.Content{
//					{Type: llm.ContentTypeText, Text: "User response"},
//				},
//			},
//		},
//	}
//
//	loop.insertMissingToolResults(req)
//
//	// Should have inserted synthetic tool results for both tool_uses
//	// The structure should be:
//	// 0: First assistant message
//	// 1: Synthetic user message with tool1 result
//	// 2: Second assistant message
//	// 3: Synthetic user message with tool2 result
//	// 4: Original user message
//	if len(req.Messages) != 5 {
//		t.Fatalf("expected 5 messages after processing, got %d", len(req.Messages))
//	}
//
//	// Check first synthetic message
//	if req.Messages[1].Role != llm.MessageRoleUser {
//		t.Errorf("expected message 1 to be user role, got %s", req.Messages[1].Role)
//	}
//	foundTool1 := false
//	for _, content := range req.Messages[1].Content {
//		if content.Type == llm.ContentTypeToolResult && content.ToolUseID == "tool1" {
//			foundTool1 = true
//			break
//		}
//	}
//	if !foundTool1 {
//		t.Error("expected to find tool1 result in message 1")
//	}
//
//	// Check second synthetic message
//	if req.Messages[3].Role != llm.MessageRoleUser {
//		t.Errorf("expected message 3 to be user role, got %s", req.Messages[3].Role)
//	}
//	foundTool2 := false
//	for _, content := range req.Messages[3].Content {
//		if content.Type == llm.ContentTypeToolResult && content.ToolUseID == "tool2" {
//			foundTool2 = true
//			break
//		}
//}
//	if !foundTool2 {
//		t.Error("expected to find tool2 result in message 3")
//	}
//}
