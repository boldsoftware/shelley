package gem

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/llm"
)

// TestGeminiThinkingIntegration tests thinking display with real Gemini API calls.
// Skipped if GEMINI_API_KEY is not set.
func TestGeminiThinkingIntegration(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY not set, skipping integration test")
	}

	service := &Service{
		APIKey: apiKey,
		Model:  "gemini-3-pro-preview", // Gemini 3 supports thinking with tools
	}

	// Define a simple bash tool to trigger thinking with ThoughtSignature
	bashTool := &llm.Tool{
		Name:        "bash",
		Description: "Execute a bash command",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"command": {
					"type": "string",
					"description": "The bash command to execute"
				}
			},
			"required": ["command"]
		}`),
	}

	t.Run("tool_use_with_thought_signature", func(t *testing.T) {
		req := &llm.Request{
			Messages: []llm.Message{
				{
					Role: llm.MessageRoleUser,
					Content: []llm.Content{
						{
							Type: llm.ContentTypeText,
							Text: "List the files in the current directory using the bash tool",
						},
					},
				},
			},
			Tools: []*llm.Tool{bashTool},
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		resp, err := service.Do(ctx, req)
		if err != nil {
			t.Fatalf("Failed to get response: %v", err)
		}

		// Verify we got a response
		if len(resp.Content) == 0 {
			t.Fatal("Expected at least one content block")
		}

		// Look for tool use with signature
		foundToolUse := false
		foundSignature := false

		for i, content := range resp.Content {
			t.Logf("Content block %d: Type=%s, Signature=%q", i, content.Type, content.Signature)

			if content.Type == llm.ContentTypeToolUse {
				foundToolUse = true
				t.Logf("  Tool: %s", content.ToolName)
				t.Logf("  Input: %s", string(content.ToolInput))

				// Gemini 3 should provide ThoughtSignature for tool calls
				if content.Signature != "" {
					foundSignature = true
					t.Logf("  ThoughtSignature present: %d bytes", len(content.Signature))
				}
			} else if content.Type == llm.ContentTypeThinking {
				t.Logf("  Thinking content detected!")
				t.Logf("  Thinking text: %s", truncateForLog(content.Thinking, 200))
				t.Logf("  Signature: %q", content.Signature)
			} else if content.Type == llm.ContentTypeText {
				t.Logf("  Text: %s", truncateForLog(content.Text, 200))
			}
		}

		if !foundToolUse {
			t.Error("Expected to find a tool use content block")
		}

		// Note: ThoughtSignature presence depends on the model version and configuration
		// So we log it but don't fail if it's missing
		if !foundSignature {
			t.Log("Note: No ThoughtSignature found in this response (model may not provide it)")
		}
	})

	t.Run("text_with_thinking_if_available", func(t *testing.T) {
		// Some Gemini models may return thinking as separate text blocks with signatures
		req := &llm.Request{
			Messages: []llm.Message{
				{
					Role: llm.MessageRoleUser,
					Content: []llm.Content{
						{
							Type: llm.ContentTypeText,
							Text: "What is 15 + 27 * 3? Show your thinking step by step.",
						},
					},
				},
			},
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		resp, err := service.Do(ctx, req)
		if err != nil {
			t.Fatalf("Failed to get response: %v", err)
		}

		if len(resp.Content) == 0 {
			t.Fatal("Expected at least one content block")
		}

		// Log what we got
		for i, content := range resp.Content {
			t.Logf("Content block %d: Type=%s", i, content.Type)

			if content.Type == llm.ContentTypeThinking {
				t.Logf("  ✓ Thinking content found!")
				t.Logf("  Thinking: %s", truncateForLog(content.Thinking, 200))
				t.Logf("  Signature: %q", content.Signature)
			} else if content.Type == llm.ContentTypeText {
				t.Logf("  Text: %s", truncateForLog(content.Text, 200))
				if content.Signature != "" {
					t.Logf("  Note: Text has signature (should be thinking?): %q", content.Signature)
				}
			}
		}

		// Check usage metadata to see if thinking was used
		t.Logf("Usage: input=%d, output=%d", resp.Usage.InputTokens, resp.Usage.OutputTokens)
	})

	t.Run("roundtrip_thinking_preservation", func(t *testing.T) {
		// First, make a request with tools to get a response with ThoughtSignature
		bashTool := &llm.Tool{
			Name:        "echo",
			Description: "Echo a message",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"message": {
						"type": "string",
						"description": "The message to echo"
					}
				},
				"required": ["message"]
			}`),
		}

		req1 := &llm.Request{
			Messages: []llm.Message{
				{
					Role: llm.MessageRoleUser,
					Content: []llm.Content{
						{
							Type: llm.ContentTypeText,
							Text: "Echo 'hello world' using the echo tool",
						},
					},
				},
			},
			Tools: []*llm.Tool{bashTool},
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		resp1, err := service.Do(ctx, req1)
		if err != nil {
			t.Fatalf("Failed to get first response: %v", err)
		}

		// Now create a second request that includes the first response in history
		// This tests that thinking content with signatures is preserved
		req2 := &llm.Request{
			Messages: []llm.Message{
				{
					Role: llm.MessageRoleUser,
					Content: []llm.Content{
						{
							Type: llm.ContentTypeText,
							Text: "Echo 'hello world' using the echo tool",
						},
					},
				},
				{
					Role:    llm.MessageRoleAssistant,
					Content: resp1.Content,
				},
				{
					Role: llm.MessageRoleUser,
					Content: []llm.Content{
						{
							Type:       llm.ContentTypeToolResult,
							ToolUseID:  resp1.Content[0].ID,
							ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: "hello world"}},
						},
					},
				},
			},
			Tools: []*llm.Tool{bashTool},
		}

		ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel2()

		resp2, err := service.Do(ctx2, req2)
		if err != nil {
			t.Fatalf("Failed to get second response (roundtrip): %v", err)
		}

		// If we got here without error, the roundtrip worked
		t.Logf("Roundtrip successful - conversation history preserved")
		t.Logf("Second response has %d content blocks", len(resp2.Content))
	})
}

// TestGemini3ModelsIntegration tests with multiple Gemini 3 models if available
func TestGemini3ModelsIntegration(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY not set, skipping integration test")
	}

	models := []string{
		"gemini-3-pro-preview",
		"gemini-3-flash-preview",
		"gemini-3.1-pro-preview",
	}

	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			service := &Service{
				APIKey: apiKey,
				Model:  model,
			}

			req := &llm.Request{
				Messages: []llm.Message{
					{
						Role: llm.MessageRoleUser,
						Content: []llm.Content{
							{
								Type: llm.ContentTypeText,
								Text: "Hello, what model are you?",
							},
						},
					},
				},
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			resp, err := service.Do(ctx, req)
			if err != nil {
				t.Logf("Model %s returned error (may not be available): %v", model, err)
				t.Skip()
				return
			}

			t.Logf("Model %s responded successfully", model)
			t.Logf("  Content blocks: %d", len(resp.Content))

			for i, content := range resp.Content {
				if content.Type == llm.ContentTypeThinking {
					t.Logf("  Block %d: THINKING (with signature: %v)", i, content.Signature != "")
				} else {
					t.Logf("  Block %d: %s", i, content.Type)
				}
			}
		})
	}
}

func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// TestGeminiImageIntegration verifies that image content is sent to Gemini
// via inlineData parts. Skipped if GEMINI_API_KEY is not set.
func TestGeminiImageIntegration(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY not set, skipping integration test")
	}

	// A 64x64 solid red PNG, base64-encoded.
	const redPNG = "iVBORw0KGgoAAAANSUhEUgAAAEAAAABACAIAAAAlC+aJAAAAb0lEQVR4nO3PAQkAAAyEwO9feoshgnABdLep8QUNyPEFDcjxBQ3I8QUNyPEFDcjxBQ3I8QUNyPEFDcjxBQ3I8QUNyPEFDcjxBQ3I8QUNyPEFDcjxBQ3I8QUNyPEFDcjxBQ3I8QUNyPEFDcjxBQ3I8QUNyPEFDcjxBQ3IPanc8OLDQitxAAAAAElFTkSuQmCC"

	service := &Service{APIKey: apiKey, Model: "gemini-2.5-flash"}
	req := &llm.Request{
		Messages: []llm.Message{{
			Role: llm.MessageRoleUser,
			Content: []llm.Content{
				{Type: llm.ContentTypeText, Text: "What single color fills this image? Reply with one word."},
				{Type: llm.ContentTypeText, MediaType: "image/png", Data: redPNG},
			},
		}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := service.Do(ctx, req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	var text string
	for _, c := range resp.Content {
		if c.Type == llm.ContentTypeText {
			text += c.Text
		}
	}
	t.Logf("Gemini reply: %q", text)
	if !strings.Contains(strings.ToLower(text), "red") {
		t.Errorf("expected reply to mention 'red'; got %q", text)
	}
}
