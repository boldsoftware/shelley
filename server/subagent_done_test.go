package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
)

// subagentDoneFixture sets up a parent conversation with an active manager and
// a child subagent conversation whose manager comes from
// getOrCreateConversationManager. It records a final assistant text
// message into the subagent's DB as the subagent's latest response.
type subagentDoneFixture struct {
	t        *testing.T
	server   *Server
	database *db.DB
	llmSvc   *predictable.Service

	parentID    string
	parentMgr   *ConversationManager
	subagentID  string
	subagentMgr *ConversationManager
	subSlug     string
	subResponse string
}

func newSubagentDoneFixture(t *testing.T, subResponse string) *subagentDoneFixture {
	t.Helper()
	server, database, ps := newTestServer(t)

	ctx := t.Context()

	// Parent conversation.
	parentConv, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}

	parentMgr, err := server.getOrCreateConversationManager(ctx, parentConv.ConversationID, "")
	if err != nil {
		t.Fatalf("get parent manager: %v", err)
	}

	// Subagent conversation, parented to the above. Use CreateSubagentConversation
	// so ParentConversationID is set.
	slug := "sub-test"
	subConv, err := database.CreateSubagentConversation(ctx, slug, parentConv.ConversationID, nil)
	if err != nil {
		t.Fatalf("create subagent conv: %v", err)
	}

	subagentMgr, err := server.getOrCreateConversationManager(ctx, subConv.ConversationID, "")
	if err != nil {
		t.Fatalf("get subagent manager: %v", err)
	}

	// Record a final assistant text message on the subagent so lastAssistantText
	// has something to read.
	assistantMsg := llm.Message{
		Role:      llm.MessageRoleAssistant,
		Content:   []llm.Content{{Type: llm.ContentTypeText, Text: subResponse}},
		EndOfTurn: true,
	}
	if err := server.recordMessage(ctx, subConv.ConversationID, assistantMsg, llm.Usage{}, nil); err != nil {
		t.Fatalf("record subagent assistant: %v", err)
	}

	return &subagentDoneFixture{
		t:           t,
		server:      server,
		database:    database,
		llmSvc:      ps,
		parentID:    parentConv.ConversationID,
		parentMgr:   parentMgr,
		subagentID:  subConv.ConversationID,
		subagentMgr: subagentMgr,
		subSlug:     slug,
		subResponse: subResponse,
	}
}

// parentMessages returns the list of persisted messages for the parent in DB
// order (ascending sequence id).
func (f *subagentDoneFixture) parentMessages() []generated.Message {
	f.t.Helper()
	var msgs []generated.Message
	err := f.database.Queries(context.Background(), func(q *generated.Queries) error {
		var qerr error
		msgs, qerr = q.ListMessages(context.Background(), f.parentID)
		return qerr
	})
	if err != nil {
		f.t.Fatalf("list parent messages: %v", err)
	}
	return msgs
}

// Ending a turn does not wake the parent, whether the parent delegated the
// turn or the user drove the child directly; only message_parent does. A
// subagent can end many turns (waiting on background jobs, chatting with the
// user), and each wake would re-read the parent's whole context.
func TestSubagentTurnEndDoesNotWakeParent(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	ctx := t.Context()
	child, err := database.CreateSubagentConversation(ctx, "quiet-child", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSubagentRunner(server).RunSubagent(ctx, child.ConversationID, "echo: delegated turn", "predictable", ""); err != nil {
		t.Fatal(err)
	}
	releaseAndWaitIdle(t, server, child.ConversationID, held.waitCall(t, "echo: delegated turn"))
	response := postBtwChat(t, server, child.ConversationID,
		ChatRequest{Message: "echo: manual child turn", Model: "predictable"})
	if response.Code != 202 {
		t.Fatalf("manual child turn status=%d body=%s", response.Code, response.Body.String())
	}
	releaseAndWaitIdle(t, server, child.ConversationID, held.waitCall(t, "echo: manual child turn"))

	// An explicit report is the parent's first wake.
	if err := NewSubagentRunner(server).MessageParent(ctx, child.ConversationID, "explicit report"); err != nil {
		t.Fatal(err)
	}
	wrapped := `<subagent_message conversation_id="` + child.ConversationID + `" slug="quiet-child">` +
		"\nexplicit report\n</subagent_message>"
	releaseAndWaitIdle(t, server, parent.ConversationID, held.waitCall(t, wrapped))
	for _, m := range listMessages(t, database, parent.ConversationID) {
		if m.Type != string(db.MessageTypeUser) || m.UserData == nil {
			continue
		}
		data, ok, err := parseSenderMessageUserData([]byte(*m.UserData))
		if err != nil || !ok {
			continue
		}
		if data.Text != "explicit report" {
			t.Fatalf("child turn end woke the parent with %q", data.Text)
		}
	}
}

// Cleanup must never evict a conversation manager whose agent is mid-turn
// (agentWorking=true). Tool calls — e.g. a shell command that runs
// for many minutes — do not Touch the manager, so lastActivity goes
// stale even though the conversation is very much alive. Evicting it tears
// down the loop context mid-flight (cancelling in-flight tool calls and LLM
// requests) and orphans the turn.
func TestCleanupSkipsWorkingConversations(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	ctx := t.Context()

	mkStale := func(working bool) (string, *ConversationManager) {
		conv, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
		if err != nil {
			t.Fatalf("create conversation: %v", err)
		}
		mgr, err := server.getOrCreateConversationManager(ctx, conv.ConversationID, "")
		if err != nil {
			t.Fatalf("get manager: %v", err)
		}
		if working {
			mgr.SetAgentWorking(true)
		}
		mgr.mu.Lock()
		mgr.lastActivity = time.Now().Add(-time.Hour) // well past the 30-min cutoff
		mgr.mu.Unlock()
		return conv.ConversationID, mgr
	}

	workingID, _ := mkStale(true)
	idleID, _ := mkStale(false)

	server.Cleanup()

	server.mu.Lock()
	_, workingKept := server.activeConversations[workingID]
	_, idleKept := server.activeConversations[idleID]
	server.mu.Unlock()

	if !workingKept {
		t.Fatalf("Cleanup evicted a conversation whose agent is still working")
	}
	if idleKept {
		t.Fatalf("Cleanup kept a stale idle conversation; want evicted")
	}
}

// requestWithText returns the most recent request the predictable service
// received whose messages contain text.
func requestWithText(t *testing.T, ps *predictable.Service, text string) *llm.Request {
	t.Helper()
	requests := ps.GetRecentRequests()
	for i := len(requests) - 1; i >= 0; i-- {
		if requestHasText(requests[i], text) {
			return requests[i]
		}
	}
	t.Fatalf("no LLM request contained %q", text)
	return nil
}

// A subagent whose manager was first loaded by something other than the
// subagent tool (the user opening it, a background job notice, reload after
// eviction) is still a subagent: it cannot spawn subagents, and it can
// message its parent.
func TestSubagentLoadedOutsideSubagentToolKeepsItsRole(t *testing.T) {
	t.Parallel()
	server, database, ps := newTestServer(t)
	ctx := t.Context()
	parent, err := database.CreateConversation(ctx, nil, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := database.CreateSubagentConversation(ctx, "worker", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.getOrCreateConversationManager(ctx, sub.ConversationID, ""); err != nil {
		t.Fatal(err)
	}

	if _, err := NewSubagentRunner(server).RunSubagent(ctx, sub.ConversationID, "echo: delegated work", "predictable", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 15*time.Second, func() bool {
		return countByType(listMessages(t, database, sub.ConversationID), db.MessageTypeAgent) == 1 && !server.IsAgentWorking(sub.ConversationID)
	})
	tools := map[string]bool{}
	for _, tool := range requestWithText(t, ps, "echo: delegated work").Tools {
		tools[tool.Name] = true
	}
	if tools["subagent"] {
		t.Error("subagent was offered the subagent tool")
	}
	if !tools["message_parent"] {
		t.Error("subagent lacks the message_parent tool")
	}
}

// Delegated subagents are told to report with message_parent; workers, which
// lack that tool, are not.
func TestSubagentSystemPromptAsksForMessageParent(t *testing.T) {
	t.Parallel()
	server, database, ps := newTestServer(t)
	ctx := t.Context()
	parent, err := database.CreateConversation(ctx, nil, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := database.CreateSubagentConversation(ctx, "helper", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := database.CreateCommitTourWorker(ctx, parent.ConversationID, t.TempDir(), "predictable", commitTourWorkerOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer stopActiveConversationLoops(server)

	const instruction = "message_parent(end_turn=true)"
	for _, tc := range []struct {
		id, prompt string
		want       bool
	}{
		{sub.ConversationID, "echo: subagent work", true},
		{worker.ConversationID, "echo: worker work", false},
	} {
		if _, err := NewSubagentRunner(server).RunSubagent(ctx, tc.id, tc.prompt, "predictable", ""); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 15*time.Second, func() bool {
			return countByType(listMessages(t, database, tc.id), db.MessageTypeAgent) == 1 && !server.IsAgentWorking(tc.id)
		})
		var system strings.Builder
		for _, s := range requestWithText(t, ps, tc.prompt).System {
			system.WriteString(s.Text)
		}
		if got := strings.Contains(system.String(), instruction); got != tc.want {
			t.Errorf("%s: system prompt has %q = %v, want %v:\n%s", tc.prompt, instruction, got, tc.want, system.String())
		}
	}
}

func commitTourWorkerOptions() db.ConversationOptions {
	return db.ConversationOptions{
		Kind:       db.CommitTourKind,
		CommitTour: &db.CommitTourRequest{Commit: "0123456789abcdef", State: "building", RequestedAt: time.Now().UTC()},
	}
}
