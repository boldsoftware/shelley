package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/ui"
)

func TestHandleVersion(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	mux := http.NewServeMux()
	h.server.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", ct)
	}

	var body struct {
		Capabilities *[]string `json:"capabilities"`
		Modified     *bool     `json:"modified"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	// capabilities must be present so clients can rely on its shape,
	// and must include the features the server actively advertises.
	if body.Capabilities == nil {
		t.Fatalf("expected capabilities field in response, got nil")
	}
	want := map[string]bool{"thinking-levels": false, "drafts": false, "queued-transcriptions": false, "live-call-events": false, "composer-text": false, "mark-unread": false}
	for _, c := range *body.Capabilities {
		if _, ok := want[c]; ok {
			want[c] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("expected capabilities to include %q, got %v", name, *body.Capabilities)
		}
	}
	if body.Modified != nil {
		t.Errorf("unexpected modified field in response: %v", *body.Modified)
	}
}

func TestHandleArchivedConversations(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	// Create a test conversation and archive it
	ctx := t.Context()
	slug := "test-conversation"
	conv, err := h.db.CreateConversation(ctx, &slug, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create conversation: %v", err)
	}
	if _, err := h.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           db.MessageTypeUser,
		UserEmail:      "alice@example.com",
	}); err != nil {
		t.Fatalf("Failed to create participant message: %v", err)
	}
	if _, err := h.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           db.MessageTypeUser,
		UserEmail:      "alice@example.com",
	}); err != nil {
		t.Fatalf("Failed to create second participant message: %v", err)
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

	var conversations []struct {
		generated.Conversation
		Participants []db.ConversationParticipant `json:"participants,omitempty"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &conversations); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if len(conversations) != 1 {
		t.Errorf("Expected 1 archived conversation, got %d", len(conversations))
	}
	wantParticipant := db.ConversationParticipant{Email: "alice@example.com", MessageCount: 2}
	if got := conversations[0].Participants; len(got) != 1 || got[0] != wantParticipant {
		t.Errorf("Expected archived participants [%+v], got %v", wantParticipant, got)
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
	t.Parallel()
	h := NewTestHarness(t)

	// Create a test conversation
	ctx := t.Context()
	slug := "test-conversation"
	conv, err := h.db.CreateConversation(ctx, &slug, true, nil, nil, db.ConversationOptions{})
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

	// Test with invalid conversation ID
	req = httptest.NewRequest(http.MethodPost, "/conversation/invalid-id/archive", nil)
	w = httptest.NewRecorder()
	h.server.handleArchiveConversation(w, req, "invalid-id")

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Expected status code %d, got %d", http.StatusInternalServerError, w.Code)
	}
}

func TestHandleUnarchiveConversation(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	// Create a test conversation and archive it
	ctx := t.Context()
	slug := "test-conversation"
	conv, err := h.db.CreateConversation(ctx, &slug, true, nil, nil, db.ConversationOptions{})
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

	// Test with invalid conversation ID
	req = httptest.NewRequest(http.MethodPost, "/conversation/invalid-id/unarchive", nil)
	w = httptest.NewRecorder()
	h.server.handleUnarchiveConversation(w, req, "invalid-id")

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Expected status code %d, got %d", http.StatusInternalServerError, w.Code)
	}
}

func TestHandleDeleteConversation(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	// Create a test conversation
	ctx := t.Context()
	slug := "test-conversation"
	conv, err := h.db.CreateConversation(ctx, &slug, true, nil, nil, db.ConversationOptions{})
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

	// Test with invalid conversation ID (should still return success as DELETE is idempotent)
	req = httptest.NewRequest(http.MethodPost, "/conversation/invalid-id/delete", nil)
	w = httptest.NewRecorder()
	h.server.handleDeleteConversation(w, req, "invalid-id")

	if w.Code != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, w.Code)
	}
}

func TestHandleRenameConversation(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	// Create a test conversation
	ctx := t.Context()
	slug := "test-conversation"
	conv, err := h.db.CreateConversation(ctx, &slug, true, nil, nil, db.ConversationOptions{})
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

	// Test duplicate slug
	duplicateSlug := "duplicate-slug"
	if _, err := h.db.CreateConversation(ctx, &duplicateSlug, true, nil, nil, db.ConversationOptions{}); err != nil {
		t.Fatalf("Failed to create duplicate-slug conversation: %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/conversation/%s/rename", conv.ConversationID), bytes.NewBufferString(`{"slug": "duplicate-slug"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.server.handleRenameConversation(w, req, conv.ConversationID)

	if w.Code != http.StatusConflict {
		t.Errorf("Expected status code %d, got %d: %s", http.StatusConflict, w.Code, w.Body.String())
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
	t.Parallel()
	h := NewTestHarness(t)

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

func TestHandleTools(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	req := httptest.NewRequest(http.MethodGet, "/api/tools", nil)
	w := httptest.NewRecorder()
	h.server.handleTools(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Tools []struct {
			Name      string `json:"name"`
			Summary   string `json:"summary"`
			DefaultOn bool   `json:"default_on"`
		} `json:"tools"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Tools) == 0 {
		t.Fatalf("expected non-empty tools list")
	}
	var hasBash bool
	for _, tt := range resp.Tools {
		if tt.Name == "keyword_search" || tt.Name == "chapter" {
			t.Fatalf("%s must not appear in the tool registry", tt.Name)
		}
		if tt.Name == "bash" {
			hasBash = true
			if !tt.DefaultOn {
				t.Fatalf("bash should be default on")
			}
		}
	}
	if !hasBash {
		t.Fatalf("bash missing from registry")
	}
}

// The Nerd Font is 1.2MB; browsers must be able to revalidate it (ETag/304)
// rather than download it on every page load that renders an icon.
func TestStaticFontRevalidates(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	mux := http.NewServeMux()
	h.server.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/SymbolsNerdFontMono.woff2", nil))
	etag := rec.Header().Get("ETag")
	if rec.Code != http.StatusOK || etag == "" || rec.Header().Get("Content-Type") != "font/woff2" {
		t.Fatalf("status %d, ETag %q, Content-Type %q", rec.Code, etag, rec.Header().Get("Content-Type"))
	}
	req := httptest.NewRequest("GET", "/SymbolsNerdFontMono.woff2", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("revalidation status %d, want 304", rec.Code)
	}
}

// Files under /assets/ are named after their content (ui/scripts/build.js), so
// browsers may keep them for good; main.js, which names them, revalidates.
func TestStaticAssetsImmutable(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	mux := http.NewServeMux()
	h.server.RegisterRoutes(mux)
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		mux.ServeHTTP(rec, req)
		return rec
	}

	main := get("/main.js")
	if main.Code != http.StatusOK || main.Header().Get("Cache-Control") != "public, max-age=0, must-revalidate" {
		t.Fatalf("/main.js: status %d, Cache-Control %q", main.Code, main.Header().Get("Cache-Control"))
	}
	assets, err := fs.Glob(ui.Dist, "dist/assets/*")
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]string{
		".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8",
		".woff2": "font/woff2", ".woff": "font/woff", ".ttf": "font/ttf",
		".eot": "application/vnd.ms-fontobject", ".svg": "image/svg+xml",
	}
	seen := map[string]bool{}
	for _, a := range assets {
		path := strings.TrimSuffix(strings.TrimPrefix(a, "dist"), ".gz")
		ext := filepath.Ext(path)
		if ext == ".map" {
			continue
		}
		seen[ext] = true
		rec := get(path)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" ||
			rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" ||
			rec.Header().Get("Content-Type") != types[ext] {
			t.Errorf("%s: status %d, Content-Encoding %q, Cache-Control %q, Content-Type %q", path, rec.Code,
				rec.Header().Get("Content-Encoding"), rec.Header().Get("Cache-Control"), rec.Header().Get("Content-Type"))
		}
	}
	for ext := range types {
		if !seen[ext] {
			t.Errorf("no %s under dist/assets", ext)
		}
	}
	if rec := get("/assets/missing-0000.js"); rec.Code != http.StatusNotFound || rec.Header().Get("Cache-Control") != "" {
		t.Errorf("missing asset: status %d, Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
}

// TestRoutesRejectWrongMethod checks that the route table, not the handlers,
// answers wrong-method requests with 405 and an Allow header.
func TestRoutesRejectWrongMethod(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	mux := http.NewServeMux()
	h.server.RegisterRoutes(mux)
	for _, tc := range []struct{ method, path, allow string }{
		{http.MethodGet, "/api/conversation/c1/archive", "POST"},
		{http.MethodGet, "/api/upload", "POST"},
		{http.MethodPost, "/api/find-files", "GET, HEAD"},
		{http.MethodPost, "/api/custom-models/m1", "DELETE, GET, HEAD, PUT"},
		{http.MethodGet, "/api/notification-channels/n1/test", "POST"},
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != tc.allow {
			t.Errorf("%s %s: got %d Allow=%q, want 405 Allow=%q", tc.method, tc.path, w.Code, w.Header().Get("Allow"), tc.allow)
		}
	}
}
