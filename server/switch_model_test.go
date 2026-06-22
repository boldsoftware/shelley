package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	claudetool "shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
	"shelley.exe.dev/models"
)

// multiModelTestManager is an LLMProvider that knows about two models.
type multiModelTestManager struct {
	services map[string]llm.Service
}

func (m *multiModelTestManager) GetService(modelID string) (llm.Service, error) {
	svc, ok := m.services[modelID]
	if !ok {
		return nil, fmt.Errorf("unsupported model: %s", modelID)
	}
	return svc, nil
}

func (m *multiModelTestManager) GetAvailableModels() []string {
	out := make([]string, 0, len(m.services))
	for k := range m.services {
		out = append(out, k)
	}
	return out
}

func (m *multiModelTestManager) HasModel(modelID string) bool {
	_, ok := m.services[modelID]
	return ok
}

func (m *multiModelTestManager) GetModelInfo(modelID string) *models.ModelInfo {
	return nil
}

func (m *multiModelTestManager) RefreshCustomModels() error {
	return nil
}

func TestSwitchModel(t *testing.T) {
	t.Parallel()

	// Use the standard test server (single-model) for basic switch validation.
	server, database, _ := newTestServer(t)

	// Create a conversation.
	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	conversationID := conversation.ConversationID

	// Send a first message to pin the model.
	w := chatPost(t, server, conversationID, ChatRequest{Message: "echo: hello", Model: "predictable"})
	if w.Code != http.StatusAccepted {
		t.Fatalf("first chat: expected 202, got %d: %s", w.Code, w.Body.String())
	}

	// Wait for the loop to record an assistant response so we know the model is pinned.
	waitForMessages(t, database, conversationID, 3, 5*time.Second) // system + user + agent

	// Verify the model is pinned.
	conv, err := database.GetConversationByID(context.Background(), conversationID)
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if conv.Model == nil || *conv.Model != "predictable" {
		t.Fatalf("expected model 'predictable', got %v", conv.Model)
	}

	// Without switching, sending a chat with a different model should fail.
	w = chatPost(t, server, conversationID, ChatRequest{Message: "echo: world", Model: "some-other-model"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("mismatched model: expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// Now switch model via the handler. The testLLMManager returns the same
	// service for any model name, so the switch is valid.
	switchBody, _ := json.Marshal(SwitchModelRequest{Model: "some-other-model"})
	req := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/switch-model", strings.NewReader(string(switchBody)))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.handleSwitchModel(w, req, conversationID)

	if w.Code != http.StatusOK {
		t.Fatalf("switch model: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var switchResp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&switchResp)
	if switchResp["changed"] != true {
		t.Fatalf("expected changed=true, got %v", switchResp)
	}

	// Verify the DB model was updated.
	conv, err = database.GetConversationByID(context.Background(), conversationID)
	if err != nil {
		t.Fatalf("get conversation after switch: %v", err)
	}
	if conv.Model == nil || *conv.Model != "some-other-model" {
		t.Fatalf("expected model 'some-other-model', got %v", conv.Model)
	}

	// Now sending a message with the new model should work.
	w = chatPost(t, server, conversationID, ChatRequest{Message: "echo: after switch", Model: "some-other-model"})
	if w.Code != http.StatusAccepted {
		t.Fatalf("chat after switch: expected 202, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSwitchModelNoOp(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	conversationID := conversation.ConversationID

	// Pin the model.
	w := chatPost(t, server, conversationID, ChatRequest{Message: "echo: hi", Model: "predictable"})
	if w.Code != http.StatusAccepted {
		t.Fatalf("first chat: expected 202, got %d", w.Code)
	}
	waitForMessages(t, database, conversationID, 3, 5*time.Second)

	// Switch to the same model = no-op.
	switchBody, _ := json.Marshal(SwitchModelRequest{Model: "predictable"})
	req := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/switch-model", strings.NewReader(string(switchBody)))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.handleSwitchModel(w, req, conversationID)

	if w.Code != http.StatusOK {
		t.Fatalf("no-op switch: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["changed"] != false {
		t.Fatalf("expected changed=false for no-op, got %v", resp)
	}
}

func TestSwitchModelInvalidModel(t *testing.T) {
	t.Parallel()

	// Create a server with a multi-model manager that only knows about "model-a".
	testDB, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ps := loop.NewPredictableService()
	mgr := &multiModelTestManager{
		services: map[string]llm.Service{"model-a": ps},
	}
	svr := NewServer(testDB, mgr,
		claudetool.ToolSetConfig{EnableBrowser: false},
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError})),
		true, "model-a", "")
	svr.hooksDir = t.TempDir()

	conversation, err := testDB.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	// Try to switch to an unknown model.
	switchBody, _ := json.Marshal(SwitchModelRequest{Model: "nonexistent-model"})
	req := httptest.NewRequest("POST", "/", strings.NewReader(string(switchBody)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	svr.handleSwitchModel(w, req, conversation.ConversationID)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid model: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSwitchModelPreservesHistory(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	conversationID := conversation.ConversationID

	// Send a message and wait for the response.
	w := chatPost(t, server, conversationID, ChatRequest{Message: "echo: first", Model: "predictable"})
	if w.Code != http.StatusAccepted {
		t.Fatalf("first chat: expected 202, got %d", w.Code)
	}
	waitForMessages(t, database, conversationID, 3, 5*time.Second)

	// Count messages before switch.
	msgsBefore := listAllMessages(t, database, conversationID)
	countBefore := len(msgsBefore)

	// Switch model.
	switchBody, _ := json.Marshal(SwitchModelRequest{Model: "some-other-model"})
	req := httptest.NewRequest("POST", "/", strings.NewReader(string(switchBody)))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.handleSwitchModel(w, req, conversationID)
	if w.Code != http.StatusOK {
		t.Fatalf("switch: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Count messages after switch — should have exactly 1 more (the warning).
	msgsAfter := listAllMessages(t, database, conversationID)
	// The switch inserts a warning message.
	if len(msgsAfter) != countBefore+1 {
		t.Fatalf("expected %d messages after switch (was %d + 1 warning), got %d",
			countBefore+1, countBefore, len(msgsAfter))
	}

	// The generation should NOT have changed.
	conv, _ := database.GetConversationByID(context.Background(), conversationID)
	if conv.CurrentGeneration != conversation.CurrentGeneration {
		t.Fatalf("expected generation %d unchanged, got %d",
			conversation.CurrentGeneration, conv.CurrentGeneration)
	}

	// Verify the warning message content.
	lastMsg := msgsAfter[len(msgsAfter)-1]
	if lastMsg.Type != "warning" {
		t.Fatalf("expected last message type 'warning', got %q", lastMsg.Type)
	}
}

// waitForMessages waits until the conversation has at least n messages.
func waitForMessages(t *testing.T, database *db.DB, conversationID string, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		msgs := listAllMessages(t, database, conversationID)
		if len(msgs) >= n {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	msgs := listAllMessages(t, database, conversationID)
	t.Fatalf("timed out waiting for %d messages, have %d", n, len(msgs))
}

func listAllMessages(t *testing.T, database *db.DB, conversationID string) []generated.Message {
	t.Helper()
	var messages []generated.Message
	err := database.Queries(context.Background(), func(q *generated.Queries) error {
		var qerr error
		messages, qerr = q.ListMessages(context.Background(), conversationID)
		return qerr
	})
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	return messages
}
