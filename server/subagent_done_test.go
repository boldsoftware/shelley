package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
)

// subagentDoneFixture sets up a parent conversation with an active manager and
// a child subagent conversation whose manager has the onDone callback wired up
// by getOrCreateSubagentConversationManager. It records a final assistant text
// message into the subagent's DB so notifyParentSubagentDone has something to
// splice into the parent's history.
type subagentDoneFixture struct {
	t        *testing.T
	server   *Server
	database *db.DB
	llmSvc   *loop.PredictableService

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

	ctx := context.Background()

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
	// so ParentConversationID is set; that's what notifyParentSubagentDone keys off.
	slug := "sub-test"
	subConv, err := database.CreateSubagentConversation(ctx, slug, parentConv.ConversationID, nil)
	if err != nil {
		t.Fatalf("create subagent conv: %v", err)
	}

	subagentMgr, err := server.getOrCreateSubagentConversationManager(ctx, subConv.ConversationID)
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

// findSyntheticPair scans parent messages for a subagent tool_use immediately
// followed by a matching tool_result, returning both decoded llm.Message values
// and whether they were found.
func (f *subagentDoneFixture) findSyntheticPair() (use, result llm.Message, ok bool) {
	f.t.Helper()
	msgs := f.parentMessages()
	for i := 0; i+1 < len(msgs); i++ {
		if msgs[i].LlmData == nil || msgs[i+1].LlmData == nil {
			continue
		}
		var m1, m2 llm.Message
		if err := json.Unmarshal([]byte(*msgs[i].LlmData), &m1); err != nil {
			continue
		}
		if err := json.Unmarshal([]byte(*msgs[i+1].LlmData), &m2); err != nil {
			continue
		}
		var useID string
		for _, c := range m1.Content {
			if c.Type == llm.ContentTypeToolUse && c.ToolName == "subagent" {
				useID = c.ID
				break
			}
		}
		if useID == "" {
			continue
		}
		for _, c := range m2.Content {
			if c.Type == llm.ContentTypeToolResult && c.ToolUseID == useID {
				return m1, m2, true
			}
		}
	}
	return llm.Message{}, llm.Message{}, false
}

// fireOnDone simulates the agent transitioning from working to not working
// (which is what triggers the onDone callback wired in convo.go's
// SetAgentWorking). We toggle through true->false to exercise the real path.
func (f *subagentDoneFixture) fireOnDone() {
	f.subagentMgr.SetAgentWorking(true)
	f.subagentMgr.SetAgentWorking(false)
}

// notifySubagentDone runs the full dispatch+notify pipeline synchronously
// (capture identity, then enqueue), like dispatchSubagentDone but without
// the goroutine — so tests can assert queue state immediately after.
func (f *subagentDoneFixture) notifySubagentDone(subagentID string) {
	response, seq, ok := f.server.captureSubagentDone(subagentID)
	if !ok {
		return
	}
	f.server.notifyParentSubagentDone(subagentID, response, seq)
}

// buildSyntheticPair constructs a minimal subagent-done tool_use/tool_result
// pair like notifyParentSubagentDone's, for tests that need to enqueue a
// batch directly (bypassing producer-side invalidation).
func (f *subagentDoneFixture) buildSyntheticPair() (use, result llm.Message) {
	toolUseID := "sa_done_test_pair"
	use = llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{{
			Type: llm.ContentTypeToolUse, ID: toolUseID, ToolName: "subagent",
			ToolInput: json.RawMessage(`{}`),
		}},
	}
	result = llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{{
			Type: llm.ContentTypeToolResult, ToolUseID: toolUseID,
			ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: "stale result"}},
		}},
	}
	return use, result
}

func TestSubagentDone(t *testing.T) {
	t.Run("HappyPath_WaitFalse_NotifiesIdleParent", testSubagentDone_HappyPath)
	t.Run("SuppressedWhileWaiterActive", testSubagentDone_SuppressedWhileWaiterActive)
	t.Run("SuppressedDespiteSlugRename", testSubagentDone_SuppressedDespiteSlugRename)
	t.Run("WaiterTimeoutAfterFinishNotifies", testSubagentDone_WaiterTimeoutAfterFinishNotifies)
	t.Run("WaiterTimeoutBeforeFinishNotifiesOnce", testSubagentDone_WaiterTimeoutBeforeFinishNotifiesOnce)
	t.Run("CancellationDoesNotNotifyParent", testSubagentDone_CancellationDoesNotNotifyParent)
	t.Run("QueuedDuringDistillation", testSubagentDone_QueuedDuringDistillation)
	t.Run("WakesIdleParentLoop", testSubagentDone_WakesIdleLoop)
	t.Run("StaleNotificationCoalescedWhileParentBusy", testSubagentDone_StaleNotificationCoalesced)
	t.Run("InjectedMidTurn", testSubagentDone_InjectedMidTurn)
	t.Run("InjectionSkippedWhileDistilling", testSubagentDone_InjectionSkippedWhileDistilling)
	t.Run("StaleQueuedNotificationDroppedAfterSyncDelivery", testSubagentDone_StaleQueuedNotificationDroppedAfterSyncDelivery)
	t.Run("StaleQueuedNotificationDroppedOnWaitFalseSend", testSubagentDone_StaleQueuedNotificationDroppedOnWaitFalseSend)
	t.Run("StaleQueuedNotificationDroppedOnWaitTrueReprompt", testSubagentDone_StaleQueuedNotificationDroppedOnWaitTrueReprompt)
	t.Run("RepromptThenTimeoutStillDropsStale", testSubagentDone_RepromptThenTimeoutStillDropsStale)
	t.Run("NotificationEnqueuedMidWaitDroppedAtDelivery", testSubagentDone_NotificationEnqueuedMidWaitDroppedAtDelivery)
	t.Run("TimeoutDoesNotDropQueuedNotification", testSubagentDone_TimeoutDoesNotDropQueuedNotification)
	t.Run("StragglerNotifierSkipsAfterSyncDelivery", testSubagentDone_StragglerNotifierSkipsAfterSyncDelivery)
	t.Run("StragglerNotifierSkipsAfterWaitFalseSupersede", testSubagentDone_StragglerNotifierSkipsAfterWaitFalseSupersede)
	t.Run("DelayedNotifierCannotReannounceInjectedResponse", testSubagentDone_DelayedNotifierCannotReannounceInjectedResponse)
	t.Run("DelayedNotifierSkipsWhileNewTurnRunning", testSubagentDone_DelayedNotifierSkipsWhileNewTurnRunning)
	t.Run("ToolResultCorrectness", testSubagentDone_ToolResultCorrectness)
	t.Run("ConcurrentSubagentFinishes_BothPairsAtomic", testSubagentDone_ConcurrentFinishes)
	t.Run("LastMessageIsToolUse_FallsBackGracefully", testSubagentDone_LastMessageIsToolUse)
	t.Run("GitInfoMessageDoesNotLeakIntoNotification", testSubagentDone_GitInfoIgnored)
	t.Run("CitationMarkersStripped", testSubagentDone_CitationMarkersStripped)
	t.Run("EvictedParentManagerStillNotified", testSubagentDone_EvictedParentManagerStillNotified)
}

// Regression: the parent was blocked inside the subagent tool call (its
// lastActivity going stale because tool execution doesn't Touch the parent
// manager), the periodic Cleanup() evicted the parent's ConversationManager
// from activeConversations, and then the subagent finished.
// notifyParentSubagentDone looked the parent up in activeConversations, found
// nothing, and returned — silently dropping the completion. The parent's turn
// had also been torn down (stopLoop cancels its context), so nothing ever
// surfaced the subagent's result: the conversation hung until the user typed
// something. The fix is to (re)create the parent manager on demand, exactly
// like the wake-idle-loop path already re-hydrates a parked loop.
func testSubagentDone_EvictedParentManagerStillNotified(t *testing.T) {
	f := newSubagentDoneFixture(t, "Finished after the parent manager was evicted.")

	// Simulate Cleanup() evicting the parent manager (30 min inactivity).
	f.server.mu.Lock()
	delete(f.server.activeConversations, f.parentID)
	f.server.mu.Unlock()
	f.parentMgr.stopLoop()

	f.fireOnDone()

	// The synthetic pair must still be persisted on the parent.
	waitFor(t, 5*time.Second, func() bool {
		return hasSyntheticDonePair(t, f.parentMessages())
	})

	// And a live manager must exist again so the loop actually runs the turn.
	f.server.mu.Lock()
	_, ok := f.server.activeConversations[f.parentID]
	f.server.mu.Unlock()
	if !ok {
		t.Fatalf("expected parent manager to be recreated in activeConversations")
	}
}

// Cleanup must never evict a conversation manager whose agent is mid-turn
// (agentWorking=true). Tool calls — e.g. a wait=true subagent call that
// blocks for many minutes — do not Touch the manager, so lastActivity goes
// stale even though the conversation is very much alive. Evicting it tears
// down the loop context mid-flight (cancelling in-flight tool calls and LLM
// requests) and orphans the turn.
func TestCleanupSkipsWorkingConversations(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	ctx := context.Background()

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

// 1. Happy path: parent is idle (agentWorking=false), subagent finishes; two
// new persisted messages appear (synthetic tool_use + matching tool_result),
// and the parent's agentWorking flips to true.
func testSubagentDone_HappyPath(t *testing.T) {
	f := newSubagentDoneFixture(t, "Subagent finished successfully.")

	before := len(f.parentMessages())
	if f.parentMgr.IsAgentWorking() {
		t.Fatalf("precondition: parent should not be working")
	}

	f.fireOnDone()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(f.parentMessages()) >= before+2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n := len(f.parentMessages()); n < before+2 {
		t.Fatalf("timeout: parent had %d msgs (before=%d, want>=%d). messages:\n%s", n, before, before+2, dumpMessages(t, f.parentMessages()))
	}
	t.Logf("happy path: parent had %d msgs after (before=%d)", len(f.parentMessages()), before)

	use, result, ok := f.findSyntheticPair()
	if !ok {
		t.Fatalf("expected synthetic tool_use/tool_result pair in parent\nmessages: %s", dumpMessages(t, f.parentMessages()))
	}

	// Inspect tool_use
	var useID, toolName string
	for _, c := range use.Content {
		if c.Type == llm.ContentTypeToolUse {
			useID = c.ID
			toolName = c.ToolName
		}
	}
	if toolName != "subagent" {
		t.Errorf("expected tool name=subagent, got %q", toolName)
	}
	if useID == "" {
		t.Errorf("tool_use ID was empty")
	}

	// Inspect tool_result
	var gotText string
	var gotUseID string
	for _, c := range result.Content {
		if c.Type == llm.ContentTypeToolResult {
			gotUseID = c.ToolUseID
			for _, r := range c.ToolResult {
				if r.Type == llm.ContentTypeText {
					gotText = r.Text
				}
			}
		}
	}
	if gotUseID != useID {
		t.Errorf("tool_result ToolUseID=%q, want %q", gotUseID, useID)
	}
	if !strings.Contains(gotText, f.subResponse) {
		t.Errorf("tool_result text=%q does not contain subagent response %q", gotText, f.subResponse)
	}

	// Parent's loop should be driven: the predictable LLM service must see a
	// request whose last message is the synthetic tool_result. (Polling for
	// agentWorking==true is racy because the predictable model finishes the
	// turn faster than the test's polling interval.)
	waitFor(t, 5*time.Second, func() bool {
		last := f.llmSvc.GetLastRequest()
		if last == nil || len(last.Messages) < 2 {
			return false
		}
		cur := last.Messages[len(last.Messages)-1]
		for _, c := range cur.Content {
			if c.Type == llm.ContentTypeToolResult && strings.Contains(toolResultText(c), f.subResponse) {
				return true
			}
		}
		return false
	})
}

func toolResultText(c llm.Content) string {
	var sb strings.Builder
	for _, r := range c.ToolResult {
		if r.Type == llm.ContentTypeText {
			sb.WriteString(r.Text)
		}
	}
	return sb.String()
}

// Suppression is now decided by an in-memory synchronous-waiter slot on the
// subagent's ConversationManager (subagentWaitOwners), consulted atomically
// inside SetAgentWorking. These tests drive that mechanism directly. Note
// that the slot is keyed by the manager (immutable conversation ID), so
// suppression is correct even when the requested slug differs from the
// subagent conversation's actual (uniqueness-renamed) slug — the bug that
// the old history-parsing suppression could not handle.

// While a synchronous waiter holds a slot, a subagent finishing must NOT fire
// the async onDone notification: the waiter delivers the response via the
// tool's return value, so a synthetic pair would duplicate it.
func testSubagentDone_SuppressedWhileWaiterActive(t *testing.T) {
	f := newSubagentDoneFixture(t, "Synchronous response.")

	f.subagentMgr.registerSubagentWaiter()

	before := len(f.parentMessages())
	f.fireOnDone() // subagent finishes while the waiter holds its slot

	// Give any (erroneous) async notification a chance to land.
	time.Sleep(150 * time.Millisecond)
	if got := len(f.parentMessages()); got != before {
		t.Fatalf("expected no new parent messages while a synchronous waiter is active, got %d new", got-before)
	}
	if hasSyntheticDonePair(t, f.parentMessages()) {
		t.Fatalf("expected no synthetic pair while a synchronous waiter is active")
	}

	// The waiter delivers the result itself, so finishing reports no owed
	// async notification.
	if owed := f.subagentMgr.finishSubagentWait(true); owed {
		t.Fatalf("finishSubagentWait(delivered=true) reported notifyOwed=true; want false")
	}
	time.Sleep(50 * time.Millisecond)
	if hasSyntheticDonePair(t, f.parentMessages()) {
		t.Fatalf("expected no synthetic pair after the waiter delivered the result")
	}
}

// Regression for the original duplicate-completion bug: the parent records a
// subagent tool_use under the REQUESTED slug ("rev1"), but the subagent
// conversation was renamed for uniqueness ("rev1-4"). The old suppression
// matched the parent's recorded slug against the renamed conversation slug,
// never matched, and so fired a duplicate async completion on top of the
// wait=true tool return. The waiter-slot mechanism is keyed by the manager
// (conversation ID), so the mismatch is irrelevant and suppression holds.
func testSubagentDone_SuppressedDespiteSlugRename(t *testing.T) {
	f := newSubagentDoneFixture(t, "Synchronous response.")

	// Record a parent tool_use whose requested slug differs from the
	// subagent's actual (renamed) slug. This is exactly what the buggy
	// history-parsing suppression keyed off.
	requestedSlug := f.subSlug + "-DIFFERENT-REQUESTED"
	pendingInput, _ := json.Marshal(map[string]any{"slug": requestedSlug, "prompt": "go", "wait": true})
	if err := f.server.recordMessage(context.Background(), f.parentID, llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{{
			Type:      llm.ContentTypeToolUse,
			ID:        "toolu_renamed_pending",
			ToolName:  "subagent",
			ToolInput: pendingInput,
		}},
	}, llm.Usage{}, nil); err != nil {
		t.Fatalf("record pending tool_use: %v", err)
	}

	// A real wait=true call would hold a slot; simulate that.
	f.subagentMgr.registerSubagentWaiter()

	before := len(f.parentMessages())
	f.fireOnDone()
	time.Sleep(150 * time.Millisecond)
	if got := len(f.parentMessages()); got != before {
		t.Fatalf("expected no new parent messages despite slug rename, got %d new", got-before)
	}
	if hasSyntheticDonePair(t, f.parentMessages()) {
		t.Fatalf("expected no synthetic pair despite slug rename")
	}
	if owed := f.subagentMgr.finishSubagentWait(true); owed {
		t.Fatalf("finishSubagentWait(delivered=true) reported notifyOwed=true; want false")
	}
}

// If the subagent finishes while a waiter holds its slot (onDone suppressed),
// but the waiter then gives up WITHOUT delivering (the timeout path returns
// only a progress summary), finishSubagentWait must report notifyOwed=true so
// the caller fires the async completion. This is the finish/timeout race the
// old timeout-map tried to cover.
func testSubagentDone_WaiterTimeoutAfterFinishNotifies(t *testing.T) {
	f := newSubagentDoneFixture(t, "Finished right as the wait timed out.")

	f.subagentMgr.registerSubagentWaiter()
	f.fireOnDone() // subagent finishes; onDone suppressed by the active slot

	owed := f.subagentMgr.finishSubagentWait(false) // timeout: not delivered
	if !owed {
		t.Fatalf("finishSubagentWait(delivered=false) after a suppressed finish reported notifyOwed=false; want true")
	}

	// The caller (endWait) fires the notification when owed.
	f.notifySubagentDone(f.subagentID)
	waitFor(t, 5*time.Second, func() bool {
		return hasSyntheticDonePair(t, f.parentMessages())
	})
}

// If the waiter times out BEFORE the subagent finishes, nothing is owed yet —
// the subagent is still working. When it later finishes (no slot held), the
// normal onDone path fires exactly one notification.
func testSubagentDone_WaiterTimeoutBeforeFinishNotifiesOnce(t *testing.T) {
	f := newSubagentDoneFixture(t, "Finished after the wait already timed out.")

	f.subagentMgr.registerSubagentWaiter()
	// Subagent is still working; mark it so SetAgentWorking has a real
	// working->idle transition to make later.
	f.subagentMgr.SetAgentWorking(true)

	if owed := f.subagentMgr.finishSubagentWait(false); owed {
		t.Fatalf("finishSubagentWait(delivered=false) while still working reported notifyOwed=true; want false")
	}

	// Now the subagent actually finishes: no slot held, onDone fires.
	f.subagentMgr.SetAgentWorking(false)
	waitFor(t, 5*time.Second, func() bool {
		return hasSyntheticDonePair(t, f.parentMessages())
	})

	// Exactly one synthetic completion fires (the parent loop may append its
	// own reply afterward, which is why we count synthetic pairs rather than
	// raw message deltas).
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if n := countSyntheticDonePairs(t, f.parentMessages()); n > 1 {
			t.Fatalf("expected exactly one synthetic done pair after late finish, got %d", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n := countSyntheticDonePairs(t, f.parentMessages()); n != 1 {
		t.Fatalf("expected exactly one synthetic done pair after late finish, got %d", n)
	}
}

// Cancelling a subagent's in-flight turn (e.g. a resend to a busy subagent, or
// a user-initiated stop) records a synthetic "[Operation cancelled]"
// end-of-turn message that flips agentWorking→idle. That transition must NOT
// fire onDone: a cancellation is not a completion, and notifying the parent
// here produces a spurious subagent-done pair (and, when a resend's new turn
// later finishes, a duplicate). With no waiter slot held during cancel, the
// only thing keeping onDone quiet is the cancelling guard.
func testSubagentDone_CancellationDoesNotNotifyParent(t *testing.T) {
	f := newSubagentDoneFixture(t, "Should never reach the parent.")

	// Bring the subagent's loop up so CancelConversation has something to tear
	// down (it returns early when loop==nil).
	if err := f.subagentMgr.ensureLoop(f.llmSvc, "predictable"); err != nil {
		t.Fatalf("ensureLoop subagent: %v", err)
	}
	f.subagentMgr.SetAgentWorking(true)

	before := len(f.parentMessages())
	if err := f.subagentMgr.CancelConversation(context.Background()); err != nil {
		t.Fatalf("CancelConversation: %v", err)
	}

	// Give any (erroneous) async notification a chance to land on the parent.
	time.Sleep(200 * time.Millisecond)
	if n := countSyntheticDonePairs(t, f.parentMessages()); n != 0 {
		t.Fatalf("cancellation fired %d subagent-done notification(s) to the parent; want 0\nmessages:\n%s", n, dumpMessages(t, f.parentMessages()))
	}
	if got := len(f.parentMessages()); got != before {
		t.Fatalf("cancellation added %d parent message(s); want 0", got-before)
	}

	// The subagent itself must be idle after cancel.
	if f.subagentMgr.IsAgentWorking() {
		t.Fatalf("subagent still working after CancelConversation")
	}
}

// countSyntheticDonePairs returns how many synthetic subagent-done tool_use/
// tool_result pairs (sa_done_ prefixed) appear in msgs. Used to assert that
// exactly one async completion fires, ignoring any normal parent replies the
// loop appends afterward.
func countSyntheticDonePairs(t *testing.T, msgs []generated.Message) int {
	t.Helper()
	n := 0
	for i := 0; i+1 < len(msgs); i++ {
		if msgs[i].LlmData == nil || msgs[i+1].LlmData == nil {
			continue
		}
		var use, result llm.Message
		if err := json.Unmarshal([]byte(*msgs[i].LlmData), &use); err != nil {
			continue
		}
		if err := json.Unmarshal([]byte(*msgs[i+1].LlmData), &result); err != nil {
			continue
		}
		var useID string
		for _, c := range use.Content {
			if c.Type == llm.ContentTypeToolUse && c.ToolName == "subagent" && strings.HasPrefix(c.ID, "sa_done_") {
				useID = c.ID
			}
		}
		if useID == "" {
			continue
		}
		for _, c := range result.Content {
			if c.Type == llm.ContentTypeToolResult && c.ToolUseID == useID {
				n++
			}
		}
	}
	return n
}

func hasSyntheticDonePair(t *testing.T, msgs []generated.Message) bool {
	t.Helper()
	for i := 0; i+1 < len(msgs); i++ {
		if msgs[i].LlmData == nil || msgs[i+1].LlmData == nil {
			continue
		}
		var use, result llm.Message
		if err := json.Unmarshal([]byte(*msgs[i].LlmData), &use); err != nil {
			continue
		}
		if err := json.Unmarshal([]byte(*msgs[i+1].LlmData), &result); err != nil {
			continue
		}
		var useID string
		for _, c := range use.Content {
			if c.Type == llm.ContentTypeToolUse && c.ToolName == "subagent" && strings.HasPrefix(c.ID, "sa_done_") {
				useID = c.ID
			}
		}
		if useID == "" {
			continue
		}
		for _, c := range result.Content {
			if c.Type == llm.ContentTypeToolResult && c.ToolUseID == useID {
				return true
			}
		}
	}
	return false
}

// 3. Parent distilling: notification waits in the pending-batch queue until
// distillation ends, then drains. No more drop-on-distilling — the single
// queue serializes subagent-done batches with distillation just like it
// already does for user messages.
func testSubagentDone_QueuedDuringDistillation(t *testing.T) {
	f := newSubagentDoneFixture(t, "Some response.")

	f.parentMgr.SetDistilling(true)

	before := len(f.parentMessages())
	f.notifySubagentDone(f.subagentID)

	// While distilling, the synthetic pair must NOT yet be persisted.
	if got := len(f.parentMessages()); got != before {
		t.Fatalf("expected no new parent messages while distilling, got %d new", got-before)
	}
	if _, _, ok := f.findSyntheticPair(); ok {
		t.Fatalf("unexpected synthetic pair recorded while distilling")
	}

	// End distillation — this is what runDistillNewGeneration's defer does
	// in real code (SetDistilling(false) then drainPendingMessages).
	f.parentMgr.SetDistilling(false)
	go f.parentMgr.drainPendingMessages(f.server)

	// Now the synthetic pair must show up.
	waitFor(t, 5*time.Second, func() bool {
		_, _, ok := f.findSyntheticPair()
		return ok
	})
}

// 4. Wakes idle parent: tear down the parent's loop, finish the subagent.
// notifyParentSubagentDone must Hydrate + ensureLoop the parent back up,
// persist the synthetic pair to the DB, queue them onto the loop, and the
// loop must actually fire an LLM request whose last two history entries are
// the synthetic tool_use and tool_result.
func testSubagentDone_WakesIdleLoop(t *testing.T) {
	f := newSubagentDoneFixture(t, "Background work done.")

	// Bring the parent's loop up once with the predictable service so model is
	// recorded and toolset is built; then drop it as if the parent went idle.
	if err := f.parentMgr.ensureLoop(f.llmSvc, "predictable"); err != nil {
		t.Fatalf("ensureLoop initial: %v", err)
	}
	f.parentMgr.ResetLoop()

	f.parentMgr.mu.Lock()
	hasLoop := f.parentMgr.loop != nil
	f.parentMgr.mu.Unlock()
	if hasLoop {
		t.Fatalf("expected parent loop to be torn down")
	}

	f.llmSvc.ClearRequests()

	f.fireOnDone()

	// Persisted synthetic pair shows up.
	waitFor(t, 5*time.Second, func() bool {
		_, _, ok := f.findSyntheticPair()
		return ok
	})

	// Parent loop should be running again.
	waitFor(t, 5*time.Second, func() bool {
		f.parentMgr.mu.Lock()
		defer f.parentMgr.mu.Unlock()
		return f.parentMgr.loop != nil
	})

	// And the predictable LLM service should have been invoked, with the last
	// two history entries being our synthetic tool_use and tool_result.
	waitFor(t, 5*time.Second, func() bool {
		reqs := f.llmSvc.GetRecentRequests()
		if len(reqs) == 0 {
			return false
		}
		last := reqs[len(reqs)-1]
		if len(last.Messages) < 2 {
			return false
		}
		n := len(last.Messages)
		prev := last.Messages[n-2]
		cur := last.Messages[n-1]
		var prevUseID string
		for _, c := range prev.Content {
			if c.Type == llm.ContentTypeToolUse && c.ToolName == "subagent" {
				prevUseID = c.ID
			}
		}
		if prevUseID == "" {
			return false
		}
		for _, c := range cur.Content {
			if c.Type == llm.ContentTypeToolResult && c.ToolUseID == prevUseID {
				return true
			}
		}
		return false
	})
}

// 5. Tool result correctness: tool_use ID matches tool_result ToolUseID, and
// long subagent text is truncated to 500 chars (plus suffix) in the tool_result.
func testSubagentDone_ToolResultCorrectness(t *testing.T) {
	long := strings.Repeat("x", 800)
	f := newSubagentDoneFixture(t, long)

	f.fireOnDone()

	waitFor(t, 5*time.Second, func() bool {
		_, _, ok := f.findSyntheticPair()
		return ok
	})

	use, result, ok := f.findSyntheticPair()
	if !ok {
		t.Fatalf("missing synthetic pair")
	}

	var useID string
	for _, c := range use.Content {
		if c.Type == llm.ContentTypeToolUse {
			useID = c.ID
		}
	}
	if useID == "" || !strings.HasPrefix(useID, "sa_done_") {
		t.Errorf("unexpected tool_use ID: %q", useID)
	}

	var gotText, gotUseID string
	for _, c := range result.Content {
		if c.Type == llm.ContentTypeToolResult {
			gotUseID = c.ToolUseID
			for _, r := range c.ToolResult {
				if r.Type == llm.ContentTypeText {
					gotText = r.Text
				}
			}
		}
	}
	if gotUseID != useID {
		t.Errorf("ToolUseID mismatch: use=%q result=%q", useID, gotUseID)
	}

	// The full 800-char text must not be present verbatim; the truncated body
	// (first 500 "x"s + suffix) must be present.
	if strings.Contains(gotText, long) {
		t.Errorf("expected subagent text to be truncated, but full 800-char body is present")
	}
	if !strings.Contains(gotText, strings.Repeat("x", 500)) {
		t.Errorf("expected first 500 chars of subagent text to be present, got %q", truncForLog(gotText))
	}
	if !strings.Contains(gotText, "...") {
		t.Errorf("expected truncation suffix '...' in tool_result text")
	}
}

// testSubagentDone_InjectedMidTurn reproduces the "late subagent reply wastes
// a parent turn" problem observed in production — a subagent finishing while
// the parent is mid-turn used to queue its completion notification until the
// parent's turn ended (potentially hours later), wasting a full parent turn
// (plus a user-facing notification) acknowledging old news — and verifies the
// fix: the notification is INJECTED into the parent's running turn at the
// next LLM round, so the parent reacts immediately.
//
// The parent runs a real turn (predictable model, bash sleep tool round);
// while the tool executes, the subagent finishes. The synthetic pair must
// land in the parent's history BEFORE the turn's final assistant message —
// i.e. within the same turn — exactly once, leaving nothing queued for the
// end-of-turn drain.
func testSubagentDone_InjectedMidTurn(t *testing.T) {
	f := newSubagentDoneFixture(t, "review complete: LGTM")
	ctx := context.Background()

	// Start a real parent turn whose first round calls the bash tool with a
	// sleep, giving the subagent completion a window to arrive mid-turn.
	userMsg := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "bash: sleep 2"}},
	}
	if _, err := f.parentMgr.AcceptUserMessage(ctx, f.llmSvc, "predictable", userMsg); err != nil {
		t.Fatalf("AcceptUserMessage: %v", err)
	}

	// Wait until the bash tool_use is recorded: the tool round is running.
	waitFor(t, 5*time.Second, func() bool {
		for _, m := range f.parentMessages() {
			if m.LlmData == nil {
				continue
			}
			var lm llm.Message
			if err := json.Unmarshal([]byte(*m.LlmData), &lm); err != nil {
				continue
			}
			for _, c := range lm.Content {
				if c.Type == llm.ContentTypeToolUse && c.ToolName == "bash" {
					return true
				}
			}
		}
		return false
	})
	if !f.parentMgr.IsAgentWorking() {
		t.Fatalf("parent should be mid-turn")
	}

	// Subagent finishes now, mid-turn. The notification is queued (parent is
	// working) and must be injected at the next LLM round instead of waiting
	// for the turn to end.
	f.notifySubagentDone(f.subagentID)

	// Wait for the turn to complete.
	waitFor(t, 15*time.Second, func() bool {
		return !f.parentMgr.IsAgentWorking()
	})

	msgs := f.parentMessages()
	if n := countSyntheticDonePairs(t, msgs); n != 1 {
		t.Fatalf("expected exactly one synthetic done pair, got %d\n%s", n, dumpMessages(t, msgs))
	}

	// The pair must sit BEFORE the final end-of-turn assistant message: it was
	// spliced into the running turn, not appended as a new turn afterwards.
	pairIdx, endIdx := -1, -1
	for i, m := range msgs {
		if m.LlmData == nil {
			continue
		}
		var lm llm.Message
		if err := json.Unmarshal([]byte(*m.LlmData), &lm); err != nil {
			continue
		}
		for _, c := range lm.Content {
			if c.Type == llm.ContentTypeToolUse && strings.HasPrefix(c.ID, "sa_done_") {
				pairIdx = i
			}
		}
		if lm.Role == llm.MessageRoleAssistant && lm.EndOfTurn && endIdx == -1 {
			endIdx = i // FIRST end-of-turn: where the original turn finished
		}
	}
	if pairIdx == -1 || endIdx == -1 {
		t.Fatalf("missing pair (%d) or end-of-turn message (%d)\n%s", pairIdx, endIdx, dumpMessages(t, msgs))
	}
	if pairIdx > endIdx {
		t.Fatalf("synthetic pair (idx %d) landed AFTER the turn ended (idx %d): not injected mid-turn\n%s",
			pairIdx, endIdx, dumpMessages(t, msgs))
	}

	// Nothing left queued: the end-of-turn drain must not re-deliver.
	if n := countPendingSubagentDone(f.parentMgr, f.subagentID); n != 0 {
		t.Fatalf("expected empty pending queue after injection, got %d", n)
	}
}

// testSubagentDone_InjectionSkippedWhileDistilling verifies that mid-turn
// injection stays hands-off while the conversation is being rewritten by
// distillation: takeInjectableSubagentDone returns nothing and leaves the
// batch queued for the post-distillation drain (whose delivery is covered by
// QueuedDuringDistillation).
func testSubagentDone_InjectionSkippedWhileDistilling(t *testing.T) {
	f := newSubagentDoneFixture(t, "subagent response")
	ctx := context.Background()

	f.parentMgr.SetDistilling(true)
	defer f.parentMgr.SetDistilling(false)

	f.notifySubagentDone(f.subagentID)
	waitFor(t, 5*time.Second, func() bool {
		return countPendingSubagentDone(f.parentMgr, f.subagentID) == 1
	})

	f.parentMgr.mu.Lock()
	generation := f.parentMgr.loopGeneration
	f.parentMgr.mu.Unlock()
	if msgs := f.parentMgr.takeInjectableSubagentDone(ctx, generation); len(msgs) != 0 {
		t.Fatalf("expected no injectable messages while distilling, got %d", len(msgs))
	}
	if n := countPendingSubagentDone(f.parentMgr, f.subagentID); n != 1 {
		t.Fatalf("expected batch to stay queued while distilling, got %d", n)
	}
}

// testSubagentDone_StaleQueuedNotificationDroppedAfterSyncDelivery reproduces
// the "late subagent reply wastes a parent turn" bug observed in production:
//
//  1. The parent is mid-turn when the subagent finishes, so the completion
//     notification is queued on the parent's pending-batch queue (it cannot
//     drain until the parent's turn ends — potentially much later).
//  2. Still within the same turn, the parent polls the subagent with a
//     wait=true tool call ("status check — give me your findings") and
//     receives the full response synchronously via the tool result.
//  3. When the parent's turn finally ends, the queued notification drains
//     anyway, splicing a synthetic "subagent finished" pair into history and
//     burning a full LLM turn (plus a user-facing notification) for the
//     parent to say "that's the same answer I already have; nothing to do."
//
// Once the response has been delivered synchronously, the queued notification
// is stale — the same supersession policy enqueueBatch already applies when a
// newer notification for the same subagent arrives. It must be dropped.
func testSubagentDone_StaleQueuedNotificationDroppedAfterSyncDelivery(t *testing.T) {
	f := newSubagentDoneFixture(t, "first-turn response")
	ctx := context.Background()

	// Parent is mid-turn: enqueued subagent-done batches wait in
	// pendingBatches rather than draining immediately.
	f.parentMgr.SetAgentWorking(true)

	// Subagent finishes while the parent is busy -> queued notification.
	f.notifySubagentDone(f.subagentID)
	waitFor(t, 5*time.Second, func() bool {
		return countPendingSubagentDone(f.parentMgr, f.subagentID) == 1
	})

	// Parent (still mid-turn) polls the subagent with wait=true and gets the
	// response synchronously via the tool result.
	runner := NewSubagentRunner(f.server)
	res, err := runner.RunSubagent(ctx, f.subagentID, "echo: foo", true, 10*time.Second, "predictable", "")
	if err != nil {
		t.Fatalf("RunSubagent(wait=true): %v", err)
	}
	if !strings.Contains(res, "foo") {
		t.Fatalf("expected synchronous response 'foo', got %q", res)
	}

	// The queued notification is now stale — the parent already has the
	// subagent's answer — and must have been dropped.
	if n := countPendingSubagentDone(f.parentMgr, f.subagentID); n != 0 {
		t.Fatalf("expected stale queued subagent-done batch to be dropped after synchronous delivery, still have %d", n)
	}

	// Parent's turn ends. Draining must not splice a synthetic done pair.
	f.parentMgr.SetAgentWorking(false)
	go f.parentMgr.drainPendingMessages(f.server)
	time.Sleep(150 * time.Millisecond) // let any erroneous pair land
	if n := countSyntheticDonePairs(t, f.parentMessages()); n != 0 {
		t.Fatalf("expected no synthetic done pairs on parent after synchronous delivery, got %d", n)
	}
}

// testSubagentDone_StaleQueuedNotificationDroppedOnWaitFalseSend covers the
// wait=false flavor of the same staleness: a notification for the subagent's
// PREVIOUS turn is queued on the busy parent, and the parent then sends the
// subagent new work (wait=false to a busy subagent). The new work supersedes
// the queued notification — the same "only the newest matters" policy
// enqueueBatch applies between two queued notifications — so it must be
// dropped; the new turn's completion will enqueue a fresh one.
func testSubagentDone_StaleQueuedNotificationDroppedOnWaitFalseSend(t *testing.T) {
	f := newSubagentDoneFixture(t, "previous-turn response")

	// Parent is mid-turn.
	f.parentMgr.SetAgentWorking(true)

	// Subagent finished its previous turn while the parent was busy.
	f.notifySubagentDone(f.subagentID)
	waitFor(t, 5*time.Second, func() bool {
		return countPendingSubagentDone(f.parentMgr, f.subagentID) == 1
	})

	// The subagent is now busy on another turn (simulated), and the parent
	// queues new work for it with wait=false.
	if err := f.subagentMgr.ensureLoop(f.llmSvc, "predictable"); err != nil {
		t.Fatalf("ensureLoop subagent: %v", err)
	}
	f.subagentMgr.SetAgentWorking(true)

	runner := NewSubagentRunner(f.server)
	res, err := runner.RunSubagent(context.Background(), f.subagentID, "do the next thing", false, time.Minute, "predictable", "")
	if err != nil {
		t.Fatalf("RunSubagent(wait=false): %v", err)
	}
	if !strings.Contains(res, "queued") {
		t.Fatalf("expected a queued status, got %q", res)
	}

	// The stale previous-turn notification must be gone.
	if n := countPendingSubagentDone(f.parentMgr, f.subagentID); n != 0 {
		t.Fatalf("expected stale queued subagent-done batch to be dropped on wait=false send, still have %d", n)
	}
}

// testSubagentDone_StaleQueuedNotificationDroppedOnWaitTrueReprompt covers
// the wait=true re-prompt path: a notification for the subagent's previous
// turn is queued on the busy parent, the subagent is busy with another turn,
// and the parent sends a follow-up with wait=true. Once the follow-up is
// accepted it supersedes the queued notification, which must be dropped
// before waitForResponse delivers (or times out on) the new turn.
func testSubagentDone_StaleQueuedNotificationDroppedOnWaitTrueReprompt(t *testing.T) {
	f := newSubagentDoneFixture(t, "previous-turn response")

	// Parent is mid-turn.
	f.parentMgr.SetAgentWorking(true)

	// Subagent finished its previous turn while the parent was busy.
	f.notifySubagentDone(f.subagentID)
	waitFor(t, 5*time.Second, func() bool {
		return countPendingSubagentDone(f.parentMgr, f.subagentID) == 1
	})

	// The subagent is busy with another (simulated) turn that finishes
	// shortly; the parent re-prompts with wait=true.
	if err := f.subagentMgr.ensureLoop(f.llmSvc, "predictable"); err != nil {
		t.Fatalf("ensureLoop subagent: %v", err)
	}
	f.subagentMgr.SetAgentWorking(true)
	go func() {
		time.Sleep(200 * time.Millisecond)
		f.subagentMgr.SetAgentWorking(false)
	}()

	runner := NewSubagentRunner(f.server)
	res, err := runner.RunSubagent(context.Background(), f.subagentID, "echo: foo", true, 10*time.Second, "predictable", "")
	if err != nil {
		t.Fatalf("RunSubagent(wait=true): %v", err)
	}
	if !strings.Contains(res, "foo") {
		t.Fatalf("expected the follow-up response 'foo', got %q", res)
	}

	// The stale previous-turn notification must be gone.
	if n := countPendingSubagentDone(f.parentMgr, f.subagentID); n != 0 {
		t.Fatalf("expected stale queued subagent-done batch to be dropped on wait=true re-prompt, still have %d", n)
	}
}

// testSubagentDone_RepromptThenTimeoutStillDropsStale pins the wait=true
// re-prompt drop specifically: the follow-up is sent (superseding the queued
// previous-turn notification) but the wait then TIMES OUT, so the
// synchronous-delivery drop never runs. The stale notification must already
// be gone — the parent asked for the new turn's outcome, which will arrive
// as a fresh notification when that turn finishes.
func testSubagentDone_RepromptThenTimeoutStillDropsStale(t *testing.T) {
	f := newSubagentDoneFixture(t, "previous-turn response")

	// Parent is mid-turn.
	f.parentMgr.SetAgentWorking(true)

	// Subagent finished its previous turn while the parent was busy.
	f.notifySubagentDone(f.subagentID)
	waitFor(t, 5*time.Second, func() bool {
		return countPendingSubagentDone(f.parentMgr, f.subagentID) == 1
	})

	// Re-prompt with wait=true. The follow-up turn (predictable "delay: 4")
	// outlives the 1s deadline, so waitForResponse returns a progress summary
	// without ever reaching the delivery path.
	runner := NewSubagentRunner(f.server)
	res, err := runner.RunSubagent(context.Background(), f.subagentID, "delay: 4", true, time.Second, "predictable", "")
	if err != nil {
		t.Fatalf("RunSubagent(wait=true, timeout): %v", err)
	}
	if !strings.Contains(res, "still working") {
		t.Fatalf("expected a timeout/progress response, got %q", res)
	}

	// The stale previous-turn notification must have been dropped when the
	// follow-up was accepted, even though nothing was delivered. (The
	// follow-up turn is still running — its fresh notification arrives only
	// when it finishes, well after this check.)
	if n := countPendingSubagentDone(f.parentMgr, f.subagentID); n != 0 {
		t.Fatalf("expected stale queued subagent-done batch to be dropped on re-prompt despite timeout, still have %d", n)
	}
}

// testSubagentDone_NotificationEnqueuedMidWaitDroppedAtDelivery pins the
// synchronous-delivery drop in waitForResponse: a notification that lands on
// the parent's queue AFTER the wait=true call has already sent its prompt
// (e.g. an in-flight notifyParentSubagentDone goroutine scheduled just before
// the waiter slot was registered) must still be scrubbed when the call
// delivers the final answer. The other two (pre-send) drop sites have already
// run by then and cannot help.
func testSubagentDone_NotificationEnqueuedMidWaitDroppedAtDelivery(t *testing.T) {
	f := newSubagentDoneFixture(t, "previous-turn response")

	// Parent is mid-turn, so injected notifications stay queued.
	f.parentMgr.SetAgentWorking(true)

	// Kick off a wait=true call whose turn takes ~1s (predictable "delay:").
	type result struct {
		res string
		err error
	}
	done := make(chan result, 1)
	runner := NewSubagentRunner(f.server)
	go func() {
		res, err := runner.RunSubagent(context.Background(), f.subagentID, "delay: 1", true, 10*time.Second, "predictable", "")
		done <- result{res, err}
	}()

	// Wait until the subagent's turn is running AND the re-prompt has
	// recorded its supersession watermark (which happens just after
	// AcceptUserMessage returns — agentWorking flips true slightly earlier,
	// inside AcceptUserMessage, so waiting on the watermark avoids racing
	// the gap between the two).
	waitFor(t, 5*time.Second, func() bool {
		return f.subagentMgr.IsAgentWorking() && f.subagentMgr.handledSeq() > 0
	})

	// A straggler notifier goroutine for the PREVIOUS turn lands mid-wait.
	// The re-prompt already recorded the supersession watermark, so the
	// straggler is skipped at enqueue time (producer-side invalidation) —
	// nothing may appear in the queue.
	f.notifySubagentDone(f.subagentID)
	if n := countPendingSubagentDone(f.parentMgr, f.subagentID); n != 0 {
		t.Fatalf("expected mid-wait straggler notification to be skipped at enqueue, have %d queued", n)
	}

	// Separately pin the delivery-time scrub (drop site 3): force a batch
	// past the producer-side check (nil isStale — e.g. a notifier built
	// before the subagent manager was active) while the wait is in flight.
	use, res := f.buildSyntheticPair()
	f.parentMgr.EnqueueSubagentDone(f.server, "predictable", f.subagentID, use, res, nil)
	waitFor(t, 5*time.Second, func() bool {
		return countPendingSubagentDone(f.parentMgr, f.subagentID) == 1
	})

	r := <-done
	if r.err != nil {
		t.Fatalf("RunSubagent(wait=true): %v", r.err)
	}
	if !strings.Contains(r.res, "Delayed") {
		t.Fatalf("expected the delayed response, got %q", r.res)
	}

	// The mid-wait notification is stale relative to the just-delivered
	// response and must have been dropped at delivery time.
	if n := countPendingSubagentDone(f.parentMgr, f.subagentID); n != 0 {
		t.Fatalf("expected mid-wait queued subagent-done batch to be dropped at delivery, still have %d", n)
	}
}

// testSubagentDone_TimeoutDoesNotDropQueuedNotification is the negative case:
// a wait=true call that TIMES OUT delivers only a progress summary — the
// parent does NOT have the subagent's answer — so a queued notification for
// this subagent must survive the call.
func testSubagentDone_TimeoutDoesNotDropQueuedNotification(t *testing.T) {
	f := newSubagentDoneFixture(t, "previous-turn response")

	// Parent is mid-turn.
	f.parentMgr.SetAgentWorking(true)

	// Subagent finished its previous turn while the parent was busy.
	f.notifySubagentDone(f.subagentID)
	waitFor(t, 5*time.Second, func() bool {
		return countPendingSubagentDone(f.parentMgr, f.subagentID) == 1
	})

	// The subagent is busy with a long-running turn; the parent polls with
	// wait=true and a deadline that expires while the turn is in flight.
	if err := f.subagentMgr.ensureLoop(f.llmSvc, "predictable"); err != nil {
		t.Fatalf("ensureLoop subagent: %v", err)
	}
	f.subagentMgr.SetAgentWorking(true)
	defer f.subagentMgr.SetAgentWorking(false)

	runner := NewSubagentRunner(f.server)
	res, err := runner.RunSubagent(context.Background(), f.subagentID, "echo: foo", true, 700*time.Millisecond, "predictable", "")
	if err != nil {
		t.Fatalf("RunSubagent(wait=true, timeout): %v", err)
	}
	if !strings.Contains(res, "still working") {
		t.Fatalf("expected a timeout/progress response, got %q", res)
	}

	// The queued notification still carries information the parent never
	// received; it must NOT have been dropped.
	if n := countPendingSubagentDone(f.parentMgr, f.subagentID); n != 1 {
		t.Fatalf("expected queued subagent-done batch to survive a timed-out wait, have %d", n)
	}
}

// testSubagentDone_StragglerNotifierSkipsAfterSyncDelivery pins the
// producer-side invalidation: a notifyParentSubagentDone goroutine that is
// delayed until AFTER a wait=true call has delivered the same response (and
// after every queue-scrub has run) must not enqueue a duplicate. The
// wait=true delivery records the response's sequence id on the subagent
// manager (markSyncDelivered); the straggler's isStale closure sees it at
// enqueue time and discards the batch.
func testSubagentDone_StragglerNotifierSkipsAfterSyncDelivery(t *testing.T) {
	f := newSubagentDoneFixture(t, "the response")
	ctx := context.Background()

	// Parent is mid-turn, so any enqueued notification would stay queued.
	f.parentMgr.SetAgentWorking(true)

	// The parent polls the subagent with wait=true and receives the response
	// synchronously. This marks the response's sequence id as delivered and
	// scrubs the (empty) queue.
	runner := NewSubagentRunner(f.server)
	res, err := runner.RunSubagent(ctx, f.subagentID, "echo: foo", true, 10*time.Second, "predictable", "")
	if err != nil {
		t.Fatalf("RunSubagent(wait=true): %v", err)
	}
	if !strings.Contains(res, "foo") {
		t.Fatalf("expected synchronous response, got %q", res)
	}

	// Straggler: a delayed notifier goroutine for the previous turn fires
	// only now — after all drop points have run. It must skip enqueueing.
	f.notifySubagentDone(f.subagentID)

	// notifyParentSubagentDone runs synchronously here, so the queue state is
	// final: nothing may be queued.
	if n := countPendingSubagentDone(f.parentMgr, f.subagentID); n != 0 {
		t.Fatalf("expected straggler notification to be skipped after sync delivery, have %d queued", n)
	}
}

// testSubagentDone_StragglerNotifierSkipsAfterWaitFalseSupersede pins the
// producer-side invalidation for the wait=false supersession path with the
// worst-case interleaving: the completion for the PREVIOUS turn was already
// CAPTURED (dispatch time — identity fixed) but its enqueue is delayed until
// after the parent has sent the subagent new work (which scrubbed the queue
// and recorded the supersession watermark). The late enqueue must skip: the
// watermark covers the captured response's sequence id.
func testSubagentDone_StragglerNotifierSkipsAfterWaitFalseSupersede(t *testing.T) {
	f := newSubagentDoneFixture(t, "previous-turn response")
	ctx := context.Background()

	// Parent is mid-turn, so an enqueued notification would stay queued.
	f.parentMgr.SetAgentWorking(true)

	// Dispatch-time capture happens NOW, before the supersession — the
	// notifier goroutine then stalls (simulated by holding the values).
	response, seq, ok := f.server.captureSubagentDone(f.subagentID)
	if !ok {
		t.Fatalf("expected capture to succeed while subagent idle")
	}

	// Parent sends the subagent new work (wait=false). This records the
	// supersession watermark for the existing response and scrubs the queue.
	runner := NewSubagentRunner(f.server)
	if _, err := runner.RunSubagent(ctx, f.subagentID, "delay: 5", false, 0, "predictable", ""); err != nil {
		t.Fatalf("RunSubagent(wait=false): %v", err)
	}

	// The stalled notifier finally enqueues. The watermark must reject it.
	f.server.notifyParentSubagentDone(f.subagentID, response, seq)
	if n := countPendingSubagentDone(f.parentMgr, f.subagentID); n != 0 {
		t.Fatalf("expected straggler notification to be skipped after wait=false supersede, have %d queued", n)
	}
}

// testSubagentDone_DelayedNotifierCannotReannounceInjectedResponse pins the
// claim-based dedupe (notifiedResponseSeq): a notifier for turn A stalls;
// turn B's notifier announces B's response, which is enqueued and then LEAVES
// the queue (mid-turn injection/drain) — so queue coalescing can no longer
// help. When the stalled notifier finally runs, it re-reads the subagent's
// latest response (B, already announced) and must skip: only the first claim
// for a given response seq wins.
func testSubagentDone_DelayedNotifierCannotReannounceInjectedResponse(t *testing.T) {
	f := newSubagentDoneFixture(t, "turn-B response")

	// Parent idle: the first notification drains (leaves the queue)
	// immediately, like a mid-turn injection would.
	f.notifySubagentDone(f.subagentID)
	waitFor(t, 5*time.Second, func() bool {
		return countSyntheticDonePairs(t, f.parentMessages()) == 1
	})
	waitFor(t, 5*time.Second, func() bool {
		return countPendingSubagentDone(f.parentMgr, f.subagentID) == 0
	})

	// Parent is now mid-turn (the drained notification woke its loop, or
	// simulate it); the stalled duplicate notifier for the SAME response
	// finally runs. It must claim-fail and enqueue nothing.
	f.parentMgr.SetAgentWorking(true)
	f.notifySubagentDone(f.subagentID)
	if n := countPendingSubagentDone(f.parentMgr, f.subagentID); n != 0 {
		t.Fatalf("expected delayed duplicate notifier to be skipped by claim, have %d queued", n)
	}
	f.parentMgr.SetAgentWorking(false)

	// And no second synthetic pair may ever land.
	if n := countSyntheticDonePairs(t, f.parentMessages()); n != 1 {
		t.Fatalf("expected exactly one synthetic pair, got %d", n)
	}
}

// testSubagentDone_DelayedNotifierSkipsWhileNewTurnRunning pins the
// working-check in notifyParentSubagentDone: a notifier for finished turn A
// that only runs after turn B has STARTED (subagent working again) must
// skip — reading "the latest agent row" mid-turn would announce an
// in-progress turn (often a bare tool_use → "(no textual response)") as
// finished. Turn B's own completion notifies with the real result.
func testSubagentDone_DelayedNotifierSkipsWhileNewTurnRunning(t *testing.T) {
	f := newSubagentDoneFixture(t, "turn-A response")

	// Parent is mid-turn so a notification would stay queued (visible).
	f.parentMgr.SetAgentWorking(true)

	// Turn B is now running on the subagent.
	if err := f.subagentMgr.ensureLoop(f.llmSvc, "predictable"); err != nil {
		t.Fatalf("ensureLoop subagent: %v", err)
	}
	f.subagentMgr.SetAgentWorking(true)
	defer f.subagentMgr.SetAgentWorking(false)

	// The delayed notifier for turn A finally runs. It must skip.
	f.notifySubagentDone(f.subagentID)
	if n := countPendingSubagentDone(f.parentMgr, f.subagentID); n != 0 {
		t.Fatalf("expected delayed notifier to skip while a newer turn is running, have %d queued", n)
	}
}

// testSubagentDone_StaleNotificationCoalesced reproduces the "stray duplicate
// subagent notifications" bug: a subagent that finishes MORE THAN ONCE while
// the parent is busy (e.g. the parent hit its wait=true timeout, re-prompted
// the subagent, and the subagent finished each turn) must not leave multiple
// subagent-done batches queued for the parent. Only ONE notification per
// subagent conversation should remain pending — the newest — so that when the
// parent's turn ends it drains a single "subagent finished" pair instead of a
// pile of stale echoes of already-superseded turns.
func testSubagentDone_StaleNotificationCoalesced(t *testing.T) {
	f := newSubagentDoneFixture(t, "first-turn response")

	// Parent is mid-turn: its loop is busy, so enqueued subagent-done batches
	// wait in pendingBatches rather than draining immediately.
	f.parentMgr.SetAgentWorking(true)

	// Subagent finishes its first turn while the parent is busy -> one queued
	// notification.
	f.notifySubagentDone(f.subagentID)

	// The parent re-prompted the subagent (not modeled here) and it finishes a
	// SECOND turn, still while the parent is busy -> a second notification for
	// the SAME subagent conversation. This is the stale echo we must coalesce.
	f.notifySubagentDone(f.subagentID)

	// Give the async enqueue goroutines time to land both batches.
	waitFor(t, 5*time.Second, func() bool {
		return countPendingSubagentDone(f.parentMgr, f.subagentID) >= 1
	})
	time.Sleep(100 * time.Millisecond)

	// Exactly one subagent-done batch for this subagent should be queued: the
	// second (newest) notification supersedes the first.
	if n := countPendingSubagentDone(f.parentMgr, f.subagentID); n != 1 {
		t.Fatalf("expected exactly 1 queued subagent-done batch for the subagent, got %d", n)
	}

	// Drain: parent finishes its turn. Exactly one synthetic pair should land.
	f.parentMgr.SetAgentWorking(false)
	go f.parentMgr.drainPendingMessages(f.server)

	waitFor(t, 5*time.Second, func() bool {
		return countSyntheticDonePairs(t, f.parentMessages()) >= 1
	})
	// Let any erroneous second pair land.
	time.Sleep(150 * time.Millisecond)
	if n := countSyntheticDonePairs(t, f.parentMessages()); n != 1 {
		t.Fatalf("expected exactly one synthetic done pair after draining coalesced notifications, got %d", n)
	}
}

// countPendingSubagentDone counts queued subagent-done batches in the parent's
// pending queue that target the given subagent conversation.
func countPendingSubagentDone(cm *ConversationManager, subagentID string) int {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	n := 0
	for _, b := range cm.pendingBatches {
		if b.Kind == pendingBatchSubagentDone && b.SubagentConversationID == subagentID {
			n++
		}
	}
	return n
}

func truncForLog(s string) string {
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}

func dumpMessages(t *testing.T, msgs []generated.Message) string {
	t.Helper()
	var sb strings.Builder
	for i, m := range msgs {
		data := "<nil>"
		if m.LlmData != nil {
			data = *m.LlmData
		}
		sb.WriteString("[")
		b, _ := json.Marshal(i)
		sb.Write(b)
		sb.WriteString("]")
		sb.WriteString(" type=")
		sb.WriteString(m.Type)
		sb.WriteString(" data=")
		sb.WriteString(truncForLog(data))
		sb.WriteString("\n")
	}
	return sb.String()
}

// testSubagentDone_ConcurrentFinishes verifies that when two subagents under
// the same parent finish concurrently, both synthetic tool_use/tool_result
// pairs land in the parent's history and each pair is atomic: a tool_use is
// immediately followed by its matching tool_result with no interleaving.
// LLM APIs require tool_use blocks be paired with their tool_result before
// any other content; if the messages were globally racey we could end up
// with [useA, useB, resultA, resultB] which would be rejected.
func testSubagentDone_ConcurrentFinishes(t *testing.T) {
	f := newSubagentDoneFixture(t, "alpha done")

	ctx := context.Background()
	subConv2, err := f.database.CreateSubagentConversation(ctx, "sub-test-2", f.parentID, nil)
	if err != nil {
		t.Fatalf("create subagent 2: %v", err)
	}
	sub2Mgr, err := f.server.getOrCreateSubagentConversationManager(ctx, subConv2.ConversationID)
	if err != nil {
		t.Fatalf("get subagent 2 manager: %v", err)
	}
	if err := f.server.recordMessage(ctx, subConv2.ConversationID, llm.Message{
		Role:      llm.MessageRoleAssistant,
		Content:   []llm.Content{{Type: llm.ContentTypeText, Text: "beta done"}},
		EndOfTurn: true,
	}, llm.Usage{}, nil); err != nil {
		t.Fatalf("record subagent 2 assistant: %v", err)
	}

	// Prime working=true so the false transition fires onDone.
	f.subagentMgr.SetAgentWorking(true)
	sub2Mgr.SetAgentWorking(true)

	go f.subagentMgr.SetAgentWorking(false)
	go sub2Mgr.SetAgentWorking(false)

	// Either both pairs land (each = 2 msgs), or the parent loop has woken
	// and started churning. We just want >=4 messages in parent.
	// Wait until we see two distinct synthetic tool_use blocks each followed
	// by their matching tool_result. Just waiting on a message count can
	// catch an intermediate state where the parent's loop has interleaved
	// its own LLM turn between the two pairs.
	waitFor(t, 5*time.Second, func() bool {
		msgs := f.parentMessages()
		found := 0
		for i, m := range msgs {
			if m.LlmData == nil {
				continue
			}
			var lm llm.Message
			if err := json.Unmarshal([]byte(*m.LlmData), &lm); err != nil {
				continue
			}
			var useID string
			for _, c := range lm.Content {
				if c.Type == llm.ContentTypeToolUse && c.ToolName == "subagent" && strings.HasPrefix(c.ID, "sa_done_") {
					useID = c.ID
				}
			}
			if useID == "" {
				continue
			}
			if i+1 >= len(msgs) || msgs[i+1].LlmData == nil {
				continue
			}
			var next llm.Message
			if err := json.Unmarshal([]byte(*msgs[i+1].LlmData), &next); err != nil {
				continue
			}
			for _, c := range next.Content {
				if c.Type == llm.ContentTypeToolResult && c.ToolUseID == useID {
					found++
				}
			}
		}
		return found >= 2
	})

	msgs := f.parentMessages()

	type found struct {
		useIdx int
		useID  string
		text   string
	}
	var pairs []found
	for i, m := range msgs {
		if m.LlmData == nil {
			continue
		}
		var lm llm.Message
		if err := json.Unmarshal([]byte(*m.LlmData), &lm); err != nil {
			continue
		}
		var useID string
		for _, c := range lm.Content {
			if c.Type == llm.ContentTypeToolUse && c.ToolName == "subagent" && strings.HasPrefix(c.ID, "sa_done_") {
				useID = c.ID
			}
		}
		if useID == "" {
			continue
		}
		if i+1 >= len(msgs) || msgs[i+1].LlmData == nil {
			t.Fatalf("tool_use at idx %d (id=%s) has no following message; messages:\n%s", i, useID, dumpMessages(t, msgs))
		}
		var next llm.Message
		if err := json.Unmarshal([]byte(*msgs[i+1].LlmData), &next); err != nil {
			t.Fatalf("unmarshal next: %v", err)
		}
		var matched bool
		var text string
		for _, c := range next.Content {
			if c.Type == llm.ContentTypeToolResult && c.ToolUseID == useID {
				matched = true
				text = toolResultText(c)
			}
		}
		if !matched {
			t.Fatalf("tool_use at idx %d (id=%s) NOT followed by matching tool_result; messages:\n%s", i, useID, dumpMessages(t, msgs))
		}
		pairs = append(pairs, found{useIdx: i, useID: useID, text: text})
	}

	// We expect at least 2 distinct subagent pairs (the parent's loop may
	// also produce additional turns, but those aren't sa_done_ pairs).
	if len(pairs) < 2 {
		t.Fatalf("expected >=2 synthetic subagent pairs, got %d. messages:\n%s", len(pairs), dumpMessages(t, msgs))
	}

	seenAlpha, seenBeta := false, false
	seenIDs := map[string]bool{}
	for _, p := range pairs {
		if seenIDs[p.useID] {
			t.Errorf("duplicate tool_use ID across pairs: %q", p.useID)
		}
		seenIDs[p.useID] = true
		if strings.Contains(p.text, "alpha done") {
			seenAlpha = true
		}
		if strings.Contains(p.text, "beta done") {
			seenBeta = true
		}
	}
	if !seenAlpha || !seenBeta {
		t.Errorf("expected both 'alpha done' and 'beta done' across synthetic pairs (alpha=%v, beta=%v); pairs=%+v", seenAlpha, seenBeta, pairs)
	}
}

// testSubagentDone_LastMessageIsToolUse exercises the case where the
// subagent's most recent persisted message has no text content (it's a
// tool_use only). lastAssistantText returns "" and notifyParentSubagentDone
// must fall back to "(no textual response)" without panicking, while still
// emitting a coherent synthetic pair.
func testSubagentDone_LastMessageIsToolUse(t *testing.T) {
	f := newSubagentDoneFixture(t, "earlier text that should be IGNORED")

	toolUseOnly := llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{{
			Type:      llm.ContentTypeToolUse,
			ID:        "toolu_subagent_did_a_thing",
			ToolName:  "bash",
			ToolInput: []byte(`{"command":"echo hi"}`),
		}},
	}
	if err := f.server.recordMessage(context.Background(), f.subagentID, toolUseOnly, llm.Usage{}, nil); err != nil {
		t.Fatalf("record tool_use-only message: %v", err)
	}

	f.fireOnDone()

	waitFor(t, 5*time.Second, func() bool {
		_, _, ok := f.findSyntheticPair()
		return ok
	})

	_, result, ok := f.findSyntheticPair()
	if !ok {
		t.Fatalf("expected synthetic pair even when last subagent msg has no text")
	}
	var gotText string
	for _, c := range result.Content {
		if c.Type == llm.ContentTypeToolResult {
			gotText = toolResultText(c)
		}
	}
	if !strings.Contains(gotText, "(no textual response)") {
		t.Errorf("expected fallback text '(no textual response)' in tool_result, got %q", truncForLog(gotText))
	}
	if strings.Contains(gotText, "earlier text that should be IGNORED") {
		t.Errorf("tool_result leaked text from an older assistant message: %q", truncForLog(gotText))
	}
}

// testSubagentDone_GitInfoIgnored verifies that a gitinfo message recorded
// after the subagent's last real LLM response does not get picked up as
// "the subagent's response" and forwarded to the parent. Gitinfo messages
// have role=Assistant in their llm_data (they describe a worktree state
// change) but Type=gitinfo and are user-visible only — never sent to the
// LLM. Before the lastAgentText fix, notifyParentSubagentDone would
// happily splice the gitinfo text (e.g. '… now at abc1234 "some commit"')
// into the parent's tool_result.
func testSubagentDone_GitInfoIgnored(t *testing.T) {
	f := newSubagentDoneFixture(t, "The subagent's actual final answer.")

	gitMsg := llm.Message{
		Role:    llm.MessageRoleAssistant,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: `~/exe-subagent-done (subagent-done-notify) now at 7b2a11b65 "shelley: notify parent agent when subagent finishes"`}},
	}
	if _, err := f.database.CreateMessage(context.Background(), db.CreateMessageParams{
		ConversationID: f.subagentID,
		Type:           db.MessageTypeGitInfo,
		LLMData:        gitMsg,
		UsageData:      llm.Usage{},
	}); err != nil {
		t.Fatalf("create gitinfo msg: %v", err)
	}

	f.fireOnDone()

	waitFor(t, 5*time.Second, func() bool {
		_, _, ok := f.findSyntheticPair()
		return ok
	})

	_, result, ok := f.findSyntheticPair()
	if !ok {
		t.Fatalf("expected synthetic pair")
	}
	var text string
	for _, c := range result.Content {
		if c.Type == llm.ContentTypeToolResult {
			text = toolResultText(c)
		}
	}
	if !strings.Contains(text, f.subResponse) {
		t.Errorf("expected subagent's real response %q in tool_result, got %q", f.subResponse, truncForLog(text))
	}
	if strings.Contains(text, "now at 7b2a11b65") {
		t.Errorf("gitinfo message text leaked into tool_result: %q", truncForLog(text))
	}
}

// testSubagentDone_CitationMarkersStripped verifies that a ChatGPT-backed
// subagent's inline citation markup does not reach the parent conversation.
// The notification carries the text in a tool_result, which llmDataForAPI's
// agent-text strip never sees, so it must already be clean.
func testSubagentDone_CitationMarkersStripped(t *testing.T) {
	f := newSubagentDoneFixture(t, "Found it\ue200cite\ue202turn1search0\ue201 in the docs.")

	f.fireOnDone()

	waitFor(t, 5*time.Second, func() bool {
		_, _, ok := f.findSyntheticPair()
		return ok
	})

	_, result, ok := f.findSyntheticPair()
	if !ok {
		t.Fatalf("expected synthetic pair")
	}
	var text string
	for _, c := range result.Content {
		if c.Type == llm.ContentTypeToolResult {
			text = toolResultText(c)
		}
	}
	if !strings.Contains(text, "Found it in the docs.") {
		t.Errorf("expected stripped subagent response in tool_result, got %q", truncForLog(text))
	}
	if strings.ContainsAny(text, "\ue200\ue201\ue202\ue203") {
		t.Errorf("citation markers leaked into tool_result: %q", truncForLog(text))
	}
}
