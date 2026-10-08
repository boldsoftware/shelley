package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
)

type heldLLMCall struct {
	request *llm.Request
	release chan struct{}
	once    sync.Once
}

func (c *heldLLMCall) Release() { c.once.Do(func() { close(c.release) }) }

type heldLLMService struct {
	llm.Service
	mu      sync.Mutex
	calls   map[string][]*heldLLMCall
	changed chan struct{}
}

func newHeldLLMService() *heldLLMService {
	return &heldLLMService{
		Service: predictable.NewService(),
		calls:   make(map[string][]*heldLLMCall),
		changed: make(chan struct{}),
	}
}

func (s *heldLLMService) Do(ctx context.Context, request *llm.Request) (*llm.Response, error) {
	call := &heldLLMCall{request: request, release: make(chan struct{})}
	text := lastRequestUserText(request)
	s.mu.Lock()
	s.calls[text] = append(s.calls[text], call)
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
	select {
	case <-call.release:
		return s.Service.Do(ctx, request)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *heldLLMService) waitCall(t *testing.T, text string) *heldLLMCall {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		s.mu.Lock()
		if calls := s.calls[text]; len(calls) > 0 {
			call := calls[0]
			s.calls[text] = calls[1:]
			s.mu.Unlock()
			return call
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			t.Fatalf("timed out waiting for LLM call %q", text)
		}
	}
}

func lastRequestUserText(request *llm.Request) string {
	for i := len(request.Messages) - 1; i >= 0; i-- {
		if request.Messages[i].Role != llm.MessageRoleUser {
			continue
		}
		for _, content := range request.Messages[i].Content {
			if content.Type == llm.ContentTypeText {
				return strings.TrimSpace(content.Text)
			}
		}
	}
	return ""
}

func postBtwChat(t *testing.T, server *Server, conversationID string, request any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	server.handleChatConversation(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body))), conversationID)
	return w
}

func postBtw(t *testing.T, server *Server, parentID, question string, queue bool) db.BtwReaderIdentity {
	t.Helper()
	w := postBtwChat(t, server, parentID, ChatRequest{Message: "/btw " + question, Model: "predictable", Queue: queue})
	if w.Code != http.StatusAccepted {
		t.Fatalf("/btw status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Status string               `json:"status"`
		Btw    db.BtwReaderIdentity `json:"btw"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "accepted" || response.Btw.ConversationID == "" {
		t.Fatalf("invalid /btw response: %s", w.Body.String())
	}
	return response.Btw
}

func releaseAndWaitIdle(t *testing.T, server *Server, conversationID string, call *heldLLMCall) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	next := server.streamPub.Subscribe(ctx, 0)
	call.Release()
	for {
		event, ok := next()
		if !ok {
			t.Fatalf("timed out waiting for %s to become idle", conversationID)
		}
		if event.ConversationState != nil && event.ConversationState.ConversationID == conversationID && !event.ConversationState.Working {
			return
		}
	}
}

func newBtwTest(t *testing.T) (*Server, *db.DB, *heldLLMService, *generated.Conversation) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	server, database, _ := newTestServer(t)
	held := newHeldLLMService()
	server.llmManager = &testLLMManager{service: held}
	t.Cleanup(func() { stopActiveConversationLoops(server) })
	parent, err := database.CreateConversation(
		t.Context(), nil, true, nil, strPtr("predictable"), db.ConversationOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	return server, database, held, parent
}

func requireBtwStatus(t *testing.T, response *httptest.ResponseRecorder, status int) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func btwResponseMessageID(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.MessageID == "" {
		t.Fatalf("message receipt=%#v err=%v body=%s", body, err, response.Body.String())
	}
	return body.MessageID
}

func evictBtwManager(t *testing.T, server *Server, conversationID string) {
	t.Helper()
	server.mu.Lock()
	manager := server.activeConversations[conversationID]
	delete(server.activeConversations, conversationID)
	server.mu.Unlock()
	if manager == nil {
		t.Fatalf("missing active manager for %s", conversationID)
	}
	manager.stopLoop()
}

func btwSystemData(t *testing.T, database *db.DB, conversationID string, generation int64) string {
	t.Helper()
	var matches []generated.Message
	for _, row := range listMessages(t, database, conversationID) {
		if row.Generation == generation && row.Type == string(db.MessageTypeSystem) {
			matches = append(matches, row)
		}
	}
	if len(matches) != 1 || matches[0].LlmData == nil {
		t.Fatalf("generation %d system rows=%#v", generation, matches)
	}
	return *matches[0].LlmData
}

func requireBtwStreamOpen(t *testing.T, server *Server, conversationID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	recorder := newFlusherRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/conversation/"+conversationID+"/stream", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		server.handleStreamConversation(recorder, req, conversationID)
		close(done)
	}()

	select {
	case <-recorder.flushed:
	case <-ctx.Done():
		t.Fatalf("timed out opening BTW stream: status=%d body=%s", recorder.Code, recorder.getString())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("BTW stream did not close after cancellation")
	}
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.getString(), conversationID) {
		t.Fatalf("BTW stream status=%d body=%s", recorder.Code, recorder.getString())
	}
}

func TestBtwSlashHookRouting(t *testing.T) {
	t.Run("hook precedes built-in", func(t *testing.T) {
		server, database, _, parent := newBtwTest(t)
		writeSlashHook(t, "btw", "#!/bin/sh\nprintf 'echo: custom hook won'\n")
		w := postBtwChat(t, server, parent.ConversationID,
			ChatRequest{Message: " \t/btw ignored", Model: "predictable"})
		requireBtwStatus(t, w, http.StatusAccepted)
		if readers, err := database.ListBtwReaders(t.Context(), parent.ConversationID); err != nil || len(readers) != 0 {
			t.Fatalf("built-in ran despite hook: %#v, %v", readers, err)
		}
		rows := listMessages(t, database, parent.ConversationID)
		if len(rows) < 2 || rows[1].LlmData == nil || !strings.Contains(*rows[1].LlmData, "custom hook won") {
			t.Fatalf("hook replacement was not sent normally: %#v", rows)
		}
	})
	t.Run("empty hook fully handles command", func(t *testing.T) {
		server, database, _, parent := newBtwTest(t)
		writeSlashHook(t, "btw", "#!/bin/sh\ncat >/dev/null\n")
		requireBtwStatus(t, postBtwChat(t, server, parent.ConversationID,
			ChatRequest{Message: "/btw echo: must not run", Model: "predictable"}), http.StatusAccepted)
		if readers, err := database.ListBtwReaders(t.Context(), parent.ConversationID); err != nil || len(readers) != 0 {
			t.Fatalf("empty hook created built-in reader: %#v, %v", readers, err)
		}
		if rows := listMessages(t, database, parent.ConversationID); len(rows) != 0 {
			t.Fatalf("empty hook triggered parent turn: %#v", rows)
		}
	})
}

func TestBtwRunsChatMessageHook(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	dumpFile := filepath.Join(t.TempDir(), "chat-message.json")
	hook := fmt.Sprintf("#!/bin/sh\ncat > %q\nprintf '%%s\\n' '{\"message\":\"echo: chat-hooked\"}'\n", dumpFile)
	if err := os.WriteFile(filepath.Join(server.hooksDir, hookChatMessage), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}

	reader := postBtw(t, server, parent.ConversationID, "echo: original", true)
	held.waitCall(t, "echo: chat-hooked")
	input, err := os.ReadFile(dumpFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"message":"echo: original"`,
		`"conversation_id":"` + parent.ConversationID + `"`,
		`"queued":false`,
	} {
		if !strings.Contains(string(input), want) {
			t.Fatalf("chat-message hook input missing %q: %s", want, input)
		}
	}
	rows := listMessages(t, database, reader.ConversationID)
	if len(rows) < 2 || rows[1].LlmData == nil || !strings.Contains(*rows[1].LlmData, "echo: chat-hooked") {
		t.Fatalf("chat-message hook did not rewrite reader question: %#v", rows)
	}
}

func TestBtwRejectsDraftParent(t *testing.T) {
	server, database, _, _ := newBtwTest(t)
	model := "predictable"
	draft, err := database.CreateDraftConversation(
		t.Context(), nil, &model, db.ConversationOptions{}, "/btw echo: draft",
	)
	if err != nil {
		t.Fatal(err)
	}

	w := postBtwChat(t, server, draft.ConversationID,
		ChatRequest{Message: "/btw echo: draft", Model: model})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "draft conversations") {
		t.Fatalf("draft /btw status=%d body=%s", w.Code, w.Body.String())
	}
	if readers, err := database.ListBtwReaders(t.Context(), draft.ConversationID); err != nil || len(readers) != 0 {
		t.Fatalf("draft /btw created readers: %#v, %v", readers, err)
	}
	if rows := listMessages(t, database, draft.ConversationID); len(rows) != 0 {
		t.Fatalf("draft /btw wrote parent rows: %#v", rows)
	}
	reloaded, err := database.GetConversationByID(t.Context(), draft.ConversationID)
	if err != nil || !reloaded.IsDraft {
		t.Fatalf("draft parent was promoted: %#v, %v", reloaded, err)
	}
}

func TestBtwClearSurvivesEvictionAndRemainsUsable(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	reader := postBtw(t, server, parent.ConversationID, "echo: before clear", false)
	releaseAndWaitIdle(t, server, reader.ConversationID, held.waitCall(t, "echo: before clear"))
	initialPrompt := btwSystemData(t, database, reader.ConversationID, 1)

	w := httptest.NewRecorder()
	server.handleStartNewGeneration(w, httptest.NewRequest(http.MethodPost, "/", nil), reader.ConversationID)
	if w.Code != http.StatusOK {
		t.Fatalf("clear status=%d body=%s", w.Code, w.Body.String())
	}
	row, err := database.GetConversationByID(t.Context(), reader.ConversationID)
	if err != nil || row.CurrentGeneration != 2 {
		t.Fatalf("clear generation row=%#v err=%v", row, err)
	}
	if got := btwSystemData(t, database, reader.ConversationID, 2); got != initialPrompt {
		t.Fatalf("clear changed reader system prompt:\nold=%s\nnew=%s", initialPrompt, got)
	}

	evictBtwManager(t, server, reader.ConversationID)
	requireBtwStreamOpen(t, server, reader.ConversationID)
	requireBtwStatus(t, postBtwChat(t, server, reader.ConversationID,
		ChatRequest{Message: "echo: after clear", Model: "predictable"}), http.StatusAccepted)
	call := held.waitCall(t, "echo: after clear")
	if !requestHasText(call.request, btwReaderRestrictionPrompt) ||
		!requestHasText(call.request, btwFrozenReferenceBegin) {
		t.Fatalf("rehydrated clear request lost BTW restrictions: %#v", call.request)
	}
	releaseAndWaitIdle(t, server, reader.ConversationID, call)
}

func TestBtwCompactRecreatesPromptAndRemainsChattable(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	reader := postBtw(t, server, parent.ConversationID, "echo: before compact", false)
	releaseAndWaitIdle(t, server, reader.ConversationID, held.waitCall(t, "echo: before compact"))
	initialPrompt := btwSystemData(t, database, reader.ConversationID, 1)

	body, err := json.Marshal(DistillNewGenerationRequest{
		SourceConversationID: reader.ConversationID,
		Model:                "predictable",
		Method:               distillMethodCompact,
	})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	server.handleDistillNewGeneration(w, httptest.NewRequest(
		http.MethodPost, "/api/conversations/distill-new-generation", strings.NewReader(string(body)),
	))
	if w.Code != http.StatusCreated {
		t.Fatalf("compact status=%d body=%s", w.Code, w.Body.String())
	}
	if got := btwSystemData(t, database, reader.ConversationID, 2); got != initialPrompt {
		t.Fatalf("compact changed reader system prompt:\nold=%s\nnew=%s", initialPrompt, got)
	}

	requireBtwStatus(t, postBtwChat(t, server, reader.ConversationID,
		ChatRequest{Message: "echo: after compact", Model: "predictable"}), http.StatusAccepted)
	call := held.waitCall(t, "echo: after compact")
	if !requestHasText(call.request, btwReaderRestrictionPrompt) ||
		!requestHasText(call.request, btwFrozenReferenceBegin) {
		t.Fatalf("compacted request lost BTW restrictions: %#v", call.request)
	}
	releaseAndWaitIdle(t, server, reader.ConversationID, call)
}

func TestWedgedBtwReaderRecoversAfterServerReload(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	reader := postBtw(t, server, parent.ConversationID, "echo: before reload", false)
	releaseAndWaitIdle(t, server, reader.ConversationID, held.waitCall(t, "echo: before reload"))
	initialPrompt := btwSystemData(t, database, reader.ConversationID, 1)

	if _, err := db.WithTxRes(database, t.Context(), func(q *generated.Queries) (generated.Conversation, error) {
		return q.IncrementConversationGeneration(t.Context(), reader.ConversationID)
	}); err != nil {
		t.Fatal(err)
	}
	evictBtwManager(t, server, reader.ConversationID)

	reloaded := NewServer(
		database, &testLLMManager{service: held}, server.toolSetConfig, server.logger,
		true, "predictable", "",
	)
	reloaded.hooksDir = server.hooksDir
	reloaded.terminals.SetSpawner(InProcessSpawner)
	t.Cleanup(func() { stopActiveConversationLoops(reloaded) })

	requireBtwStreamOpen(t, reloaded, reader.ConversationID)
	if got := btwSystemData(t, database, reader.ConversationID, 2); got != initialPrompt {
		t.Fatalf("reload changed recovered reader system prompt:\nold=%s\nnew=%s", initialPrompt, got)
	}
	requireBtwStatus(t, postBtwChat(t, reloaded, reader.ConversationID,
		ChatRequest{Message: "echo: after reload", Model: "predictable"}), http.StatusAccepted)
	call := held.waitCall(t, "echo: after reload")
	if !requestHasText(call.request, btwReaderRestrictionPrompt) ||
		!requestHasText(call.request, btwFrozenReferenceBegin) {
		t.Fatalf("reloaded request lost BTW restrictions: %#v", call.request)
	}
	releaseAndWaitIdle(t, reloaded, reader.ConversationID, call)
}

func TestBtwCreationUsesServerPointerAndDetachedContext(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	ctx := t.Context()
	secret, err := database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: parent.ConversationID,
		Type:           db.MessageTypeUser,
		LLMData:        llm.UserStringMessage("PARENT_ONLY_SECRET"),
	})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"message":"/btw echo: side","model":"predictable","queue":true,"anchor_sequence_id":0,"context_generation":99}`
	w := httptest.NewRecorder()
	server.handleChatConversation(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)), parent.ConversationID)
	requireBtwStatus(t, w, http.StatusAccepted)
	var response struct {
		Btw db.BtwReaderIdentity `json:"btw"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	descriptor := response.Btw
	if descriptor.ParentPointer != (db.BtwParentPointer{Generation: parent.CurrentGeneration, SequenceID: secret.SequenceID}) {
		t.Fatalf("server pointer=%#v want generation=%d sequence=%d", descriptor.ParentPointer, parent.CurrentGeneration, secret.SequenceID)
	}
	if _, err := database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: parent.ConversationID,
		Type:           db.MessageTypeUser,
		LLMData:        llm.UserStringMessage("LATER_PARENT_TEXT"),
	}); err != nil {
		t.Fatal(err)
	}
	call := held.waitCall(t, "echo: side")
	if !requestHasText(call.request, "PARENT_ONLY_SECRET") || !requestHasText(call.request, btwReaderRestrictionPrompt) {
		t.Fatalf("request lacks frozen context/restriction: %#v", call.request)
	}
	if requestHasText(call.request, "LATER_PARENT_TEXT") {
		t.Fatal("later parent message entered detached reader request")
	}
	releaseAndWaitIdle(t, server, descriptor.ConversationID, call)
	if got := len(listMessages(t, database, parent.ConversationID)); got != 2 {
		t.Fatalf("completion injected into parent history: %d rows", got)
	}
}

func TestBtwBypassesQueueAndSiblingManagersAreIndependent(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	requireBtwStatus(t, postBtwChat(t, server, parent.ConversationID,
		ChatRequest{Message: "echo: parent", Model: "predictable"}), http.StatusAccepted)
	held.waitCall(t, "echo: parent")
	first := postBtw(t, server, parent.ConversationID, "echo: first", true)
	second := postBtw(t, server, parent.ConversationID, "echo: second", true)
	held.waitCall(t, "echo: first")
	held.waitCall(t, "echo: second")

	parentRow, err := database.GetConversationByID(t.Context(), parent.ConversationID)
	if err != nil || parentRow.QueuedMessages != "[]" {
		t.Fatalf("/btw entered parent queue: %#v, %v", parentRow, err)
	}
	server.mu.Lock()
	parentManager := server.activeConversations[parent.ConversationID]
	firstManager := server.activeConversations[first.ConversationID]
	secondManager := server.activeConversations[second.ConversationID]
	server.mu.Unlock()
	if parentManager == nil || firstManager == nil || secondManager == nil ||
		parentManager == firstManager || firstManager == secondManager || parentManager == secondManager {
		t.Fatalf("managers are not independent: parent=%p first=%p second=%p", parentManager, firstManager, secondManager)
	}
	if !parentManager.IsAgentWorking() || !firstManager.IsAgentWorking() || !secondManager.IsAgentWorking() {
		t.Fatal("parent and sibling BTWs were not concurrently active")
	}
}

func TestBtwCancellationIsDetached(t *testing.T) {
	server, _, held, parent := newBtwTest(t)
	postBtwChat(t, server, parent.ConversationID, ChatRequest{Message: "echo: parent", Model: "predictable"})
	held.waitCall(t, "echo: parent")
	first := postBtw(t, server, parent.ConversationID, "echo: first", false)
	second := postBtw(t, server, parent.ConversationID, "echo: second", false)
	held.waitCall(t, "echo: first")
	held.waitCall(t, "echo: second")

	w := httptest.NewRecorder()
	server.handleCancelConversation(w, httptest.NewRequest(http.MethodPost, "/", nil), parent.ConversationID)
	if w.Code != http.StatusOK || server.IsAgentWorking(parent.ConversationID) ||
		!server.IsAgentWorking(first.ConversationID) || !server.IsAgentWorking(second.ConversationID) {
		t.Fatalf("parent cancellation coupled BTW state: status=%d parent=%t first=%t second=%t", w.Code,
			server.IsAgentWorking(parent.ConversationID), server.IsAgentWorking(first.ConversationID), server.IsAgentWorking(second.ConversationID))
	}

	postBtwChat(t, server, parent.ConversationID, ChatRequest{Message: "echo: parent again", Model: "predictable"})
	held.waitCall(t, "echo: parent again")
	w = httptest.NewRecorder()
	server.handleCancelConversation(w, httptest.NewRequest(http.MethodPost, "/", nil), first.ConversationID)
	if w.Code != http.StatusOK || server.IsAgentWorking(first.ConversationID) || !server.IsAgentWorking(parent.ConversationID) ||
		!server.IsAgentWorking(second.ConversationID) {
		t.Fatalf("child cancellation coupled parent/sibling: status=%d parent=%t first=%t second=%t", w.Code,
			server.IsAgentWorking(parent.ConversationID), server.IsAgentWorking(first.ConversationID), server.IsAgentWorking(second.ConversationID))
	}
}

func TestBtwRejectsNestedReaders(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	reader := postBtw(t, server, parent.ConversationID, "echo: first", false)
	held.waitCall(t, "echo: first")
	w := postBtwChat(t, server, reader.ConversationID, ChatRequest{Message: "/btw nested", Model: "predictable"})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "/btw is unavailable in child conversations") {
		t.Fatalf("nested /btw status=%d body=%s", w.Code, w.Body.String())
	}
	if children, err := database.GetSubagents(t.Context(), reader.ConversationID); err != nil || len(children) != 0 {
		t.Fatalf("nested /btw created children: %#v, %v", children, err)
	}
	lineage, err := database.CreateConversation(t.Context(), nil, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateConversationParent(t.Context(), lineage.ConversationID, parent.ConversationID); err != nil {
		t.Fatal(err)
	}
	detached := postBtw(t, server, lineage.ConversationID, "echo: lineage", false)
	held.waitCall(t, "echo: lineage")
	if detached.ParentConversationID != lineage.ConversationID {
		t.Fatalf("lineage reader parent=%q want=%q", detached.ParentConversationID, lineage.ConversationID)
	}
}

func TestBtwUsesOnlyReaderToolsAndMetadataListing(t *testing.T) {
	server, _, held, parent := newBtwTest(t)
	descriptor := postBtw(t, server, parent.ConversationID, "echo: inspect", false)
	call := held.waitCall(t, "echo: inspect")
	names := make([]string, 0, len(call.request.Tools))
	for _, tool := range call.request.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "bash,read_image" {
		t.Fatalf("BTW tools=%v", names)
	}

	w := httptest.NewRecorder()
	server.handleListBtwReaders(w, httptest.NewRequest(http.MethodGet, "/", nil), parent.ConversationID)
	if w.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", w.Code, w.Body.String())
	}
	var listed struct {
		Readers []db.BtwReaderIdentity `json:"readers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || len(listed.Readers) != 1 || listed.Readers[0] != descriptor {
		t.Fatalf("metadata listing=%#v err=%v", listed, err)
	}
}

func TestBtwDismissRemovesReaderButPreservesChild(t *testing.T) {
	server, database, _, parent := newBtwTest(t)
	descriptor := postBtw(t, server, parent.ConversationID, "echo: dismiss", false)
	before := listMessages(t, database, descriptor.ConversationID)
	if len(before) == 0 {
		t.Fatal("BTW child has no messages before dismissal")
	}

	w := httptest.NewRecorder()
	server.handleDismissBtwReader(w, httptest.NewRequest(http.MethodPost, "/", nil), parent.ConversationID, descriptor.ConversationID)
	requireBtwStatus(t, w, http.StatusNoContent)

	readers, err := database.ListBtwReaders(t.Context(), parent.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(readers) != 0 {
		t.Fatalf("dismissed readers=%#v", readers)
	}
	child, err := database.GetConversationByID(t.Context(), descriptor.ConversationID)
	if err != nil {
		t.Fatalf("dismissed child missing: %v", err)
	}
	if child.ParentConversationID == nil || *child.ParentConversationID != parent.ConversationID {
		t.Fatalf("dismissed child parent=%v want=%q", child.ParentConversationID, parent.ConversationID)
	}
	if after := listMessages(t, database, descriptor.ConversationID); len(after) != len(before) {
		t.Fatalf("dismissed child messages=%d want=%d", len(after), len(before))
	}
}

func TestBtwDismissWrongParentReturnsNotFound(t *testing.T) {
	server, database, _, parent := newBtwTest(t)
	descriptor := postBtw(t, server, parent.ConversationID, "echo: dismiss", false)
	otherParent, err := database.CreateConversation(t.Context(), nil, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	server.handleDismissBtwReader(w, httptest.NewRequest(http.MethodPost, "/", nil), otherParent.ConversationID, descriptor.ConversationID)
	requireBtwStatus(t, w, http.StatusNotFound)
}

func TestBtwSummaryIsOrdinaryMarkedChildTurn(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	descriptor := postBtw(t, server, parent.ConversationID, "echo: discuss", false)
	initial := held.waitCall(t, "echo: discuss")
	releaseAndWaitIdle(t, server, descriptor.ConversationID, initial)

	w := httptest.NewRecorder()
	server.handleSummarizeBtwReader(w, httptest.NewRequest(http.MethodPost, "/", nil), parent.ConversationID, descriptor.ConversationID)
	requireBtwStatus(t, w, http.StatusAccepted)
	messageID := btwResponseMessageID(t, w)
	summaryText := "Provide a concise, self-contained summary of this BTW discussion suitable for the parent composer. Include the answer and important supporting context; do not address the parent directly."
	held.waitCall(t, summaryText)
	rows := listMessages(t, database, descriptor.ConversationID)
	var users []generated.Message
	for _, row := range rows {
		if row.Type == string(db.MessageTypeUser) {
			users = append(users, row)
		}
	}
	if len(users) != 2 || users[0].UserData != nil || users[1].UserData == nil || *users[1].UserData != `{"btw_turn_kind":"summary"}` {
		t.Fatalf("summary user metadata=%#v", users)
	}
	if messageID != users[1].MessageID {
		t.Fatalf("summary receipt message=%q want %q", messageID, users[1].MessageID)
	}

	otherParent, err := database.CreateConversation(t.Context(), nil, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	server.handleSummarizeBtwReader(w, httptest.NewRequest(http.MethodPost, "/", nil), otherParent.ConversationID, descriptor.ConversationID)
	if w.Code != http.StatusNotFound {
		t.Fatalf("summary ownership status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestBtwRunsConversationHooksExactlyOnce(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	cwd := t.TempDir()
	if err := database.UpdateConversationCwd(t.Context(), parent.ConversationID, cwd); err != nil {
		t.Fatal(err)
	}
	newCount := filepath.Join(t.TempDir(), "new-count")
	systemCount := filepath.Join(t.TempDir(), "system-count")
	cwdJSON, _ := json.Marshal(cwd)
	newHook := fmt.Sprintf("#!/bin/sh\nprintf x >> %q\ncat >/dev/null\nprintf '%%s\\n' '{\"prompt\":\"echo: hooked\",\"model\":\"predictable\",\"cwd\":%s}'\n", newCount, cwdJSON)
	if err := os.WriteFile(filepath.Join(server.hooksDir, hookNewConversation), []byte(newHook), 0o755); err != nil {
		t.Fatal(err)
	}
	systemHook := fmt.Sprintf("#!/bin/sh\nprintf x >> %q\ncat\n", systemCount)
	if err := os.WriteFile(filepath.Join(server.hooksDir, hookSystemPrompt), []byte(systemHook), 0o755); err != nil {
		t.Fatal(err)
	}
	descriptor := postBtw(t, server, parent.ConversationID, "echo: original", false)
	held.waitCall(t, "echo: hooked")
	for path, name := range map[string]string{newCount: "new-conversation", systemCount: "system-prompt"} {
		contents, err := os.ReadFile(path)
		if err != nil || string(contents) != "x" {
			t.Fatalf("%s hook count=%q err=%v", name, contents, err)
		}
	}
	rows := listMessages(t, database, descriptor.ConversationID)
	if rows[0].LlmData == nil || !strings.Contains(*rows[0].LlmData, btwReaderRestrictionPrompt) ||
		rows[1].LlmData == nil || !strings.Contains(*rows[1].LlmData, "echo: hooked") {
		t.Fatalf("hooked initial rows=%#v", rows)
	}
}

func TestConversationDeletionWithUserInitiatedChildKeepsGenericForeignKeyFailure(t *testing.T) {
	server, database, _ := newTestServer(t)
	ctx := t.Context()
	parent, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateConversationParent(ctx, child.ConversationID, parent.ConversationID); err != nil {
		t.Fatal(err)
	}

	err = server.deleteConversation(ctx, parent.ConversationID)
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Fatalf("delete error=%v, want foreign-key failure", err)
	}
	for _, id := range []string{parent.ConversationID, child.ConversationID} {
		if _, err := database.GetConversationByID(ctx, id); err != nil {
			t.Fatalf("failed deletion removed %s: %v", id, err)
		}
	}
	preserved, err := database.GetConversationByID(ctx, child.ConversationID)
	if err != nil || preserved.ParentConversationID == nil ||
		*preserved.ParentConversationID != parent.ConversationID {
		t.Fatalf("lineage parent=%v err=%v, want %s", preserved.ParentConversationID, err, parent.ConversationID)
	}
}

func TestBtwParentDeletionWithUserInitiatedLineageRetainsGenericFailure(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	ctx := t.Context()
	if w := postBtwChat(t, server, parent.ConversationID, ChatRequest{Message: "echo: parent", Model: "predictable"}); w.Code != http.StatusAccepted {
		t.Fatalf("parent status=%d body=%s", w.Code, w.Body.String())
	}
	held.waitCall(t, "echo: parent")
	reader := postBtw(t, server, parent.ConversationID, "echo: reader", false)
	held.waitCall(t, "echo: reader")
	lineage, err := database.CreateConversation(ctx, nil, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateConversationParent(ctx, lineage.ConversationID, parent.ConversationID); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	server.handleDeleteConversation(w, httptest.NewRequest(http.MethodPost, "/", nil), parent.ConversationID)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("parent with lineage status=%d body=%s", w.Code, w.Body.String())
	}
	for _, id := range []string{parent.ConversationID, reader.ConversationID, lineage.ConversationID} {
		if _, err := database.GetConversationByID(ctx, id); err != nil {
			t.Fatalf("failed deletion removed %s: %v", id, err)
		}
	}
	preserved, err := database.GetConversationByID(ctx, lineage.ConversationID)
	if err != nil || preserved.ParentConversationID == nil ||
		*preserved.ParentConversationID != parent.ConversationID {
		t.Fatalf("lineage parent=%v err=%v, want %s", preserved.ParentConversationID, err, parent.ConversationID)
	}
	server.mu.Lock()
	readerActive := server.activeConversations[reader.ConversationID]
	parentActive := server.activeConversations[parent.ConversationID]
	server.mu.Unlock()
	if readerActive == nil || parentActive == nil {
		t.Fatalf("failed deletion removed active managers: parent=%p reader=%p", parentActive, readerActive)
	}
}

func TestBtwParentDeletionPreflightPreventsPartialReaderDeletion(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	ctx := t.Context()
	reader := postBtw(t, server, parent.ConversationID, "echo: reader", false)
	held.waitCall(t, "echo: reader")
	if _, err := database.CreateSubagentConversation(ctx, "ordinary-child", parent.ConversationID, nil); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	server.handleDeleteConversation(w, httptest.NewRequest(http.MethodPost, "/", nil), parent.ConversationID)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("blocked parent deletion status=%d body=%s", w.Code, w.Body.String())
	}
	for _, id := range []string{parent.ConversationID, reader.ConversationID} {
		if _, err := database.GetConversationByID(ctx, id); err != nil {
			t.Fatalf("preflight failure partially deleted %s: %v", id, err)
		}
	}
	server.mu.Lock()
	active := server.activeConversations[reader.ConversationID]
	server.mu.Unlock()
	if active == nil {
		t.Fatal("preflight failure removed active reader manager")
	}
}

func TestBtwLeavesOrdinaryChatBehaviorUnchanged(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	requireBtwStatus(t, postBtwChat(t, server, parent.ConversationID,
		ChatRequest{Message: "echo: ordinary", Model: "predictable"}), http.StatusAccepted)
	held.waitCall(t, "echo: ordinary")
	rows := listMessages(t, database, parent.ConversationID)
	if len(rows) < 2 || rows[1].UserData != nil {
		t.Fatalf("ordinary chat rows changed: %#v", rows)
	}

	child, err := database.CreateSubagentConversation(
		t.Context(), "ordinary-child", parent.ConversationID, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	status, err := NewSubagentRunner(server).RunSubagent(
		t.Context(), child.ConversationID, "echo: ordinary child", "predictable", "",
	)
	if err != nil || !strings.Contains(status, "message sent") {
		t.Fatalf("ordinary subagent status=%q err=%v", status, err)
	}
	if call := held.waitCall(t, "echo: ordinary child"); requestHasText(call.request, btwFrozenReferenceBegin) {
		t.Fatal("ordinary subagent received BTW decoration")
	}
}

func requestHasText(request *llm.Request, text string) bool {
	if request == nil {
		return false
	}
	for _, system := range request.System {
		if strings.Contains(system.Text, text) {
			return true
		}
	}
	for _, message := range request.Messages {
		for _, content := range message.Content {
			if strings.Contains(content.Text, text) {
				return true
			}
		}
	}
	return false
}

func TestBtwSummaryReceiptSurvivesConcurrentFollowup(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	reader := postBtw(t, server, parent.ConversationID, "echo: discuss", false)
	releaseAndWaitIdle(t, server, reader.ConversationID, held.waitCall(t, "echo: discuss"))
	manager, err := server.getOrCreateConversationManager(t.Context(), reader.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var summary *httptest.ResponseRecorder
	var followID string
	var calls sync.WaitGroup
	calls.Add(2)
	go func() {
		defer calls.Done()
		<-start
		summary = httptest.NewRecorder()
		server.handleSummarizeBtwReader(summary, httptest.NewRequest(http.MethodPost, "/", nil), parent.ConversationID, reader.ConversationID)
	}()
	go func() {
		defer calls.Done()
		<-start
		_, followID, err = manager.AcceptUserMessageWithID(t.Context(), held, "predictable", llm.UserStringMessage("echo: followup"))
	}()
	close(start)
	calls.Wait()
	if err != nil || followID == "" {
		t.Fatalf("followup receipt=%q err=%v", followID, err)
	}
	if summary.Code != http.StatusAccepted {
		t.Fatalf("summary status=%d body=%s", summary.Code, summary.Body.String())
	}
	messageID := btwResponseMessageID(t, summary)
	if messageID == followID {
		t.Fatalf("summary receipt reused followup ID %q", followID)
	}
	rows := listMessages(t, database, reader.ConversationID)
	byID := make(map[string]generated.Message)
	for _, row := range rows {
		byID[row.MessageID] = row
	}
	if byID[messageID].UserData == nil || *byID[messageID].UserData != `{"btw_turn_kind":"summary"}` {
		t.Fatalf("summary receipt resolved to wrong row: %#v", byID[messageID])
	}
	if row := byID[followID]; row.LlmData == nil || !strings.Contains(*row.LlmData, "echo: followup") || row.UserData != nil {
		t.Fatalf("followup receipt resolved to wrong row: %#v", row)
	}
}

func TestBtwDirectDeletionRacesManagerCreationWithoutResurrection(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	reader := postBtw(t, server, parent.ConversationID, "echo: race", false)
	releaseAndWaitIdle(t, server, reader.ConversationID, held.waitCall(t, "echo: race"))
	server.mu.Lock()
	old := server.activeConversations[reader.ConversationID]
	delete(server.activeConversations, reader.ConversationID)
	server.mu.Unlock()
	old.stopLoop()

	start := make(chan struct{})
	var calls sync.WaitGroup
	for range 12 {
		calls.Add(1)
		go func() {
			defer calls.Done()
			<-start
			_, _ = server.getOrCreateConversationManager(t.Context(), reader.ConversationID, "")
		}()
	}
	var deleted *httptest.ResponseRecorder
	calls.Add(1)
	go func() {
		defer calls.Done()
		<-start
		deleted = httptest.NewRecorder()
		server.handleDeleteConversation(deleted, httptest.NewRequest(http.MethodPost, "/", nil), reader.ConversationID)
	}()
	close(start)
	calls.Wait()
	if deleted.Code != http.StatusOK {
		t.Fatalf("direct delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if _, err := database.GetConversationByID(t.Context(), reader.ConversationID); err == nil {
		t.Fatal("directly deleted reader survived")
	}
	server.mu.Lock()
	active := server.activeConversations[reader.ConversationID]
	deleting := server.deletingConversations[reader.ConversationID]
	server.mu.Unlock()
	if active != nil || !deleting {
		t.Fatalf("reader resurrected: active=%p tombstone=%t", active, deleting)
	}
	if _, err := server.getOrCreateConversationManager(t.Context(), reader.ConversationID, ""); !errors.Is(err, errConversationDeleting) {
		t.Fatalf("post-delete manager lookup error=%v", err)
	}
}

func TestSubagentRunnerRejectsBtwReaderReuse(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	reader := postBtw(t, server, parent.ConversationID, "echo: reader", false)
	held.waitCall(t, "echo: reader")
	before := len(listMessages(t, database, reader.ConversationID))
	row, err := database.GetConversationByID(t.Context(), reader.ConversationID)
	if err != nil || row.Slug == nil {
		t.Fatalf("reader row=%#v err=%v", row, err)
	}
	reusedID, _, err := (&db.SubagentDBAdapter{DB: database}).GetOrCreateSubagentConversation(
		t.Context(), *row.Slug, parent.ConversationID, "",
	)
	if err != nil || reusedID != reader.ConversationID {
		t.Fatalf("slug reuse id=%q want=%q err=%v", reusedID, reader.ConversationID, err)
	}
	runner := NewSubagentRunner(server)
	if _, err := runner.RunSubagent(t.Context(), reader.ConversationID, "delegated work", "predictable", ""); err == nil ||
		!strings.Contains(err.Error(), "cannot be used as delegated subagent work") {
		t.Fatalf("error=%v", err)
	}
	if got := len(listMessages(t, database, reader.ConversationID)); got != before {
		t.Fatalf("rejected subagent work wrote messages: before=%d after=%d", before, got)
	}
}

func TestBtwSlashQueueSemantics(t *testing.T) {
	t.Run("queued question bypasses parent queue", func(t *testing.T) {
		server, database, held, parent := newBtwTest(t)
		w := postBtwChat(t, server, parent.ConversationID, ChatRequest{
			Message: " \t\r\n/btw echo: spaced",
			Model:   "predictable",
			Queue:   true,
		})
		requireBtwStatus(t, w, http.StatusAccepted)
		held.waitCall(t, "echo: spaced")
		row, err := database.GetConversationByID(t.Context(), parent.ConversationID)
		if err != nil || row.QueuedMessages != "[]" {
			t.Fatalf("queued /btw entered parent queue: %#v err=%v", row, err)
		}
		if readers, err := database.ListBtwReaders(t.Context(), parent.ConversationID); err != nil || len(readers) != 1 {
			t.Fatalf("readers=%#v err=%v", readers, err)
		}
	})
	t.Run("bare forms write and queue nothing", func(t *testing.T) {
		server, database, _, parent := newBtwTest(t)
		for _, message := range []string{"/btw", "/btw ", " \t/btw\r\n"} {
			w := postBtwChat(t, server, parent.ConversationID, ChatRequest{
				Message: message,
				Model:   "predictable",
				Queue:   true,
			})
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "requires a side question") {
				t.Fatalf("message=%q status=%d body=%s", message, w.Code, w.Body.String())
			}
		}
		row, err := database.GetConversationByID(t.Context(), parent.ConversationID)
		if err != nil || row.QueuedMessages != "[]" {
			t.Fatalf("bare /btw queued: %#v err=%v", row, err)
		}
		if rows := listMessages(t, database, parent.ConversationID); len(rows) != 0 {
			t.Fatalf("bare /btw wrote literal rows: %#v", rows)
		}
	})
}

func TestBtwReaderCannotBeForked(t *testing.T) {
	server, database, _ := newTestServer(t)
	parent, err := database.CreateConversation(t.Context(), nil, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := database.CreateBtwReaderConversation(t.Context(), db.CreateBtwReaderConversationParams{
		SlugBase:      "btw-no-fork",
		ParentID:      parent.ConversationID,
		Model:         strPtr("predictable"),
		ParentPointer: db.BtwParentPointer{Generation: 1},
		SystemMessage: llm.UserStringMessage(btwReaderRestrictionPrompt),
		UserMessage:   llm.UserStringMessage("question"),
	})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	server.handleForkConversation(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}")), reader.ConversationID)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "cannot be forked") {
		t.Fatalf("fork BTW status=%d body=%s", w.Code, w.Body.String())
	}
}

// A /btw reader can change its model and reasoning, as /model does, but not
// its fixed tools and prompt.
func TestBtwChangesOnlyModelSettings(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	reader := postBtw(t, server, parent.ConversationID, "echo: settings", false)
	releaseAndWaitIdle(t, server, reader.ConversationID, held.waitCall(t, "echo: settings"))
	manager, err := server.getOrCreateConversationManager(t.Context(), reader.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	high := "high"
	if _, err := server.changeSettings(t.Context(), manager, SettingsChange{ThinkingLevel: &high}); err != nil {
		t.Fatalf("changing reasoning: %v", err)
	}
	if _, err := server.changeSettings(t.Context(), manager, SettingsChange{ToolOverrides: map[string]string{"bash": "off"}}); !errors.Is(err, errSettingsUnavailable) {
		t.Fatalf("changing tools: got %v, want errSettingsUnavailable", err)
	}
	if got := btwSystemData(t, database, reader.ConversationID, 1); got == "" {
		t.Fatal("reader lost its system prompt")
	}
}
