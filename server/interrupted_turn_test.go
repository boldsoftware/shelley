package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

func seedManualInterruption(t *testing.T, database *db.DB, trailingError bool) string {
	t.Helper()
	ctx := context.Background()
	model := "predictable"
	conv, err := database.CreateConversation(ctx, nil, true, nil, &model, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if _, err := database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           db.MessageTypeSystem,
		LLMData:        llm.UserStringMessage("system"),
	}); err != nil {
		t.Fatalf("Create system message: %v", err)
	}
	if _, err := database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           db.MessageTypeUser,
		LLMData:        llm.UserStringMessage("echo: resumed"),
	}); err != nil {
		t.Fatalf("Create user message: %v", err)
	}
	if trailingError {
		if _, err := database.CreateMessage(ctx, db.CreateMessageParams{
			ConversationID: conv.ConversationID,
			Type:           db.MessageTypeError,
			LLMData: llm.Message{
				Role:      llm.MessageRoleAssistant,
				Content:   []llm.Content{{Type: llm.ContentTypeText, Text: "temporary failure"}},
				EndOfTurn: true,
			},
			UserData: map[string]any{"retryable": true},
		}); err != nil {
			t.Fatalf("Create error message: %v", err)
		}
	}
	if err := database.SetConversationAgentWorking(ctx, conv.ConversationID, true); err != nil {
		t.Fatalf("SetConversationAgentWorking: %v", err)
	}
	if ids, err := database.ConsumeResumeAfterUpgrade(ctx); err != nil || len(ids) != 0 {
		t.Fatalf("mark ordinary restart interruption: ids=%v err=%v", ids, err)
	}
	got, err := database.GetConversationByID(ctx, conv.ConversationID)
	if err != nil {
		t.Fatalf("GetConversationByID: %v", err)
	}
	if !got.TurnInterrupted || got.AgentWorking {
		t.Fatalf("restart state interrupted=%v working=%v, want true/false", got.TurnInterrupted, got.AgentWorking)
	}
	return conv.ConversationID
}

func TestResumeInterruptedConversation(t *testing.T) {
	t.Parallel()
	srv, database, predictableService := newTestServer(t)
	predictableService.SetResponseDelay(300 * time.Millisecond)
	ctx := context.Background()
	conversationID := seedManualInterruption(t, database, false)

	req := httptest.NewRequest(http.MethodPost, "/api/conversation/"+conversationID+"/resume", nil)
	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("resume status = %d, want 202: %s", w.Code, w.Body.String())
	}
	claimed, err := database.GetConversationByID(ctx, conversationID)
	if err != nil {
		t.Fatalf("GetConversationByID after resume: %v", err)
	}
	if claimed.TurnInterrupted || !claimed.AgentWorking {
		t.Fatalf("claimed state interrupted=%v working=%v, want false/true", claimed.TurnInterrupted, claimed.AgentWorking)
	}

	// A second click while the retry is live must not enqueue another LLM call.
	w = httptest.NewRecorder()
	srv.handleResumeConversation(w, httptest.NewRequest(http.MethodPost, "/", nil), conversationID)
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), "not_applicable") {
		t.Fatalf("duplicate resume = %d %q, want accepted not_applicable", w.Code, w.Body.String())
	}

	waitFor(t, 10*time.Second, func() bool {
		messages, err := database.ListMessages(ctx, conversationID)
		if err != nil {
			return false
		}
		for _, message := range messages {
			if message.Type == string(db.MessageTypeAgent) && isAgentEndOfTurn(&message) {
				return true
			}
		}
		return false
	})
	waitFor(t, 5*time.Second, func() bool { return !srv.IsAgentWorking(conversationID) })

	messages, err := database.ListMessages(ctx, conversationID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	var users, agents int
	for _, message := range messages {
		switch message.Type {
		case string(db.MessageTypeUser):
			users++
		case string(db.MessageTypeAgent):
			agents++
		}
	}
	if users != 1 {
		t.Fatalf("user messages = %d, want 1 (resume must not add a user message)", users)
	}
	if agents != 1 {
		t.Fatalf("agent messages = %d, want 1", agents)
	}

	// Once a terminal message exists, another click remains a harmless no-op.
	w = httptest.NewRecorder()
	srv.handleResumeConversation(w, httptest.NewRequest(http.MethodPost, "/", nil), conversationID)
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), "not_applicable") {
		t.Fatalf("completed resume = %d %q, want accepted not_applicable", w.Code, w.Body.String())
	}
}

func TestResumeInterruptedToolPersistsResult(t *testing.T) {
	t.Parallel()
	srv, database, predictableService := newTestServer(t)
	defer stopActiveConversationLoops(srv)
	ctx := context.Background()
	conversationID := seedManualInterruption(t, database, false)
	_, err := database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conversationID,
		Type:           db.MessageTypeAgent,
		LLMData: llm.Message{
			Role: llm.MessageRoleAssistant,
			Content: []llm.Content{
				{Type: llm.ContentTypeToolUse, ID: "toolu_interrupted", ToolName: "bash"},
			},
		},
	})
	if err != nil {
		t.Fatalf("create tool call: %v", err)
	}
	// An unreadable row must not block recording the interrupted result.
	corrupt := `{"Content":"not a list"}`
	if err := database.QueriesTx(ctx, func(q *generated.Queries) error {
		_, err := q.CreateMessage(ctx, generated.CreateMessageParams{
			MessageID: "corrupt", ConversationID: conversationID, SequenceID: 900,
			Generation: 1, Type: string(db.MessageTypeAgent), LlmData: &corrupt,
		})
		return err
	}); err != nil {
		t.Fatalf("create corrupt message: %v", err)
	}

	w := httptest.NewRecorder()
	srv.handleResumeConversation(w, httptest.NewRequest(http.MethodPost, "/", nil), conversationID)
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), "resuming") {
		t.Fatalf("resume = %d %s", w.Code, w.Body.String())
	}
	messages, err := database.ListMessages(ctx, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	for _, message := range messages {
		if message.UserData == nil || !strings.Contains(*message.UserData, `"interrupted_tool_result":true`) {
			continue
		}
		if message.LlmData == nil {
			t.Fatal("interrupted result has no LLM data")
		}
		var result llm.Message
		if err := json.Unmarshal([]byte(*message.LlmData), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Content) != 1 || result.Content[0].ToolUseID != "toolu_interrupted" ||
			!result.Content[0].ToolError || len(result.Content[0].ToolResult) != 1 ||
			result.Content[0].ToolResult[0].Text != "Interrupted" {
			t.Fatalf("unexpected interrupted result: %+v", result)
		}
		count++
	}
	if count != 1 {
		t.Fatalf("persisted interrupted results = %d, want 1", count)
	}
	if result, err := database.RecordInterruptedToolResults(ctx, conversationID); err != nil {
		t.Fatal(err)
	} else if result != nil {
		t.Fatalf("repeated recording returned message %s, want nil", result.MessageID)
	}
	after, err := database.ListMessages(ctx, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	var afterCount int
	for _, message := range after {
		if message.UserData != nil && strings.Contains(*message.UserData, `"interrupted_tool_result":true`) {
			afterCount++
		}
	}
	if afterCount != 1 {
		t.Fatalf("retry added duplicate interrupted results: %d", afterCount)
	}
	waitFor(t, 5*time.Second, func() bool { return predictableService.GetLastRequest() != nil })
	var sent int
	for _, message := range predictableService.GetLastRequest().Messages {
		for _, content := range message.Content {
			if content.Type == llm.ContentTypeToolResult && content.ToolUseID == "toolu_interrupted" {
				sent++
			}
		}
	}
	if sent != 1 {
		t.Fatalf("model received %d interrupted tool results, want 1", sent)
	}
}

func TestSendAfterInterruptedToolPersistsResult(t *testing.T) {
	t.Parallel()
	srv, database, service := newTestServer(t)
	defer stopActiveConversationLoops(srv)
	ctx := context.Background()
	conversationID := seedManualInterruption(t, database, false)
	_, err := database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conversationID,
		Type:           db.MessageTypeAgent,
		LLMData: llm.Message{
			Role:    llm.MessageRoleAssistant,
			Content: []llm.Content{{Type: llm.ContentTypeToolUse, ID: "toolu_interrupted", ToolName: "bash"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := srv.getOrCreateConversationManager(ctx, conversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AcceptUserMessage(ctx, service, "predictable", llm.UserStringMessage("echo: continued")); err != nil {
		t.Fatal(err)
	}
	messages, err := database.ListMessages(ctx, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.UserData != nil && strings.Contains(*message.UserData, `"interrupted_tool_result":true`) {
			return
		}
	}
	t.Fatal("send did not persist interrupted tool result")
}

func TestInterruptedToolResultStreamed(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"continue", "send", "upgrade", "upgrade_setup_failure"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			srv, database, service := newTestServer(t)
			defer stopActiveConversationLoops(srv)
			ctx := t.Context()
			var convID string
			automatic := strings.HasPrefix(mode, "upgrade")
			if automatic {
				convID = seedInterruptedConversation(t, database, nil)
			} else {
				convID = seedManualInterruption(t, database, false)
			}
			if _, err := database.CreateMessage(ctx, db.CreateMessageParams{
				ConversationID: convID, Type: db.MessageTypeAgent,
				LLMData: llm.Message{
					Role:    llm.MessageRoleAssistant,
					Content: []llm.Content{{Type: llm.ContentTypeToolUse, ID: "toolu_streamed", ToolName: "bash"}},
				},
			}); err != nil {
				t.Fatal(err)
			}
			var resume db.UpgradeResume
			if automatic {
				resume = reserveUpgradeResume(t, database, convID)
			}
			manager, err := srv.getOrCreateConversationManager(ctx, convID, "")
			if err != nil {
				t.Fatal(err)
			}
			streamCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			streams := map[string]func() (StreamResponse, bool){
				"global": srv.streamPub.Subscribe(streamCtx, -1),
				"legacy": manager.subpub.Subscribe(streamCtx, -1),
			}
			resolve := func(string) (llm.Service, error) { return service, nil }
			setupErr := errors.New("loop setup failed")
			if mode == "upgrade_setup_failure" {
				manager.decorateService = func(llm.Service) (llm.Service, error) { return nil, setupErr }
			}
			switch mode {
			case "continue":
				err = manager.ContinueInterruptedTurn(ctx, "predictable", resolve)
			case "send":
				_, err = manager.AcceptUserMessage(ctx, service, "predictable", llm.UserStringMessage("echo: continued"))
			default:
				err = manager.ResumeInterruptedTurnAfterUpgrade(ctx, resume, "predictable", resolve, resumeWarningText)
			}
			if mode == "upgrade_setup_failure" {
				if !errors.Is(err, setupErr) {
					t.Fatalf("resume error = %v, want %v", err, setupErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var result *generated.Message
			for _, message := range listMessages(t, database, convID) {
				if message.UserData != nil && strings.Contains(*message.UserData, `"interrupted_tool_result":true`) {
					result = &message
				}
			}
			if result == nil {
				t.Fatal("no persisted interrupted result")
			}
			// Publication must precede returning to the caller; drain only events
			// already queued so a missing result cannot hide behind a later retry.
			cancel()
			for name, next := range streams {
				found := 0
				for event, ok := next(); ok; event, ok = next() {
					if event.ConversationID != convID {
						continue
					}
					for _, message := range event.Messages {
						if message.MessageID == result.MessageID {
							found++
						} else if message.SequenceID > result.SequenceID && found == 0 {
							t.Errorf("%s stream skipped interrupted result before sequence %d", name, message.SequenceID)
						}
					}
				}
				if found != 1 {
					t.Errorf("%s stream delivered interrupted result %d times, want 1", name, found)
				}
			}
		})
	}
}

func TestResumeInterruptedRetryAfterTerminalError(t *testing.T) {
	t.Parallel()
	srv, database, predictableService := newTestServer(t)
	predictableService.SetResponseDelay(300 * time.Millisecond)
	conversationID := seedManualInterruption(t, database, true)

	w := httptest.NewRecorder()
	srv.handleResumeConversation(w, httptest.NewRequest(http.MethodPost, "/", nil), conversationID)
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), "resuming") {
		t.Fatalf("resume retry = %d %q, want accepted resuming", w.Code, w.Body.String())
	}

	// The old retry affordance remains visible until a new assistant row lands.
	// It must share Continue's dedupe stamp rather than scheduling a second run.
	retry := httptest.NewRecorder()
	srv.handleRetryConversation(retry, httptest.NewRequest(http.MethodPost, "/", nil), conversationID)
	if retry.Code != http.StatusAccepted || !strings.Contains(retry.Body.String(), "not_applicable") {
		t.Fatalf("retry after Continue = %d %q, want accepted not_applicable", retry.Code, retry.Body.String())
	}

	waitFor(t, 10*time.Second, func() bool {
		messages, err := database.ListMessages(context.Background(), conversationID)
		if err != nil {
			return false
		}
		for _, message := range messages {
			if message.Type == string(db.MessageTypeAgent) && isAgentEndOfTurn(&message) {
				return true
			}
		}
		return false
	})
	messages, err := database.ListMessages(context.Background(), conversationID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	agents := 0
	for _, message := range messages {
		if message.Type == string(db.MessageTypeAgent) {
			agents++
		}
	}
	if agents != 1 {
		t.Fatalf("agent messages = %d, want 1 after Continue+Retry race", agents)
	}
}

func TestResumeInterruptedConversationUsesPersistedModel(t *testing.T) {
	t.Parallel()
	srv, database, _ := newTestServer(t)
	conversationID := seedManualInterruption(t, database, false)
	manager, err := srv.getOrCreateConversationManager(context.Background(), conversationID, "")
	if err != nil {
		t.Fatalf("get manager: %v", err)
	}
	if got := manager.GetModel(); got != "predictable" {
		t.Fatalf("initial model = %q, want predictable", got)
	}
	if err := database.ForceUpdateConversationModel(context.Background(), conversationID, "model-after-switch"); err != nil {
		t.Fatalf("ForceUpdateConversationModel: %v", err)
	}
	// Mirrors an idle /model change: DB is current, while ResetLoop leaves the
	// old in-memory model ID until Hydrate runs again.
	manager.ResetLoop()
	if got := manager.GetModel(); got != "predictable" {
		t.Fatalf("test setup lost stale model, got %q", got)
	}

	w := httptest.NewRecorder()
	srv.handleResumeConversation(w, httptest.NewRequest(http.MethodPost, "/", nil), conversationID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("resume status = %d, want 202: %s", w.Code, w.Body.String())
	}
	if got := manager.GetModel(); got != "model-after-switch" {
		t.Fatalf("resumed model = %q, want model-after-switch", got)
	}
}
