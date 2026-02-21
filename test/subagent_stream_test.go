package test

import (
	"bufio"
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
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
	"shelley.exe.dev/models"
	"shelley.exe.dev/server"
)

// StreamResponse matches server.StreamResponse for testing
type StreamResponse struct {
	Messages               []json.RawMessage       `json:"messages"`
	Conversation           generated.Conversation  `json:"conversation"`
	ConversationState      *ConversationState      `json:"conversation_state,omitempty"`
	ConversationListUpdate *ConversationListUpdate `json:"conversation_list_update,omitempty"`
	Heartbeat              bool                    `json:"heartbeat,omitempty"`
}

type ConversationState struct {
	ConversationID string `json:"conversation_id"`
	Working        bool   `json:"working"`
	Model          string `json:"model,omitempty"`
}

type ConversationListUpdate struct {
	Type           string                  `json:"type"`
	Conversation   *generated.Conversation `json:"conversation,omitempty"`
	ConversationID string                  `json:"conversation_id,omitempty"`
}

type fakeLLMManager struct {
	service *loop.PredictableService
}

func (m *fakeLLMManager) GetService(modelID string) (llm.Service, error) {
	return m.service, nil
}

func (m *fakeLLMManager) GetAvailableModels() []string {
	return []string{"predictable"}
}

func (m *fakeLLMManager) HasModel(modelID string) bool {
	return modelID == "predictable"
}

func (m *fakeLLMManager) GetModelInfo(modelID string) *models.ModelInfo {
	return nil
}

func (m *fakeLLMManager) RefreshCustomModels() error {
	return nil
}

func setupTestServerForSubagent(t *testing.T) (*server.Server, *db.DB, *httptest.Server, *loop.PredictableService) {
	t.Helper()

	// Create temporary database
	tempDB := t.TempDir() + "/test.db"
	database, err := db.New(db.Config{DSN: tempDB})
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	// Run migrations
	if err := database.Migrate(context.Background()); err != nil {
		t.Fatalf("Failed to migrate database: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	// Use predictable model
	predictableService := loop.NewPredictableService()
	llmManager := &fakeLLMManager{service: predictableService}

	toolSetConfig := claudetool.ToolSetConfig{
		WorkingDir:    t.TempDir(),
		EnableBrowser: false,
	}

	svr := server.NewServer(database, llmManager, toolSetConfig, logger, true, "", "predictable", "", nil)

	mux := http.NewServeMux()
	svr.RegisterRoutes(mux)
	testServer := httptest.NewServer(mux)
	t.Cleanup(testServer.Close)

	return svr, database, testServer, predictableService
}

// readSSEEvent reads a single SSE event from the response body with a timeout
func readSSEEventWithTimeout(reader *bufio.Reader, timeout time.Duration) (*StreamResponse, error) {
	type result struct {
		resp *StreamResponse
		err  error
	}
	ch := make(chan result, 1)

	go func() {
		var dataLines []string
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				ch <- result{nil, err}
				return
			}
			line = strings.TrimSpace(line)

			if line == "" && len(dataLines) > 0 {
				// End of event
				break
			}

			if strings.HasPrefix(line, "data: ") {
				dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
			}
		}

		if len(dataLines) == 0 {
			ch <- result{nil, nil}
			return
		}

		data := strings.Join(dataLines, "\n")
		var response StreamResponse
		if err := json.Unmarshal([]byte(data), &response); err != nil {
			ch <- result{nil, err}
			return
		}
		ch <- result{&response, nil}
	}()

	select {
	case r := <-ch:
		return r.resp, r.err
	case <-time.After(timeout):
		return nil, context.DeadlineExceeded
	}
}

// TestSubagentNotificationViaStream tests that when RunSubagent is called,
// the subagent conversation is properly notified to all SSE streams.
func TestSubagentNotificationViaStream(t *testing.T) {
	svr, database, testServer, _ := setupTestServerForSubagent(t)

	ctx := context.Background()

	// Create parent conversation
	parentSlug := "parent-convo"
	parentConv, err := database.CreateConversation(ctx, &parentSlug, true, nil, nil)
	if err != nil {
		t.Fatalf("Failed to create parent conversation: %v", err)
	}

	// Start streaming from parent conversation
	streamURL := testServer.URL + "/api/conversation/" + parentConv.ConversationID + "/stream"
	resp, err := http.Get(streamURL)
	if err != nil {
		t.Fatalf("Failed to connect to stream: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)

	// Read initial event (should be the conversation state)
	initialEvent, err := readSSEEventWithTimeout(reader, 2*time.Second)
	if err != nil {
		t.Fatalf("Failed to read initial SSE event: %v", err)
	}
	if initialEvent == nil {
		t.Fatal("Expected initial event")
	}
	t.Logf("Initial event: conversation_id=%s, has_state=%v",
		initialEvent.Conversation.ConversationID,
		initialEvent.ConversationState != nil)

	// Create a subagent conversation directly in DB (simulating what SubagentTool.Run does)
	subSlug := "sub-worker"
	subConv, err := database.CreateSubagentConversation(ctx, subSlug, parentConv.ConversationID, nil)
	if err != nil {
		t.Fatalf("Failed to create subagent conversation: %v", err)
	}
	t.Logf("Created subagent: id=%s, slug=%s, parent=%s",
		subConv.ConversationID, *subConv.Slug, *subConv.ParentConversationID)

	// Now call RunSubagent (what the subagent tool does after creating the conversation)
	// This should trigger the notification to all SSE streams
	subagentRunner := server.NewSubagentRunner(svr)
	go func() {
		// Call RunSubagent with wait=false so it returns quickly
		subagentRunner.RunSubagent(ctx, subConv.ConversationID, "Test prompt", false, 10*time.Second)
	}()

	// Wait for notification
	var receivedSubagentUpdate bool
	var receivedUpdate *ConversationListUpdate

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		event, err := readSSEEventWithTimeout(reader, 500*time.Millisecond)
		if err == context.DeadlineExceeded {
			continue // Keep waiting
		}
		if err != nil {
			t.Logf("Error reading event: %v", err)
			break
		}
		if event == nil {
			continue
		}

		t.Logf("Received event: has_list_update=%v, has_state=%v, heartbeat=%v",
			event.ConversationListUpdate != nil,
			event.ConversationState != nil,
			event.Heartbeat)

		if event.ConversationListUpdate != nil {
			update := event.ConversationListUpdate
			t.Logf("List update: type=%s", update.Type)
			if update.Conversation != nil {
				t.Logf("  conversation_id=%s, parent=%v, slug=%v",
					update.Conversation.ConversationID,
					update.Conversation.ParentConversationID,
					update.Conversation.Slug)
				if update.Conversation.ConversationID == subConv.ConversationID {
					receivedSubagentUpdate = true
					receivedUpdate = update
					break
				}
			}
		}
	}

	// Verify we received the notification
	if !receivedSubagentUpdate {
		t.Error("Expected to receive subagent update notification via SSE stream when RunSubagent is called")
	} else {
		t.Logf("SUCCESS: Received subagent update: type=%s, slug=%v", receivedUpdate.Type, receivedUpdate.Conversation.Slug)
	}
}

// TestSubagentWorkingStateNotification tests that subagent working state changes
// are properly notified via the SSE stream.
func TestSubagentWorkingStateNotification(t *testing.T) {
	// This test would verify that when a subagent starts/stops working,
	// the parent conversation's stream receives a ConversationState update.
	// Currently we just document this should work via publishConversationState.
	t.Skip("Skipping - requires more infrastructure to trigger working state changes")
}
