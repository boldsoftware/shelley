package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
)

// TestConversationStreamReceivesListUpdateForNewConversation tests that when subscribed
// to one conversation's stream, we receive updates about new conversations.
func TestConversationStreamReceivesListUpdateForNewConversation(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	// Create a conversation to subscribe to
	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}

	// Get or create conversation manager to ensure the conversation is active
	_, err = server.getOrCreateConversationManager(context.Background(), conversation.ConversationID, "")
	if err != nil {
		t.Fatalf("failed to get conversation manager: %v", err)
	}

	// Start the conversation stream
	sseCtx, sseCancel := context.WithCancel(context.Background())
	defer sseCancel()

	sseRecorder := newFlusherRecorder()
	sseReq := httptest.NewRequest("GET", "/api/conversation/"+conversation.ConversationID+"/stream", nil)
	sseReq = sseReq.WithContext(sseCtx)

	sseStarted := make(chan struct{})
	sseDone := make(chan struct{})
	go func() {
		close(sseStarted)
		server.handleStreamConversation(sseRecorder, sseReq, conversation.ConversationID)
		close(sseDone)
	}()

	<-sseStarted

	// Wait for the initial event
	select {
	case <-sseRecorder.flushed:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for initial SSE event")
	}

	// Create another conversation via the API
	chatReq := ChatRequest{
		Message: "hello",
		Model:   "predictable",
	}
	chatBody, _ := json.Marshal(chatReq)
	req := httptest.NewRequest("POST", "/api/conversations/new", strings.NewReader(string(chatBody)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.handleNewConversation(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// Wait for the conversation list update to come through the existing stream
	deadline := time.Now().Add(2 * time.Second)
	var receivedUpdate bool
	for time.Now().Before(deadline) && !receivedUpdate {
		select {
		case <-sseRecorder.flushed:
			chunks := sseRecorder.getChunks()
			for _, chunk := range chunks {
				// Check for conversation_list_update with the new conversation ID
				if strings.Contains(chunk, "conversation_list_update") && strings.Contains(chunk, resp.ConversationID) {
					receivedUpdate = true
					break
				}
			}
		case <-time.After(100 * time.Millisecond):
		}
	}

	if !receivedUpdate {
		t.Error("did not receive conversation list update for new conversation")
		chunks := sseRecorder.getChunks()
		t.Logf("SSE chunks received: %v", chunks)
	}

	sseCancel()
	<-sseDone
}

// TestConversationStreamReceivesListUpdateForRename tests that when subscribed
// to one conversation's stream, we receive updates when another conversation is renamed.
func TestConversationStreamReceivesListUpdateForRename(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	// Create two conversations
	conv1, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation 1: %v", err)
	}
	conv2, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation 2: %v", err)
	}

	// Get or create conversation manager for conv1 (the one we'll subscribe to)
	_, err = server.getOrCreateConversationManager(context.Background(), conv1.ConversationID, "")
	if err != nil {
		t.Fatalf("failed to get conversation manager: %v", err)
	}

	// Start the conversation stream for conv1
	sseCtx, sseCancel := context.WithCancel(context.Background())
	defer sseCancel()

	sseRecorder := newFlusherRecorder()
	sseReq := httptest.NewRequest("GET", "/api/conversation/"+conv1.ConversationID+"/stream", nil)
	sseReq = sseReq.WithContext(sseCtx)

	sseStarted := make(chan struct{})
	sseDone := make(chan struct{})
	go func() {
		close(sseStarted)
		server.handleStreamConversation(sseRecorder, sseReq, conv1.ConversationID)
		close(sseDone)
	}()

	<-sseStarted

	// Wait for the initial event
	select {
	case <-sseRecorder.flushed:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for initial SSE event")
	}

	// Rename conv2
	renameReq := RenameRequest{Slug: "test-slug-rename"}
	renameBody, _ := json.Marshal(renameReq)
	req := httptest.NewRequest("POST", "/api/conversation/"+conv2.ConversationID+"/rename", strings.NewReader(string(renameBody)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.handleRenameConversation(w, req, conv2.ConversationID)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	// Wait for the conversation list update with the new slug
	deadline := time.Now().Add(2 * time.Second)
	var receivedUpdate bool
	for time.Now().Before(deadline) && !receivedUpdate {
		select {
		case <-sseRecorder.flushed:
			chunks := sseRecorder.getChunks()
			for _, chunk := range chunks {
				if strings.Contains(chunk, "conversation_list_update") && strings.Contains(chunk, "test-slug-rename") {
					receivedUpdate = true
					break
				}
			}
		case <-time.After(100 * time.Millisecond):
		}
	}

	if !receivedUpdate {
		t.Error("did not receive conversation list update for slug change")
		chunks := sseRecorder.getChunks()
		t.Logf("SSE chunks received: %v", chunks)
	}

	sseCancel()
	<-sseDone
}

// TestConversationStreamReceivesListUpdateForDelete tests that when subscribed
// to one conversation's stream, we receive updates when another conversation is deleted.
func TestConversationStreamReceivesListUpdateForDelete(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	// Create two conversations
	conv1, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation 1: %v", err)
	}
	conv2, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation 2: %v", err)
	}

	// Get or create conversation manager for conv1
	_, err = server.getOrCreateConversationManager(context.Background(), conv1.ConversationID, "")
	if err != nil {
		t.Fatalf("failed to get conversation manager: %v", err)
	}

	// Start the conversation stream for conv1
	sseCtx, sseCancel := context.WithCancel(context.Background())
	defer sseCancel()

	sseRecorder := newFlusherRecorder()
	sseReq := httptest.NewRequest("GET", "/api/conversation/"+conv1.ConversationID+"/stream", nil)
	sseReq = sseReq.WithContext(sseCtx)

	sseStarted := make(chan struct{})
	sseDone := make(chan struct{})
	go func() {
		close(sseStarted)
		server.handleStreamConversation(sseRecorder, sseReq, conv1.ConversationID)
		close(sseDone)
	}()

	<-sseStarted

	// Wait for the initial event
	select {
	case <-sseRecorder.flushed:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for initial SSE event")
	}

	// Delete conv2
	req := httptest.NewRequest("POST", "/api/conversation/"+conv2.ConversationID+"/delete", nil)
	w := httptest.NewRecorder()

	server.handleDeleteConversation(w, req, conv2.ConversationID)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	// Wait for the delete update
	deadline := time.Now().Add(2 * time.Second)
	var receivedUpdate bool
	for time.Now().Before(deadline) && !receivedUpdate {
		select {
		case <-sseRecorder.flushed:
			chunks := sseRecorder.getChunks()
			for _, chunk := range chunks {
				if strings.Contains(chunk, "conversation_list_update") &&
					strings.Contains(chunk, `"type":"delete"`) &&
					strings.Contains(chunk, conv2.ConversationID) {
					receivedUpdate = true
					break
				}
			}
		case <-time.After(100 * time.Millisecond):
		}
	}

	if !receivedUpdate {
		t.Error("did not receive conversation list delete update")
		chunks := sseRecorder.getChunks()
		t.Logf("SSE chunks received: %v", chunks)
	}

	sseCancel()
	<-sseDone
}

// TestConversationStreamReceivesListUpdateForArchive tests that when subscribed
// to one conversation's stream, we receive updates when another conversation is archived.
func TestConversationStreamReceivesListUpdateForArchive(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	// Create two conversations
	conv1, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation 1: %v", err)
	}
	conv2, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation 2: %v", err)
	}

	// Get or create conversation manager for conv1
	_, err = server.getOrCreateConversationManager(context.Background(), conv1.ConversationID, "")
	if err != nil {
		t.Fatalf("failed to get conversation manager: %v", err)
	}

	// Start the conversation stream for conv1
	sseCtx, sseCancel := context.WithCancel(context.Background())
	defer sseCancel()

	sseRecorder := newFlusherRecorder()
	sseReq := httptest.NewRequest("GET", "/api/conversation/"+conv1.ConversationID+"/stream", nil)
	sseReq = sseReq.WithContext(sseCtx)

	sseStarted := make(chan struct{})
	sseDone := make(chan struct{})
	go func() {
		close(sseStarted)
		server.handleStreamConversation(sseRecorder, sseReq, conv1.ConversationID)
		close(sseDone)
	}()

	<-sseStarted

	// Wait for the initial event
	select {
	case <-sseRecorder.flushed:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for initial SSE event")
	}

	// Archive conv2
	req := httptest.NewRequest("POST", "/api/conversation/"+conv2.ConversationID+"/archive", nil)
	w := httptest.NewRecorder()

	server.handleArchiveConversation(w, req, conv2.ConversationID)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	// Wait for the archive update
	deadline := time.Now().Add(2 * time.Second)
	var receivedUpdate bool
	for time.Now().Before(deadline) && !receivedUpdate {
		select {
		case <-sseRecorder.flushed:
			chunks := sseRecorder.getChunks()
			for _, chunk := range chunks {
				if strings.Contains(chunk, "conversation_list_update") &&
					strings.Contains(chunk, conv2.ConversationID) &&
					strings.Contains(chunk, `"archived":true`) {
					receivedUpdate = true
					break
				}
			}
		case <-time.After(100 * time.Millisecond):
		}
	}

	if !receivedUpdate {
		t.Error("did not receive conversation list archive update")
		chunks := sseRecorder.getChunks()
		t.Logf("SSE chunks received: %v", chunks)
	}

	sseCancel()
	<-sseDone
}

func TestConversationStreamIncludesListPatchInitialReset(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	conversation, err := database.CreateConversation(context.Background(), strPtr("current"), true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateConversation(context.Background(), strPtr("other"), true, nil, nil, db.ConversationOptions{}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := newFlusherRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/stream?conversation="+conversation.ConversationID, nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		server.handleStream(rec, req)
		close(done)
	}()

	patch := waitForConversationStreamListPatch(t, rec, "")
	if !patch.Reset || patch.OldHash != nil || patch.NewHash == "" {
		t.Fatalf("expected initial reset patch, got %+v", patch)
	}
	state := mustApplyPatch(t, nil, patch.Patch)
	if len(state) != 2 {
		t.Fatalf("expected reset list with 2 conversations, got %d", len(state))
	}
	verifyHash(t, state, patch.NewHash)

	cancel()
	<-done
}

func TestConversationStreamListPatchReplaysFromHash(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	conversation, err := database.CreateConversation(context.Background(), strPtr("current"), true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}

	patchCtx, patchCancel := context.WithCancel(context.Background())
	patchRec := newFlusherRecorder()
	patchReq := httptest.NewRequest(http.MethodGet, "/api/stream", nil).WithContext(patchCtx)
	patchDone := make(chan struct{})
	go func() {
		server.handleStream(patchRec, patchReq)
		close(patchDone)
	}()
	initial := waitForPatchEventAfter(t, patchRec, "")
	for _, slug := range []string{"one", "two"} {
		if _, err := database.CreateConversation(context.Background(), strPtr(slug), true, nil, nil, db.ConversationOptions{}); err != nil {
			t.Fatal(err)
		}
		server.publishConversationListUpdate(ConversationListUpdate{Type: "update"})
	}
	patchCancel()
	<-patchDone

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := newFlusherRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/stream?conversation="+conversation.ConversationID+"&conversation_list_hash="+initial.NewHash, nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		server.handleStream(rec, req)
		close(done)
	}()

	first := waitForConversationStreamListPatch(t, rec, initial.NewHash)
	second := waitForConversationStreamListPatch(t, rec, first.NewHash)
	if first.Reset || second.Reset {
		t.Fatalf("expected replay patches, got %+v then %+v", first, second)
	}
	if first.OldHash == nil || *first.OldHash != initial.NewHash {
		t.Fatalf("first patch should start at initial hash, got %+v", first)
	}
	if second.OldHash == nil || *second.OldHash != first.NewHash {
		t.Fatalf("second patch should chain from first, got %+v", second)
	}

	cancel()
	<-done
}

func TestConversationStreamListPatchCurrentHashSkipsInitialAndStreamsLive(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	conversation, err := database.CreateConversation(context.Background(), strPtr("current"), true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.conversationListStream.recompute(context.Background()); err != nil {
		t.Fatal(err)
	}
	currentHash := server.conversationListStream.currentHash

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := newFlusherRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/stream?conversation="+conversation.ConversationID+"&conversation_list_hash="+currentHash, nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		server.handleStream(rec, req)
		close(done)
	}()

	waitForConversationStreamData(t, rec)
	// The contract for a matching conversation_list_hash is "don't replay
	// the world": no reset and no spurious full-list patches. Background
	// recomputes between the test's hash capture and the server's connect
	// may emit a benign forward patch from currentHash if internal writes
	// happened (e.g. system prompt hydration) — that's fine as long as it
	// is NOT a reset.
	for _, ev := range parseConversationStreamListPatches(rec.getString()) {
		if ev.Reset {
			t.Fatalf("did not expect initial reset for current hash, got %+v", ev)
		}
		if ev.OldHash == nil || *ev.OldHash != currentHash {
			t.Fatalf("unexpected initial patch with non-current OldHash; got %+v", ev)
		}
		currentHash = ev.NewHash
	}

	if _, err := database.CreateConversation(context.Background(), strPtr("newer"), true, nil, nil, db.ConversationOptions{}); err != nil {
		t.Fatal(err)
	}
	server.publishConversationListUpdate(ConversationListUpdate{Type: "update"})
	patch := waitForConversationStreamListPatch(t, rec, currentHash)
	if patch.Reset || patch.OldHash == nil || *patch.OldHash != currentHash {
		t.Fatalf("expected live patch from current hash, got %+v", patch)
	}

	cancel()
	<-done
}

// streamWaitTimeout is generous on purpose: the per-conversation stream
// handler runs Hydrate before its first flush, which on a cold cache shells
// out to git and walks the working tree for guidance files. Under -race on
// a loaded CI worker that has been observed to take several seconds, so a
// 2s ceiling produced flakes (e.g. Buildkite #2891).
const streamWaitTimeout = 30 * time.Second

func waitForConversationStreamData(t *testing.T, rec *flusherRecorder) {
	t.Helper()
	timer := time.NewTimer(streamWaitTimeout)
	defer timer.Stop()
	for {
		if strings.Contains(rec.getString(), "\n\n") {
			return
		}
		select {
		case <-rec.flushed:
		case <-timer.C:
			t.Fatalf("timed out waiting for conversation stream data; body=%s", rec.getString())
		}
	}
}

func waitForConversationStreamListPatch(t *testing.T, rec *flusherRecorder, prevHash string) ConversationListPatchEvent {
	t.Helper()
	timer := time.NewTimer(streamWaitTimeout)
	defer timer.Stop()
	for {
		for _, ev := range parseConversationStreamListPatches(rec.getString()) {
			if (prevHash == "" && (ev.OldHash == nil || *ev.OldHash == "" || ev.Reset)) ||
				(prevHash != "" && ev.OldHash != nil && *ev.OldHash == prevHash) {
				return ev
			}
		}
		select {
		case <-rec.flushed:
		case <-timer.C:
			t.Fatalf("timed out waiting for conversation_list_patch after %q; body=%s", prevHash, rec.getString())
		}
	}
}

func parseConversationStreamListPatches(body string) []ConversationListPatchEvent {
	var events []ConversationListPatchEvent
	for _, part := range strings.Split(body, "\n\n") {
		for _, line := range strings.Split(part, "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var response StreamResponse
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &response); err != nil {
				continue
			}
			if response.ConversationListPatch != nil {
				events = append(events, *response.ConversationListPatch)
			}
		}
	}
	return events
}
