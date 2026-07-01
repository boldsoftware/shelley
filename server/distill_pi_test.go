package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

func textMsg(role llm.MessageRole, text string) llm.Message {
	return llm.Message{Role: role, Content: []llm.Content{{Type: llm.ContentTypeText, Text: text}}}
}

func toolResultMsg(text string) llm.Message {
	return llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{{
			Type:       llm.ContentTypeToolResult,
			ToolUseID:  "t1",
			ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: text}},
		}},
	}
}

func TestFindPiCutPointNeverLandsOnToolResult(t *testing.T) {
	// big strings so each message is ~ many tokens
	big := strings.Repeat("x", 4000) // ~1000 tokens
	msgs := []llm.Message{
		textMsg(llm.MessageRoleUser, big),      // 0
		textMsg(llm.MessageRoleAssistant, big), // 1 (tool_use elided; text only)
		toolResultMsg(big),                     // 2 - NOT a valid cut point
		textMsg(llm.MessageRoleAssistant, big), // 3
		textMsg(llm.MessageRoleUser, big),      // 4
	}

	// keepRecentTokens chosen so the walk-back crosses the tool result.
	cut := findPiCutPoint(msgs, 2500)
	if cut < 0 || cut >= len(msgs) {
		t.Fatalf("cut out of range: %d", cut)
	}
	if isToolResultMessage(msgs[cut]) {
		t.Fatalf("cut landed on a tool_result message at index %d", cut)
	}
}

func TestFindPiCutPointKeepsAllWhenSmall(t *testing.T) {
	msgs := []llm.Message{
		textMsg(llm.MessageRoleUser, "hi"),
		textMsg(llm.MessageRoleAssistant, "hello"),
	}
	// Plenty of budget -> nothing summarized, cut at start.
	if cut := findPiCutPoint(msgs, 20000); cut != 0 {
		t.Fatalf("expected cut=0 (keep all), got %d", cut)
	}
}

func TestSerializePiConversationRendersRolesAndTools(t *testing.T) {
	msgs := []llm.Message{
		textMsg(llm.MessageRoleUser, "fix the bug"),
		{
			Role: llm.MessageRoleAssistant,
			Content: []llm.Content{
				{Type: llm.ContentTypeText, Text: "on it"},
				{Type: llm.ContentTypeToolUse, ToolName: "bash", ToolInput: json.RawMessage(`{"command":"ls"}`)},
			},
		},
		toolResultMsg("file.go"),
	}
	out := serializePiConversation(msgs)
	for _, want := range []string{"[User]: fix the bug", "[Assistant]: on it", "[Assistant tool calls]: bash(", "[Tool result]: file.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("serialized conversation missing %q\n---\n%s", want, out)
		}
	}
}

func TestSteeringSection(t *testing.T) {
	got := steeringSection("keep the auth work, drop CSS")
	if !strings.Contains(got, "User Guidance") {
		t.Errorf("missing guidance header: %q", got)
	}
	if !strings.Contains(got, "keep the auth work, drop CSS") {
		t.Errorf("missing instructions body: %q", got)
	}
}

func TestExtractPiFileOps(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.MessageRoleAssistant, Content: []llm.Content{
			{Type: llm.ContentTypeToolUse, ToolName: "read_image", ToolInput: json.RawMessage(`{"path":"a.go"}`)},
			{Type: llm.ContentTypeToolUse, ToolName: "patch", ToolInput: json.RawMessage(`{"path":"b.go"}`)},
			{Type: llm.ContentTypeToolUse, ToolName: "read_context_file", ToolInput: json.RawMessage(`{"path":"b.go"}`)},
		}},
	}
	read, modified := extractPiFileOps(msgs)
	// b.go was both read and patched -> counts only as modified.
	if len(read) != 1 || read[0] != "a.go" {
		t.Errorf("read files = %v, want [a.go]", read)
	}
	if len(modified) != 1 || modified[0] != "b.go" {
		t.Errorf("modified files = %v, want [b.go]", modified)
	}
}

// TestPiDistillCopiesRecentMessagesIntoNewGeneration drives the handler with
// method="pi" and verifies the new generation contains the verbatim-copied
// recent messages (the whole short conversation, since nothing is summarized).
func TestPiDistillCopiesRecentMessagesIntoNewGeneration(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := NewTestHarness(t)
		defer stopActiveConversationLoops(h.server)

		h.NewConversation("echo: first thing", "")
		h.WaitResponse()
		synctest.Wait()
		h.Chat("echo: second thing")
		h.WaitResponse()
		synctest.Wait()
		convID := h.convID
		ctx := context.Background()

		beforeGen, err := h.db.GetConversationByID(ctx, convID)
		if err != nil {
			t.Fatalf("GetConversationByID: %v", err)
		}

		reqBody := DistillNewGenerationRequest{
			SourceConversationID: convID,
			Model:                "predictable",
			Method:               distillMethodCompact,
		}
		body, _ := json.Marshal(reqBody)
		req := httptest.NewRequest("POST", "/api/conversations/distill-new-generation", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.server.handleDistillNewGeneration(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
		}

		waitForConversationDistillingToClear(t, h.server, convID)
		synctest.Wait()

		after, err := h.db.GetConversationByID(ctx, convID)
		if err != nil {
			t.Fatalf("GetConversationByID: %v", err)
		}
		if after.CurrentGeneration != beforeGen.CurrentGeneration+1 {
			t.Fatalf("generation = %d, want %d", after.CurrentGeneration, beforeGen.CurrentGeneration+1)
		}

		ctxMsgs, err := h.db.ListMessagesForContext(ctx, convID)
		if err != nil {
			t.Fatalf("ListMessagesForContext: %v", err)
		}
		// New generation context should contain the system prompt plus the
		// verbatim-copied recent messages (the original user/agent turns).
		var sawUserEcho, sawCarriedFlag bool
		for _, m := range ctxMsgs {
			if m.Generation != after.CurrentGeneration {
				t.Fatalf("context message from stale generation %d", m.Generation)
			}
			if m.Type == string(db.MessageTypeUser) && m.LlmData != nil &&
				strings.Contains(*m.LlmData, "first thing") {
				sawUserEcho = true
			}
			// Copied messages are stamped compaction_carried=true so the UI can
			// collapse the replayed tail behind a single band.
			if m.Type != string(db.MessageTypeSystem) && m.UserData != nil {
				var ud map[string]string
				if json.Unmarshal([]byte(*m.UserData), &ud) == nil && ud["compaction_carried"] == "true" {
					sawCarriedFlag = true
				}
			}
		}
		if !sawUserEcho {
			t.Fatalf("expected verbatim recent user message copied into new generation; got %d context msgs", len(ctxMsgs))
		}
		if !sawCarriedFlag {
			t.Fatalf("expected copied messages stamped compaction_carried=true")
		}
	})
}

// TestPiDistillForcesSummaryWhenOverBudget lowers keepRecentTokens so the cut
// point leaves older messages to summarize, and verifies a distilled summary
// message (distilled=true, distill_method=pi) is inserted into the new
// generation alongside the verbatim recent tail.
func TestPiDistillForcesSummaryWhenOverBudget(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := NewTestHarness(t)
		defer stopActiveConversationLoops(h.server)
		// Force a tiny recent budget so something is always summarized.
		h.server.piDistillKeepRecentTokens = 1

		h.NewConversation("echo: alpha", "")
		h.WaitResponse()
		synctest.Wait()
		h.Chat("echo: beta")
		h.WaitResponse()
		synctest.Wait()
		convID := h.convID
		ctx := context.Background()

		reqBody := DistillNewGenerationRequest{
			SourceConversationID: convID,
			Model:                "predictable",
			Method:               distillMethodCompact,
		}
		body, _ := json.Marshal(reqBody)
		req := httptest.NewRequest("POST", "/api/conversations/distill-new-generation", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.server.handleDistillNewGeneration(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
		}

		waitForConversationDistillingToClear(t, h.server, convID)
		synctest.Wait()

		after, err := h.db.GetConversationByID(ctx, convID)
		if err != nil {
			t.Fatalf("GetConversationByID: %v", err)
		}

		msgs, err := h.db.ListMessages(ctx, convID)
		if err != nil {
			t.Fatalf("ListMessages: %v", err)
		}
		var sawSummary bool
		for _, m := range msgs {
			if m.Generation != after.CurrentGeneration || m.UserData == nil {
				continue
			}
			var ud map[string]string
			if json.Unmarshal([]byte(*m.UserData), &ud) != nil {
				continue
			}
			if ud["distilled"] == "true" && ud["distill_method"] == distillMethodCompact {
				sawSummary = true
				if !strings.Contains(ud["distillation_content"], "<summary>") {
					t.Errorf("pi summary message missing <summary> wrapper: %q", ud["distillation_content"])
				}
			}
		}
		if !sawSummary {
			t.Fatalf("expected a pi summary message in the new generation")
		}
	})
}

// TestPiReDistillPreservesPriorSummary verifies that distilling an
// already-distilled conversation does NOT lose the earlier summary content:
// a distilled message kept in the verbatim tail retains its distilled=true
// marker (and thus its real summary text), and a distilled message that falls
// into the summarized slice is fed to the summarizer as its real text rather
// than the "Distillation written to ..." placeholder.
func TestPiReDistillPreservesPriorSummary(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := NewTestHarness(t)
		defer stopActiveConversationLoops(h.server)

		h.NewConversation("echo: alpha", "")
		h.WaitResponse()
		synctest.Wait()
		convID := h.convID
		ctx := context.Background()

		distill := func() {
			reqBody := DistillNewGenerationRequest{
				SourceConversationID: convID,
				Model:                "predictable",
				Method:               distillMethodCompact,
			}
			body, _ := json.Marshal(reqBody)
			req := httptest.NewRequest("POST", "/api/conversations/distill-new-generation", strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.server.handleDistillNewGeneration(w, req)
			if w.Code != http.StatusCreated {
				t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
			}
			waitForConversationDistillingToClear(t, h.server, convID)
			synctest.Wait()
		}

		// First distill: short conversation fits in budget, so the whole turn is
		// copied verbatim and no summary is produced. To get a distilled message
		// into the history we force a tiny budget for the first pass.
		h.server.piDistillKeepRecentTokens = 1
		distill()

		// Confirm a distilled summary message now exists in generation 2.
		gen2, err := h.db.GetConversationByID(ctx, convID)
		if err != nil {
			t.Fatalf("GetConversationByID: %v", err)
		}
		var firstSummary string
		msgs, err := h.db.ListMessages(ctx, convID)
		if err != nil {
			t.Fatalf("ListMessages: %v", err)
		}
		for _, m := range msgs {
			if m.Generation != gen2.CurrentGeneration || m.UserData == nil {
				continue
			}
			var ud map[string]string
			if json.Unmarshal([]byte(*m.UserData), &ud) == nil && ud["distilled"] == "true" {
				firstSummary = ud["distillation_content"]
			}
		}
		if firstSummary == "" {
			t.Fatalf("expected a distilled message after first pi distill")
		}

		// Second distill with a generous budget: everything fits, so the prior
		// distilled message is copied verbatim into the kept tail. It MUST retain
		// its distilled=true marker and real summary content.
		h.server.piDistillKeepRecentTokens = 0 // use default (large) budget
		distill()

		gen3, err := h.db.GetConversationByID(ctx, convID)
		if err != nil {
			t.Fatalf("GetConversationByID: %v", err)
		}
		msgs, err = h.db.ListMessages(ctx, convID)
		if err != nil {
			t.Fatalf("ListMessages: %v", err)
		}
		var keptSummary string
		for _, m := range msgs {
			if m.Generation != gen3.CurrentGeneration || m.UserData == nil {
				continue
			}
			var ud map[string]string
			if json.Unmarshal([]byte(*m.UserData), &ud) == nil && ud["distilled"] == "true" {
				keptSummary = ud["distillation_content"]
			}
		}
		if keptSummary == "" {
			t.Fatalf("prior distilled message lost its distilled marker after re-distillation")
		}
		if !strings.Contains(keptSummary, "<summary>") {
			t.Errorf("kept distilled message lost its summary content: %q", keptSummary)
		}
	})
}

func TestResolvePiSummarizationTextSubstitutesPlaceholder(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ud, _ := json.Marshal(map[string]string{
		"distilled":            "true",
		"distillation_content": "REAL SUMMARY TEXT",
	})
	udStr := string(ud)
	entry := piContextMessage{
		llm:    textMsg(llm.MessageRoleUser, "Distillation written to /tmp/x.md"),
		source: generated.Message{MessageID: "m1", UserData: &udStr},
	}
	got := resolvePiSummarizationText(logger, entry)
	if len(got.Content) == 0 || got.Content[0].Text != "REAL SUMMARY TEXT" {
		t.Fatalf("expected placeholder replaced with real summary, got %+v", got.Content)
	}
	// The original entry must be untouched (no shared-slice mutation).
	if entry.llm.Content[0].Text != "Distillation written to /tmp/x.md" {
		t.Fatalf("resolvePiSummarizationText mutated the source message")
	}
}

// TestCompactBatchesMessageWrites guards against the per-message commit
// regression: compaction copies the recent tail forward, and previously did so
// one DB transaction per message. Each commit fires the conversation-list
// recompute hook (which reads + hashes the whole list), so the stream loaded
// visibly slowly on a large DB — the carried count ticked up one slow step at a
// time. recordMessages now batches the summary + tail into a single Tx, so the
// number of commit hooks fired during compaction must NOT grow with the tail.
func TestCompactBatchesMessageWrites(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := NewTestHarness(t)
		defer stopActiveConversationLoops(h.server)

		// Several turns so the carried tail has multiple messages.
		h.NewConversation("echo one", "")
		h.WaitResponse()
		synctest.Wait()
		h.Chat("echo two")
		h.WaitResponse()
		synctest.Wait()
		h.Chat("echo three")
		h.WaitResponse()
		synctest.Wait()
		convID := h.convID

		// Count commit-hook fires during the compaction only.
		var commits int64
		h.db.Pool().OnCommit(func() { atomic.AddInt64(&commits, 1) })

		reqBody := DistillNewGenerationRequest{
			SourceConversationID: convID,
			Model:                "predictable",
			Method:               distillMethodCompact,
		}
		body, _ := json.Marshal(reqBody)
		req := httptest.NewRequest("POST", "/api/conversations/distill-new-generation", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.server.handleDistillNewGeneration(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
		}
		waitForConversationDistillingToClear(t, h.server, convID)
		synctest.Wait()

		// Count how many context messages were carried forward.
		ctx := context.Background()
		msgs, err := h.db.ListMessages(ctx, convID)
		if err != nil {
			t.Fatalf("ListMessages: %v", err)
		}
		var carried int
		for _, m := range msgs {
			if m.UserData == nil {
				continue
			}
			var ud map[string]string
			if json.Unmarshal([]byte(*m.UserData), &ud) == nil && ud["compaction_carried"] == "true" {
				carried++
			}
		}
		if carried < 3 {
			t.Fatalf("expected at least 3 carried messages to exercise batching, got %d", carried)
		}

		// The status is split into two immutable messages: in_progress at start
		// and a terminal "complete" appended (folded into the batch Tx) at the end.
		statuses := distillStatusMessages(t, h, convID)
		if len(statuses) != 2 {
			t.Fatalf("expected 2 distill_status messages, got %d: %+v", len(statuses), statuses)
		}
		if statuses[0]["distill_status"] != "in_progress" || statuses[1]["distill_status"] != "complete" {
			t.Fatalf("expected [in_progress, complete], got [%q, %q]", statuses[0]["distill_status"], statuses[1]["distill_status"])
		}
		if statuses[1]["distill_method"] != distillMethodCompact {
			t.Errorf("terminal status distill_method = %q, want %q", statuses[1]["distill_method"], distillMethodCompact)
		}

		// The whole compaction's commit count must stay well under one-per-
		// carried-message and not scale with the tail length. With batching, the
		// summary + carried tail + the terminal "complete" status message are all a
		// SINGLE commit. The remaining commits are fixed setup overhead (generation
		// bump, status-spinner insert, new-generation hydrate). We cap at a small
		// fixed constant so a regression that splits the batch — or appends the
		// terminal status in its own Tx — trips the test even on a short tail.
		got := atomic.LoadInt64(&commits)
		if got > int64(carried) {
			t.Fatalf("compaction fired %d commit hooks for %d carried messages; expected the tail to be batched into one Tx (no per-message recompute)", got, carried)
		}
		const maxCompactionCommits = 5
		if got > maxCompactionCommits {
			t.Fatalf("compaction fired %d commit hooks; expected ≤ %d fixed setup commits with the summary, tail, and status flip folded into one Tx", got, maxCompactionCommits)
		}
	})
}

// TestDistillMethodCoercesToCompact verifies that we have consolidated on
// compaction: legacy method values (empty and "default") are accepted for
// compatibility but always run the compaction strategy, tagging the terminal
// status message with distill_method=compact. A clearly bogus method is
// rejected.
func TestDistillMethodCoercesToCompact(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"", "default", "compact"} {
		method := method
		t.Run("method="+method, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				h := NewTestHarness(t)
				defer stopActiveConversationLoops(h.server)

				h.NewConversation("echo hello", "")
				h.WaitResponse()
				synctest.Wait()
				convID := h.convID

				reqBody := DistillNewGenerationRequest{
					SourceConversationID: convID,
					Model:                "predictable",
					Method:               method,
				}
				body, _ := json.Marshal(reqBody)
				req := httptest.NewRequest("POST", "/api/conversations/distill-new-generation", strings.NewReader(string(body)))
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				h.server.handleDistillNewGeneration(w, req)
				if w.Code != http.StatusCreated {
					t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
				}
				waitForConversationDistillingToClear(t, h.server, convID)
				synctest.Wait()

				statuses := distillStatusMessages(t, h, convID)
				if len(statuses) != 2 {
					t.Fatalf("expected 2 distill_status messages, got %d: %+v", len(statuses), statuses)
				}
				if statuses[1]["distill_method"] != distillMethodCompact {
					t.Errorf("terminal status distill_method = %q, want %q (method %q should compact)", statuses[1]["distill_method"], distillMethodCompact, method)
				}
			})
		})
	}
}

// TestDistillRejectsUnknownMethod verifies a bogus method is still rejected.
func TestDistillRejectsUnknownMethod(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := NewTestHarness(t)
		defer stopActiveConversationLoops(h.server)

		h.NewConversation("echo hello", "")
		h.WaitResponse()
		synctest.Wait()

		reqBody := DistillNewGenerationRequest{
			SourceConversationID: h.convID,
			Model:                "predictable",
			Method:               "bogus",
		}
		body, _ := json.Marshal(reqBody)
		req := httptest.NewRequest("POST", "/api/conversations/distill-new-generation", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.server.handleDistillNewGeneration(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for unknown method, got %d: %s", w.Code, w.Body.String())
		}
	})
}
