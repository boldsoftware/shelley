package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/models"
)

func TestProviderSnapshotWarmLoopLifecycle(t *testing.T) {
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	row := db.ModelProvider{ProviderID: "work", ProviderType: "openai-responses", Endpoint: "https://fixture.invalid/v1", APIKey: "old", Models: []db.ProviderModel{{ID: "gpt-6.1-sol"}}}
	if err := database.CreateModelProvider(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	registry, err := models.NewManager(&models.Config{DB: database})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.RefreshProviders(); err != nil {
		t.Fatal(err)
	}
	id := models.ProviderRouteID(row.ProviderID, row.Models[0].ID)
	s := NewServer(database, registry, claudetool.ToolSetConfig{EnableBrowser: false}, slog.New(slog.NewTextHandler(io.Discard, nil)), false, id, "")
	s.hooksDir = t.TempDir()
	conversation, err := database.CreateConversation(t.Context(), nil, true, nil, &id, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cm, err := s.getOrCreateConversationManager(t.Context(), conversation.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopActiveConversationLoops(s) })
	service, err := registry.GetService(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := cm.ensureLoop(service, id); err != nil {
		t.Fatal(err)
	}
	original := cm.loop
	update := func() {
		t.Helper()
		old, err := database.GetModelProvider(t.Context(), row.ProviderID)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.UpdateModelProvider(t.Context(), row, old.Version); err != nil {
			t.Fatal(err)
		}
		if err := registry.RefreshProviders(); err != nil {
			t.Fatal(err)
		}
		if err := cm.ensureLoop(service, id); err != nil {
			t.Fatal(err)
		}
	}
	row.DisplayName = "Renamed"
	row.Models = append(row.Models, db.ProviderModel{ID: "gpt-5.6-sol"})
	update()
	if cm.loop != original {
		t.Fatal("label or sibling model edit replaced the loop and its browser")
	}
	row.Models = nil
	update()
	if cm.loop != original {
		t.Fatal("unlisting the active model replaced its loop")
	}
	row.APIKey = "new"
	update()
	if cm.loop == original || cm.loopSourceID != row.ProviderID {
		t.Fatal("key rotation did not reconcile the same account")
	}
	if err := database.DeleteModelProvider(t.Context(), row.ProviderID); err != nil {
		t.Fatal(err)
	}
	if err := cm.ensureLoop(service, id); err == nil {
		t.Fatal("deleted provider reused a warm loop")
	}
	if err := registry.RefreshProviders(); err != nil {
		t.Fatal(err)
	}
	_, err = database.AppendQueuedMessage(t.Context(), conversation.ConversationID, db.QueuedMessage{
		ID: "queued", Llm: []byte(`{"Role":0,"Content":[{"Type":2,"Text":"do not lose me"}]}`), CreatedAt: time.Now(), Model: id,
	})
	if err != nil {
		t.Fatal(err)
	}
	<-cm.drainPendingMessages()
	if pending := queuedMessages(t, database, conversation.ConversationID); len(pending) != 1 {
		t.Fatalf("deleted account lost queued work: %+v", pending)
	}
	if _, err := database.CreateMessage(t.Context(), db.CreateMessageParams{
		ConversationID: conversation.ConversationID, Type: db.MessageTypeError,
		LLMData:  llm.Message{Role: llm.MessageRoleAssistant, EndOfTurn: true, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "retryable fixture error"}}},
		UserData: map[string]any{"retryable": true},
	}); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	s.handleRetryConversation(response, httptest.NewRequest(http.MethodPost, "/retry", strings.NewReader(`{}`)), conversation.ConversationID)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "Saved provider") {
		t.Fatalf("deleted account retry: %d %s", response.Code, response.Body.String())
	}
}
