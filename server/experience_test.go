package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"shelley.exe.dev/db"
)

func TestExperienceManagementAPI(t *testing.T) {
	h := NewTestHarness(t)
	mux := http.NewServeMux()
	h.server.RegisterRoutes(mux)
	ctx := context.Background()
	conversation, err := h.db.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()

	request := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var payload bytes.Buffer
		if body != nil {
			if err := json.NewEncoder(&payload).Encode(body); err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(method, path, &payload)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		return response
	}

	created := request(http.MethodPost, "/api/experience/memories", map[string]any{
		"cwd": cwd, "conversation_id": conversation.ConversationID, "kind": "decision",
		"title": "Database", "content": "Use SQLite", "confidence": .9,
	})
	if created.Code != http.StatusOK {
		t.Fatalf("create memory: %d %s", created.Code, created.Body.String())
	}
	var memory db.MemoryItem
	if err := json.Unmarshal(created.Body.Bytes(), &memory); err != nil {
		t.Fatal(err)
	}

	listed := request(http.MethodGet, "/api/experience/memories?cwd="+url.QueryEscape(cwd), nil)
	if listed.Code != http.StatusOK || !bytes.Contains(listed.Body.Bytes(), []byte("Database")) {
		t.Fatalf("list memories: %d %s", listed.Code, listed.Body.String())
	}
	updated := request(http.MethodPut, "/api/experience/memories/"+memory.ID+"?cwd="+url.QueryEscape(cwd), map[string]any{
		"kind": "lesson", "title": "Database", "content": "Use SQLite WAL", "confidence": 1,
	})
	if updated.Code != http.StatusNoContent {
		t.Fatalf("update memory: %d %s", updated.Code, updated.Body.String())
	}

	checkpoint := request(http.MethodPost, "/api/experience/journal", map[string]any{
		"conversation_id": conversation.ConversationID, "summary": "ready", "state": map[string]any{"next": "ship"},
	})
	if checkpoint.Code != http.StatusOK {
		t.Fatalf("create checkpoint: %d %s", checkpoint.Code, checkpoint.Body.String())
	}
	journal := request(http.MethodGet, "/api/experience/journal?conversation_id="+conversation.ConversationID, nil)
	if journal.Code != http.StatusOK || !bytes.Contains(journal.Body.Bytes(), []byte("ready")) {
		t.Fatalf("list journal: %d %s", journal.Code, journal.Body.String())
	}

	raw := []byte(`[{"scope":"project","kind":"lesson","title":"Verify","content":"Run tests","confidence":1}]`)
	if _, err := h.db.ConsolidateDreamJSON(ctx, conversation.ConversationID, cwd, "verified", raw); err != nil {
		t.Fatal(err)
	}
	dreams := request(http.MethodGet, "/api/experience/dreams?cwd="+url.QueryEscape(cwd), nil)
	if dreams.Code != http.StatusOK || !bytes.Contains(dreams.Body.Bytes(), []byte("verified")) {
		t.Fatalf("list dreams: %d %s", dreams.Code, dreams.Body.String())
	}

	invalidCheckpoint := request(http.MethodPost, "/api/experience/journal", map[string]any{
		"conversation_id": conversation.ConversationID, "summary": "bad", "state": nil,
	})
	if invalidCheckpoint.Code != http.StatusBadRequest {
		t.Fatalf("null checkpoint state: got %d, want 400", invalidCheckpoint.Code)
	}
	wrongProject := request(http.MethodDelete, "/api/experience/memories/"+memory.ID+"?cwd="+url.QueryEscape(t.TempDir()), nil)
	if wrongProject.Code != http.StatusNotFound {
		t.Fatalf("cross-project delete: got %d, want 404", wrongProject.Code)
	}
	deleted := request(http.MethodDelete, "/api/experience/memories/"+memory.ID+"?cwd="+url.QueryEscape(cwd), nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete memory: %d %s", deleted.Code, deleted.Body.String())
	}
}
