package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"shelley.exe.dev/db/generated"
)

func TestHandleVersion(t *testing.T) {
	h := NewTestHarness(t)
	defer h.cleanup()

	// Test successful GET request
	req := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	w := httptest.NewRecorder()
	h.server.handleVersion(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, w.Code)
	}

	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Expected Content-Type application/json, got %s", w.Header().Get("Content-Type"))
	}

	// Test method not allowed
	req = httptest.NewRequest(http.MethodPost, "/api/version", nil)
	w = httptest.NewRecorder()
	h.server.handleVersion(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected status code %d, got %d", http.StatusMethodNotAllowed, w.Code)
	}
}

func TestHandleArchivedConversations(t *testing.T) {
	h := NewTestHarness(t)
	defer h.cleanup()

	// Create a test conversation and archive it
	ctx := context.Background()
	slug := "test-conversation"
	conv, err := h.db.CreateConversation(ctx, &slug, true, nil)
	if err != nil {
		t.Fatalf("Failed to create conversation: %v", err)
	}

	_, err = h.db.ArchiveConversation(ctx, conv.ConversationID)
	if err != nil {
		t.Fatalf("Failed to archive conversation: %v", err)
	}

	// Test successful GET request
	req := httptest.NewRequest(http.MethodGet, "/api/conversations/archived", nil)
	w := httptest.NewRecorder()
	h.server.handleArchivedConversations(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, w.Code)
	}

	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Expected Content-Type application/json, got %s", w.Header().Get("Content-Type"))
	}

	var conversations []generated.Conversation
	if err := json.Unmarshal(w.Body.Bytes(), &conversations); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if len(conversations) != 1 {
		t.Errorf("Expected 1 archived conversation, got %d", len(conversations))
	}

	// Test method not allowed
	req = httptest.NewRequest(http.MethodPost, "/api/conversations/archived", nil)
	w = httptest.NewRecorder()
	h.server.handleArchivedConversations(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected status code %d, got %d", http.StatusMethodNotAllowed, w.Code)
	}

	// Test with query parameters
	req = httptest.NewRequest(http.MethodGet, "/api/conversations/archived?limit=10&offset=0", nil)
	w = httptest.NewRecorder()
	h.server.handleArchivedConversations(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, w.Code)
	}
}

func TestHandleArchiveConversation(t *testing.T) {
	h := NewTestHarness(t)
	defer h.cleanup()

	// Create a test conversation
	ctx := context.Background()
	slug := "test-conversation"
	conv, err := h.db.CreateConversation(ctx, &slug, true, nil)
	if err != nil {
		t.Fatalf("Failed to create conversation: %v", err)
	}

	// Test successful POST request
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/conversation/%s/archive", conv.ConversationID), nil)
	w := httptest.NewRecorder()
	h.server.handleArchiveConversation(w, req, conv.ConversationID)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, w.Code)
	}

	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Expected Content-Type application/json, got %s", w.Header().Get("Content-Type"))
	}

	var archivedConv generated.Conversation
	if err := json.Unmarshal(w.Body.Bytes(), &archivedConv); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if !archivedConv.Archived {
		t.Error("Expected conversation to be archived")
	}

	// Test method not allowed
	req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/conversation/%s/archive", conv.ConversationID), nil)
	w = httptest.NewRecorder()
	h.server.handleArchiveConversation(w, req, conv.ConversationID)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected status code %d, got %d", http.StatusMethodNotAllowed, w.Code)
	}

	// Test with invalid conversation ID
	req = httptest.NewRequest(http.MethodPost, "/conversation/invalid-id/archive", nil)
	w = httptest.NewRecorder()
	h.server.handleArchiveConversation(w, req, "invalid-id")

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Expected status code %d, got %d", http.StatusInternalServerError, w.Code)
	}
}

func TestHandleUnarchiveConversation(t *testing.T) {
	h := NewTestHarness(t)
	defer h.cleanup()

	// Create a test conversation and archive it
	ctx := context.Background()
	slug := "test-conversation"
	conv, err := h.db.CreateConversation(ctx, &slug, true, nil)
	if err != nil {
		t.Fatalf("Failed to create conversation: %v", err)
	}

	_, err = h.db.ArchiveConversation(ctx, conv.ConversationID)
	if err != nil {
		t.Fatalf("Failed to archive conversation: %v", err)
	}

	// Test successful POST request
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/conversation/%s/unarchive", conv.ConversationID), nil)
	w := httptest.NewRecorder()
	h.server.handleUnarchiveConversation(w, req, conv.ConversationID)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, w.Code)
	}

	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Expected Content-Type application/json, got %s", w.Header().Get("Content-Type"))
	}

	var unarchivedConv generated.Conversation
	if err := json.Unmarshal(w.Body.Bytes(), &unarchivedConv); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if unarchivedConv.Archived {
		t.Error("Expected conversation to be unarchived")
	}

	// Test method not allowed
	req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/conversation/%s/unarchive", conv.ConversationID), nil)
	w = httptest.NewRecorder()
	h.server.handleUnarchiveConversation(w, req, conv.ConversationID)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected status code %d, got %d", http.StatusMethodNotAllowed, w.Code)
	}

	// Test with invalid conversation ID
	req = httptest.NewRequest(http.MethodPost, "/conversation/invalid-id/unarchive", nil)
	w = httptest.NewRecorder()
	h.server.handleUnarchiveConversation(w, req, "invalid-id")

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Expected status code %d, got %d", http.StatusInternalServerError, w.Code)
	}
}

func TestHandleDeleteConversation(t *testing.T) {
	h := NewTestHarness(t)
	defer h.cleanup()

	// Create a test conversation
	ctx := context.Background()
	slug := "test-conversation"
	conv, err := h.db.CreateConversation(ctx, &slug, true, nil)
	if err != nil {
		t.Fatalf("Failed to create conversation: %v", err)
	}

	// Test successful POST request
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/conversation/%s/delete", conv.ConversationID), nil)
	w := httptest.NewRecorder()
	h.server.handleDeleteConversation(w, req, conv.ConversationID)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, w.Code)
	}

	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Expected Content-Type application/json, got %s", w.Header().Get("Content-Type"))
	}

	var response map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if response["status"] != "deleted" {
		t.Errorf("Expected status 'deleted', got '%s'", response["status"])
	}

	// Verify conversation is deleted
	_, err = h.db.GetConversationByID(ctx, conv.ConversationID)
	if err == nil {
		t.Error("Expected conversation to be deleted, but it still exists")
	}

	// Test method not allowed
	req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/conversation/%s/delete", conv.ConversationID), nil)
	w = httptest.NewRecorder()
	h.server.handleDeleteConversation(w, req, conv.ConversationID)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected status code %d, got %d", http.StatusMethodNotAllowed, w.Code)
	}

	// Test with invalid conversation ID (should still return success as DELETE is idempotent)
	req = httptest.NewRequest(http.MethodPost, "/conversation/invalid-id/delete", nil)
	w = httptest.NewRecorder()
	h.server.handleDeleteConversation(w, req, "invalid-id")

	if w.Code != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, w.Code)
	}
}

func TestHandleRenameConversation(t *testing.T) {
	h := NewTestHarness(t)
	defer h.cleanup()

	// Create a test conversation
	ctx := context.Background()
	slug := "test-conversation"
	conv, err := h.db.CreateConversation(ctx, &slug, true, nil)
	if err != nil {
		t.Fatalf("Failed to create conversation: %v", err)
	}

	// Test successful POST request
	newSlug := "new-test-conversation"
	body := `{"slug": "` + newSlug + `"}`
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/conversation/%s/rename", conv.ConversationID), bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.server.handleRenameConversation(w, req, conv.ConversationID)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, w.Code)
	}

	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Expected Content-Type application/json, got %s", w.Header().Get("Content-Type"))
	}

	var renamedConv generated.Conversation
	if err := json.Unmarshal(w.Body.Bytes(), &renamedConv); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if *renamedConv.Slug != newSlug {
		t.Errorf("Expected slug '%s', got '%s'", newSlug, *renamedConv.Slug)
	}

	// Test method not allowed
	req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/conversation/%s/rename", conv.ConversationID), nil)
	w = httptest.NewRecorder()
	h.server.handleRenameConversation(w, req, conv.ConversationID)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected status code %d, got %d", http.StatusMethodNotAllowed, w.Code)
	}

	// Test with invalid JSON
	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/conversation/%s/rename", conv.ConversationID), bytes.NewBufferString(`invalid json`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.server.handleRenameConversation(w, req, conv.ConversationID)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status code %d, got %d", http.StatusBadRequest, w.Code)
	}

	// Test with missing slug
	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/conversation/%s/rename", conv.ConversationID), bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.server.handleRenameConversation(w, req, conv.ConversationID)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status code %d, got %d", http.StatusBadRequest, w.Code)
	}

	// Test with empty slug
	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/conversation/%s/rename", conv.ConversationID), bytes.NewBufferString(`{"slug": ""}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.server.handleRenameConversation(w, req, conv.ConversationID)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status code %d, got %d", http.StatusBadRequest, w.Code)
	}

	// Test with invalid conversation ID
	req = httptest.NewRequest(http.MethodPost, "/conversation/invalid-id/rename", bytes.NewBufferString(`{"slug": "test"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.server.handleRenameConversation(w, req, "invalid-id")

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Expected status code %d, got %d", http.StatusInternalServerError, w.Code)
	}
}

func TestHandleWriteFile(t *testing.T) {
	h := NewTestHarness(t)
	defer h.cleanup()

	// Test successful POST request
	filePath := "/tmp/test-file.txt"
	fileContent := "test content"
	body := fmt.Sprintf(`{"path": "%s", "content": "%s"}`, filePath, fileContent)
	req := httptest.NewRequest(http.MethodPost, "/api/write-file", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.server.handleWriteFile(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, w.Code)
	}

	// Verify file was written
	// content, err := os.ReadFile(filePath)
	// if err != nil {
	// 	t.Fatalf("Failed to read written file: %v", err)
	// }
	// if string(content) != fileContent {
	// 	t.Errorf("Expected file content '%s', got '%s'", fileContent, string(content))
	// }

	// Test method not allowed
	req = httptest.NewRequest(http.MethodGet, "/api/write-file", nil)
	w = httptest.NewRecorder()
	h.server.handleWriteFile(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected status code %d, got %d", http.StatusMethodNotAllowed, w.Code)
	}

	// Test with invalid JSON
	req = httptest.NewRequest(http.MethodPost, "/api/write-file", bytes.NewBufferString(`invalid json`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.server.handleWriteFile(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status code %d, got %d", http.StatusBadRequest, w.Code)
	}

	// Test with missing path
	req = httptest.NewRequest(http.MethodPost, "/api/write-file", bytes.NewBufferString(`{"content": "test"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.server.handleWriteFile(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status code %d, got %d", http.StatusBadRequest, w.Code)
	}

	// Test with relative path (should fail)
	req = httptest.NewRequest(http.MethodPost, "/api/write-file", bytes.NewBufferString(`{"path": "relative-path.txt", "content": "test"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.server.handleWriteFile(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status code %d, got %d", http.StatusBadRequest, w.Code)
	}
}
