package server

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
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

// A subagent that ends its turn with a plain reply instead of
// message_parent(end_turn=true) has the reply sent as that call once nothing
// it started can wake it again. A turn that ends while a background job
// runs keeps its reply: the job's notice wakes the subagent again.
func TestSubagentUnreportedReplyReachesParent(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	ctx := t.Context()
	child, err := database.CreateSubagentConversation(ctx, "quiet-child", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	job := testBackgroundJob(t, child.ConversationID)
	if err := server.recordBackgroundJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSubagentRunner(server).RunSubagent(ctx, child.ConversationID, "echo: waiting for the tests", "predictable", ""); err != nil {
		t.Fatal(err)
	}
	releaseAndWaitIdle(t, server, child.ConversationID, held.waitCall(t, "echo: waiting for the tests"))

	// The job's notice wakes the child, which forgets message_parent.
	if err := server.reportBackgroundJobExit(ctx, job); err != nil {
		t.Fatal(err)
	}
	releaseAndWaitIdle(t, server, child.ConversationID, held.waitCall(t, backgroundJobNotice(job)))

	const reply = "edit predictable.go to add a response for that one..."
	releaseAndWaitIdle(t, server, parent.ConversationID, held.waitCall(t, subagentMessage(child, reply)))
	if got := subagentTexts(t, database, parent.ConversationID); !slices.Equal(got, []string{reply}) {
		t.Fatalf("parent heard %q, want only the final reply", got)
	}
	var texts, warnings []string
	reports := 0
	for _, m := range listMessages(t, database, child.ConversationID) {
		switch m.Type {
		case string(db.MessageTypeWarning):
			warnings = append(warnings, derefString(m.UserData))
		case string(db.MessageTypeAgent):
			msg, err := convertToLLMMessage(m)
			if err != nil {
				t.Fatal(err)
			}
			if text := messageText(msg); text != "" {
				texts = append(texts, text)
			}
			for _, c := range msg.Content {
				if c.ToolName == "message_parent" && strings.Contains(string(c.ToolInput), reply) {
					reports++
				}
			}
		}
	}
	if !slices.Equal(texts, []string{"waiting for the tests"}) || reports != 1 {
		t.Fatalf("child replies %q with %d reports, want the final reply rewritten into one report", texts, reports)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "message_parent(end_turn=true)") {
		t.Fatalf("child warnings = %q, want one noting the report", warnings)
	}
}

func TestOwesReport(t *testing.T) {
	row := func(typ db.MessageType, m llm.Message, userData string) generated.Message {
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		row := generated.Message{Type: string(typ), LlmData: strPtr(string(data)), ExcludedFromContext: m.ExcludedFromContext}
		if userData != "" {
			row.UserData = &userData
		}
		return row
	}
	prompt := row(db.MessageTypeUser, llm.UserStringMessage("go"), "")
	notice := row(db.MessageTypeUser, llm.UserStringMessage("job done"), `{"background_job_id":"j"}`)
	parentMessage := row(db.MessageTypeUser, llm.UserStringMessage("also this"), `{"sender_conversation_id":"p","sender_relationship":"parent"}`)
	reply := row(db.MessageTypeAgent, llm.Message{Role: llm.MessageRoleAssistant, Content: []llm.Content{llm.StringContent("ok")}, EndOfTurn: true}, "")
	report := func(id string, endTurn bool) generated.Message {
		input, _ := json.Marshal(map[string]any{"text": "r", "end_turn": endTurn})
		return row(db.MessageTypeAgent, llm.Message{Role: llm.MessageRoleAssistant, Content: []llm.Content{{ID: id, Type: llm.ContentTypeToolUse, ToolName: "message_parent", ToolInput: input}}}, "")
	}
	result := func(id string, failed bool) generated.Message {
		return row(db.MessageTypeUser, llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeToolResult, ToolUseID: id, ToolError: failed}}}, "")
	}
	marker := row(db.MessageTypeAgent, llm.Message{Role: llm.MessageRoleAssistant, EndOfTurn: true, ExcludedFromContext: true}, "")
	for _, tc := range []struct {
		name string
		msgs []generated.Message
		want bool
	}{
		{"prompt", []generated.Message{prompt}, true},
		{"final report", []generated.Message{prompt, report("a", true), result("a", false), marker, notice}, false},
		{"progress report", []generated.Message{prompt, report("a", false), result("a", false)}, true},
		{"failed final report", []generated.Message{prompt, report("a", true), result("a", true)}, true},
		{"report before this prompt", []generated.Message{prompt, report("a", true), result("a", false), marker, prompt}, true},
		{"job notice after a reply", []generated.Message{prompt, reply, notice}, true},
		{"parent message after a report", []generated.Message{prompt, report("a", true), result("a", false), marker, parentMessage}, true},
	} {
		if got := owesReport(tc.msgs); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// After a final report, a turn the parent didn't ask for (here a background
// job's notice) ends without disturbing the parent.
func TestSubagentReplyAfterFinalReportStaysPut(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	ctx := t.Context()
	child, err := database.CreateSubagentConversation(ctx, "quiet-child", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner := NewSubagentRunner(server)
	report := func(text string) {
		t.Helper()
		prompt := "message_parent_final: " + text
		if _, err := runner.RunSubagent(ctx, child.ConversationID, prompt, "predictable", ""); err != nil {
			t.Fatal(err)
		}
		releaseAndWaitIdle(t, server, child.ConversationID, held.waitCall(t, prompt))
		releaseAndWaitIdle(t, server, parent.ConversationID, held.waitCall(t, subagentMessage(child, text)))
	}
	report("all done")

	job := testBackgroundJob(t, child.ConversationID)
	if err := server.recordBackgroundJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := server.reportBackgroundJobExit(ctx, job); err != nil {
		t.Fatal(err)
	}
	releaseAndWaitIdle(t, server, child.ConversationID, held.waitCall(t, backgroundJobNotice(job)))

	report("second")
	if got := subagentTexts(t, database, parent.ConversationID); !slices.Equal(got, []string{"all done", "second"}) {
		t.Fatalf("parent heard %q, want only the two reports", got)
	}
}

// A delegated turn that ends with an error — a failed request, a truncated
// response, a refusal — reports the error to the parent as a reply would be,
// and keeps its error row.
func TestSubagentErrorReachesParent(t *testing.T) {
	for _, tc := range []struct {
		prompt    string
		errorType llm.ErrorType
		want      string // what the parent must be told
	}{
		{"error: boom", llm.ErrorTypeLLMRequest, "boom"},
		{"maxTokens", llm.ErrorTypeTruncation, "truncated"},
		{"refusal", llm.ErrorTypeRefusal, "declined"},
	} {
		t.Run(tc.prompt, func(t *testing.T) {
			server, database, held, parent := newBtwTest(t)
			ctx := t.Context()
			child, err := database.CreateSubagentConversation(ctx, "quiet-child", parent.ConversationID, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := NewSubagentRunner(server).RunSubagent(ctx, child.ConversationID, tc.prompt, "predictable", ""); err != nil {
				t.Fatal(err)
			}
			held.waitCall(t, tc.prompt).Release()
			errorRow := waitForErrorRow(t, database, child.ConversationID)
			if errorRow.ErrorType != tc.errorType {
				t.Fatalf("child error row = %+v, want %s", errorRow, tc.errorType)
			}
			report := "Ended with an error: " + strings.TrimSpace(messageText(errorRow))
			if !strings.Contains(report, tc.want) {
				t.Fatalf("report %q does not say %q", report, tc.want)
			}
			releaseAndWaitIdle(t, server, parent.ConversationID, held.waitCall(t, subagentMessage(child, report)))
			if got := subagentTexts(t, database, parent.ConversationID); !slices.Equal(got, []string{report}) {
				t.Fatalf("parent heard %q, want only the error", got)
			}
			waitForReportEnd(t, database, child.ConversationID)
			if server.IsAgentWorking(child.ConversationID) {
				t.Fatal("child is still working")
			}
			reports, warnings := childReports(t, database, child.ConversationID)
			if reports != 1 || len(warnings) != 1 || !strings.Contains(warnings[0], "message_parent(end_turn=true)") {
				t.Fatalf("child made %d reports with warnings %q, want one of each", reports, warnings)
			}
		})
	}
}

// After a final report, a turn the parent didn't ask for (here a background
// job's notice) that ends with an error stays put, like one that ends with
// a reply.
func TestSubagentErrorAfterFinalReportStaysPut(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	ctx := t.Context()
	child, err := database.CreateSubagentConversation(ctx, "quiet-child", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner := NewSubagentRunner(server)
	report := func(text string) {
		t.Helper()
		prompt := "message_parent_final: " + text
		if _, err := runner.RunSubagent(ctx, child.ConversationID, prompt, "predictable", ""); err != nil {
			t.Fatal(err)
		}
		releaseAndWaitIdle(t, server, child.ConversationID, held.waitCall(t, prompt))
		releaseAndWaitIdle(t, server, parent.ConversationID, held.waitCall(t, subagentMessage(child, text)))
	}
	report("all done")

	job := testBackgroundJob(t, child.ConversationID)
	if err := server.recordBackgroundJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	held.Service = failingLLMService{held.Service, backgroundJobNotice(job)}
	if err := server.reportBackgroundJobExit(ctx, job); err != nil {
		t.Fatal(err)
	}
	held.waitCall(t, backgroundJobNotice(job)).Release()
	waitForErrorRow(t, database, child.ConversationID)

	// The next report is the parent's next wake.
	report("second")
	if got := subagentTexts(t, database, parent.ConversationID); !slices.Equal(got, []string{"all done", "second"}) {
		t.Fatalf("parent heard %q, want only the two reports", got)
	}
	if reports, warnings := childReports(t, database, child.ConversationID); reports != 2 || len(warnings) != 0 {
		t.Fatalf("child made %d reports with warnings %q, want just the two real ones", reports, warnings)
	}
}

// A child whose parent was stopped keeps its error to itself and ends its
// turn cleanly.
func TestSubagentErrorWithStoppedParentStaysPut(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	ctx := t.Context()
	child, err := database.CreateSubagentConversation(ctx, "quiet-child", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner := NewSubagentRunner(server)
	if _, err := runner.RunSubagent(ctx, child.ConversationID, "error: boom", "predictable", ""); err != nil {
		t.Fatal(err)
	}
	call := held.waitCall(t, "error: boom")
	server.mu.Lock()
	server.stoppedParents[parent.ConversationID] = true
	server.mu.Unlock()
	call.Release()
	waitForErrorRow(t, database, child.ConversationID)
	conv, err := database.GetConversationByID(ctx, parent.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if conv.AgentWorking || conv.QueuedMessages != "[]" {
		t.Fatalf("stopped parent restarted: working=%v queued=%s", conv.AgentWorking, conv.QueuedMessages)
	}

	// New delegated work reopens the parent; its report is the parent's first wake.
	if _, err := runner.RunSubagent(ctx, child.ConversationID, "message_parent_final: later", "predictable", ""); err != nil {
		t.Fatal(err)
	}
	releaseAndWaitIdle(t, server, child.ConversationID, held.waitCall(t, "message_parent_final: later"))
	releaseAndWaitIdle(t, server, parent.ConversationID, held.waitCall(t, subagentMessage(child, "later")))
	if got := subagentTexts(t, database, parent.ConversationID); !slices.Equal(got, []string{"later"}) {
		t.Fatalf("parent heard %q, want only the later report", got)
	}
	if reports, warnings := childReports(t, database, child.ConversationID); reports != 1 || len(warnings) != 0 {
		t.Fatalf("child made %d reports with warnings %q, want just the real one", reports, warnings)
	}
}

// failingLLMService fails the request whose last user text is prompt.
type failingLLMService struct {
	llm.Service
	prompt string
}

func (s failingLLMService) Do(ctx context.Context, request *llm.Request) (*llm.Response, error) {
	if lastRequestUserText(request) == s.prompt {
		return nil, errors.New("boom")
	}
	return s.Service.Do(ctx, request)
}

// waitForErrorRow waits for the conversation's one error row and returns it.
func waitForErrorRow(t *testing.T, database *db.DB, conversationID string) llm.Message {
	t.Helper()
	var rows []llm.Message
	waitFor(t, 5*time.Second, func() bool {
		rows = nil
		for _, m := range listMessages(t, database, conversationID) {
			if m.Type != string(db.MessageTypeError) {
				continue
			}
			msg, err := convertToLLMMessage(m)
			if err != nil {
				t.Fatal(err)
			}
			rows = append(rows, msg)
		}
		return len(rows) > 0
	})
	if len(rows) != 1 {
		t.Fatalf("child has %d error rows, want 1", len(rows))
	}
	return rows[0]
}

// waitForReportEnd waits for the end-of-turn marker that follows a report's
// tool result. An error row ends the turn before its report runs.
func waitForReportEnd(t *testing.T, database *db.DB, conversationID string) {
	t.Helper()
	waitFor(t, 5*time.Second, func() bool {
		msgs := listMessages(t, database, conversationID)
		last := msgs[len(msgs)-1]
		msg, err := convertToLLMMessage(last)
		return err == nil && last.Type == string(db.MessageTypeAgent) && msg.EndOfTurn && msg.ExcludedFromContext
	})
}

// childReports counts the conversation's message_parent calls and returns
// its warnings.
func childReports(t *testing.T, database *db.DB, conversationID string) (reports int, warnings []string) {
	t.Helper()
	for _, m := range listMessages(t, database, conversationID) {
		switch m.Type {
		case string(db.MessageTypeWarning):
			warnings = append(warnings, derefString(m.UserData))
		case string(db.MessageTypeAgent):
			msg, err := convertToLLMMessage(m)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range msg.Content {
				if c.Type == llm.ContentTypeToolUse && c.ToolName == "message_parent" {
					reports++
				}
			}
		}
	}
	return reports, warnings
}

func testBackgroundJob(t *testing.T, conversationID string) claudetool.BackgroundJob {
	dir := t.TempDir()
	return claudetool.BackgroundJob{
		ID:             "job1",
		ConversationID: conversationID,
		Command:        "make test",
		LogPath:        filepath.Join(dir, "job1.log"),
		ExitPath:       filepath.Join(dir, "job1.exit"),
		StartedAt:      time.Now(),
	}
}

func backgroundJobNotice(job claudetool.BackgroundJob) string {
	return `<background_job id="` + job.ID + `">` + "\n" + xmlProvenanceText(job.Outcome().Notice()) + "\n</background_job>"
}

func subagentMessage(child *generated.Conversation, text string) string {
	return `<subagent_message conversation_id="` + child.ConversationID + `" slug="` + derefString(child.Slug) + `">` +
		"\n" + text + "\n</subagent_message>"
}

// subagentTexts returns what the conversation's subagents told it, in order.
func subagentTexts(t *testing.T, database *db.DB, conversationID string) []string {
	t.Helper()
	var texts []string
	for _, m := range listMessages(t, database, conversationID) {
		if m.Type != string(db.MessageTypeUser) || m.UserData == nil {
			continue
		}
		if data, ok, err := parseSenderMessageUserData([]byte(*m.UserData)); err == nil && ok {
			texts = append(texts, data.Text)
		}
	}
	return texts
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
		return countByType(listMessages(t, database, sub.ConversationID), db.MessageTypeAgent) > 0 && !server.IsAgentWorking(sub.ConversationID)
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
			return countByType(listMessages(t, database, tc.id), db.MessageTypeAgent) > 0 && !server.IsAgentWorking(tc.id)
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
