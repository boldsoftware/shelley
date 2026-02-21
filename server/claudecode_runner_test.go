package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/ant"
)

func TestClaudeCodeRunner_RunSubagent(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	toolSetConfig := claudetool.ToolSetConfig{
		WorkingDir:    t.TempDir(),
		EnableBrowser: false,
	}

	service := &ant.Service{
		APIKey: os.Getenv("ANTHROPIC_API_KEY"),
	}
	llmManager := &claudeLLMManager{service: service}

	// Create server with claude code subagent enabled
	server := NewServer(database, llmManager, toolSetConfig, logger, true, "", "claude", "", nil, true)

	// We need a real HTTP server for the claude CLI to connect back via MCP
	mux := http.NewServeMux()
	server.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Parse the port from ts.URL (e.g. "http://127.0.0.1:54321")
	parts := strings.Split(ts.URL, ":")
	server.listenAddr = ":" + parts[len(parts)-1]

	// Create a conversation using the HTTP handler
	chatReq := ChatRequest{
		Message: "init",
		Model:   "claude",
		Cwd:     t.TempDir(),
	}
	chatBody, _ := json.Marshal(chatReq)
	req := httptest.NewRequest("POST", "/api/conversations/new", strings.NewReader(string(chatBody)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.handleNewConversation(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("Failed to create conversation: %s", w.Body.String())
	}
	var resp struct {
		ConversationID string `json:"conversation_id"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	convID := resp.ConversationID

	runner := NewClaudeCodeRunner(server)

	// Run subagent using wait=true so we can get the final result
	prompt := "Please run 'echo Hello World from Subagent' and tell me what the output was."
	ctx := context.Background()
	result, err := runner.RunSubagent(ctx, convID, prompt, true, 30*time.Second)
	if err != nil {
		t.Fatalf("RunSubagent failed: %v", err)
	}

	t.Logf("Final Result: %s", result)

	if !strings.Contains(result, "Hello World from Subagent") {
		t.Errorf("Expected result to contain 'Hello World from Subagent', got %q", result)
	}

	// Verify messages were recorded
	var messages []generated.Message
	err = database.Queries(ctx, func(q *generated.Queries) error {
		var qerr error
		messages, qerr = q.ListMessages(ctx, convID)
		return qerr
	})
	if err != nil {
		t.Fatalf("Failed to list messages: %v", err)
	}

	if len(messages) < 2 {
		t.Errorf("Expected at least 2 messages (user, agent), got %d", len(messages))
	}

	for _, msg := range messages {
		var llmMsg llm.Message
		json.Unmarshal([]byte(*msg.LlmData), &llmMsg)
		t.Logf("Message Type: %s, Role: %s, Content Items: %d", msg.Type, llmMsg.Role, len(llmMsg.Content))
		for i, c := range llmMsg.Content {
			t.Logf("  Content %d: Type: %s", i, c.Type)
			if c.Type == llm.ContentTypeToolUse {
				t.Logf("    ToolName: %s", c.ToolName)
			}
		}
	}
}
