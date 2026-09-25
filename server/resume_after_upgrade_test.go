package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// seedInterruptedConversation creates a conversation whose last message is a
// user message and whose agent_working flag is TRUE: exactly the state a
// process leaves behind when it exits mid-turn.
func seedInterruptedConversation(t *testing.T, database *db.DB, parentID *string) string {
	t.Helper()
	ctx := t.Context()
	model := "predictable"
	conv, err := database.CreateConversation(ctx, nil, true, nil, &model, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if parentID != nil {
		if _, err := database.UpdateConversationParent(ctx, conv.ConversationID, *parentID); err != nil {
			t.Fatalf("UpdateConversationParent: %v", err)
		}
	}
	if _, err := database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           db.MessageTypeUser,
		LLMData: llm.Message{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}},
		},
	}); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if err := database.SetConversationAgentWorking(ctx, conv.ConversationID, true); err != nil {
		t.Fatalf("SetConversationAgentWorking: %v", err)
	}
	return conv.ConversationID
}

func reserveUpgradeResume(t *testing.T, database *db.DB, conversationID string) db.UpgradeResume {
	t.Helper()
	ctx := t.Context()
	if err := database.SetSetting(ctx, db.ResumeAfterUpgradeSettingKey, "1"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	resumes, err := database.ConsumeResumeAfterUpgrade(ctx)
	if err != nil {
		t.Fatalf("ConsumeResumeAfterUpgrade: %v", err)
	}
	if len(resumes) != 1 || resumes[0].ConversationID != conversationID {
		t.Fatalf("resumes = %v, want token for %s", resumes, conversationID)
	}
	return resumes[0]
}

// startTestServer starts the real server lifecycle (StartWithListeners) on an
// ephemeral port so the resume-after-upgrade startup path runs exactly as it
// does in production.
func startTestServer(t *testing.T, srv *Server) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.StartWithListeners(listener, "") }()
	t.Cleanup(func() {
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("server exited with error: %v", err)
			}
		default:
		}
	})
}

func countByType(msgs []generated.Message, msgType db.MessageType) int {
	n := 0
	for _, m := range msgs {
		if m.Type == string(msgType) {
			n++
		}
	}
	return n
}

// TestResumeAfterUpgradeRestart: with the one-shot flag set, a conversation
// left mid-turn is resumed on the next boot — one new assistant turn, no new
// user message, and one warning row telling the user the turn was re-fired.
func TestResumeAfterUpgradeRestart(t *testing.T) {
	t.Parallel()
	srv, database, _ := newTestServer(t)
	convID := seedInterruptedConversation(t, database, nil)
	if err := database.SetSetting(t.Context(), db.ResumeAfterUpgradeSettingKey, "1"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	startTestServer(t, srv)

	waitFor(t, 15*time.Second, func() bool {
		return countByType(listMessages(t, database, convID), db.MessageTypeAgent) == 1
	})
	// The turn must finish and clear the flag.
	waitFor(t, 15*time.Second, func() bool { return !srv.IsAgentWorking(convID) })

	msgs := listMessages(t, database, convID)
	if got := countByType(msgs, db.MessageTypeUser); got != 1 {
		t.Errorf("user messages = %d, want 1 (resume must not add a user message)", got)
	}
	if got := countByType(msgs, db.MessageTypeAgent); got != 1 {
		t.Errorf("agent messages = %d, want exactly 1 new turn", got)
	}
	if got := countByType(msgs, db.MessageTypeWarning); got != 1 {
		t.Errorf("warning messages = %d, want 1", got)
	}
	for _, m := range msgs {
		if m.Type != string(db.MessageTypeWarning) {
			continue
		}
		var ud map[string]any
		if m.UserData == nil {
			t.Fatal("warning has no user_data")
		}
		if err := json.Unmarshal([]byte(*m.UserData), &ud); err != nil {
			t.Fatalf("unmarshal warning user_data: %v", err)
		}
		if ud["text"] != resumeWarningText {
			t.Errorf("warning text = %v, want the resume warning", ud["text"])
		}
	}
	// The flag is one-shot.
	if v, err := database.GetSetting(t.Context(), db.ResumeAfterUpgradeSettingKey); err != nil || v != "" {
		t.Errorf("resume flag after boot = %q, %v; want consumed", v, err)
	}
}

func TestUpgradeResumeRechecksWorkingAfterConcurrentModelSwitch(t *testing.T) {
	t.Parallel()
	srv, database, predictableService := newTestServer(t)
	convID := seedInterruptedConversation(t, database, nil)
	resume := reserveUpgradeResume(t, database, convID)
	manager, err := srv.getOrCreateConversationManager(context.Background(), convID, "")
	if err != nil {
		t.Fatalf("get conversation manager: %v", err)
	}

	// Hold the same lock used by /model so the resume request has already been
	// launched but cannot validate stale state until the simulated switch has
	// persisted its model and cancellation.
	manager.modelSettingsMu.Lock()
	result := make(chan error, 1)
	resolverCalled := false
	go func() {
		result <- manager.ResumeInterruptedTurnAfterUpgrade(
			context.Background(),
			resume,
			"predictable",
			func(string) (llm.Service, error) {
				resolverCalled = true
				return predictableService, nil
			},
			resumeWarningText,
		)
	}()
	if err := database.ForceUpdateConversationModel(context.Background(), convID, "model-after-switch"); err != nil {
		manager.modelSettingsMu.Unlock()
		t.Fatalf("ForceUpdateConversationModel: %v", err)
	}
	if err := database.SetConversationAgentWorking(context.Background(), convID, false); err != nil {
		manager.modelSettingsMu.Unlock()
		t.Fatalf("SetConversationAgentWorking: %v", err)
	}
	manager.syncAgentWorking(false)
	manager.ResetLoop()
	manager.modelSettingsMu.Unlock()

	if err := <-result; !errors.Is(err, errInterruptedTurnNotApplicable) {
		t.Fatalf("resume error = %v, want not applicable", err)
	}
	if resolverCalled {
		t.Fatal("resume selected a service after the model switch cancelled the turn")
	}
	if got := countByType(listMessages(t, database, convID), db.MessageTypeWarning); got != 0 {
		t.Fatalf("resume warning count = %d, want 0 for skipped turn", got)
	}
}

func TestUpgradeResumeDoesNotClaimFreshTurn(t *testing.T) {
	t.Parallel()
	srv, database, predictableService := newTestServer(t)
	convID := seedInterruptedConversation(t, database, nil)
	resume := reserveUpgradeResume(t, database, convID)
	manager, err := srv.getOrCreateConversationManager(context.Background(), convID, "")
	if err != nil {
		t.Fatalf("get conversation manager: %v", err)
	}

	if _, err := database.CreateMessage(t.Context(), db.CreateMessageParams{
		ConversationID: convID,
		Type:           db.MessageTypeUser,
		LLMData:        llm.UserStringMessage("fresh turn"),
		MarkAgentStart: true,
	}); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	manager.syncAgentWorking(true)

	resolverCalled := false
	err = manager.ResumeInterruptedTurnAfterUpgrade(
		context.Background(),
		resume,
		"predictable",
		func(string) (llm.Service, error) {
			resolverCalled = true
			return predictableService, nil
		},
		resumeWarningText,
	)
	if !errors.Is(err, errInterruptedTurnNotApplicable) {
		t.Fatalf("resume error = %v, want not applicable", err)
	}
	if resolverCalled {
		t.Fatal("upgrade resume selected a service after a fresh turn started")
	}
	if got := countByType(listMessages(t, database, convID), db.MessageTypeWarning); got != 0 {
		t.Fatalf("resume warning count = %d, want 0 for fresh turn", got)
	}
	conversation, err := database.GetConversationByID(t.Context(), convID)
	if err != nil {
		t.Fatalf("GetConversationByID: %v", err)
	}
	if !conversation.AgentWorking || conversation.TurnInterrupted {
		t.Fatalf("fresh turn state working=%v interrupted=%v, want true/false", conversation.AgentWorking, conversation.TurnInterrupted)
	}
}

func TestUpgradeResumeDoesNotDuplicateRetry(t *testing.T) {
	t.Parallel()
	srv, database, predictableService := newTestServer(t)
	convID := seedInterruptedConversation(t, database, nil)
	manager, err := srv.getOrCreateConversationManager(context.Background(), convID, "")
	if err != nil {
		t.Fatalf("get conversation manager: %v", err)
	}
	errorMessage, err := database.CreateMessage(t.Context(), db.CreateMessageParams{
		ConversationID: convID,
		Type:           db.MessageTypeError,
		LLMData: llm.Message{
			Role:      llm.MessageRoleAssistant,
			Content:   []llm.Content{{Type: llm.ContentTypeText, Text: "retry me"}},
			EndOfTurn: true,
		},
		UserData: map[string]any{"retryable": true},
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	resume := reserveUpgradeResume(t, database, convID)
	manager.mu.Lock()
	manager.lastRetriedErrorMessageID = errorMessage.MessageID
	manager.mu.Unlock()

	resolverCalled := false
	err = manager.ResumeInterruptedTurnAfterUpgrade(
		context.Background(),
		resume,
		"predictable",
		func(string) (llm.Service, error) {
			resolverCalled = true
			return predictableService, nil
		},
		resumeWarningText,
	)
	if !errors.Is(err, errInterruptedTurnNotApplicable) {
		t.Fatalf("resume error = %v, want not applicable", err)
	}
	if resolverCalled {
		t.Fatal("upgrade resume selected a service after Retry already started")
	}
	conversation, err := database.GetConversationByID(t.Context(), convID)
	if err != nil {
		t.Fatalf("GetConversationByID: %v", err)
	}
	if !conversation.AgentWorking || conversation.TurnInterrupted {
		t.Fatalf("retried turn state working=%v interrupted=%v, want true/false", conversation.AgentWorking, conversation.TurnInterrupted)
	}
}

func TestUpgradeResumeStampsRetriedError(t *testing.T) {
	t.Parallel()
	srv, database, predictableService := newTestServer(t)
	convID := seedInterruptedConversation(t, database, nil)
	manager, err := srv.getOrCreateConversationManager(context.Background(), convID, "")
	if err != nil {
		t.Fatalf("get conversation manager: %v", err)
	}
	errorMessage, err := database.CreateMessage(t.Context(), db.CreateMessageParams{
		ConversationID: convID,
		Type:           db.MessageTypeError,
		LLMData: llm.Message{
			Role:      llm.MessageRoleAssistant,
			Content:   []llm.Content{{Type: llm.ContentTypeText, Text: "retry me"}},
			EndOfTurn: true,
		},
		UserData: map[string]any{"retryable": true},
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	resume := reserveUpgradeResume(t, database, convID)

	if err := manager.ResumeInterruptedTurnAfterUpgrade(
		context.Background(),
		resume,
		"predictable",
		func(string) (llm.Service, error) { return predictableService, nil },
		resumeWarningText,
	); err != nil {
		t.Fatalf("ResumeInterruptedTurnAfterUpgrade: %v", err)
	}
	manager.mu.Lock()
	got := manager.lastRetriedErrorMessageID
	manager.mu.Unlock()
	if got != errorMessage.MessageID {
		t.Fatalf("last retried error = %q, want %q", got, errorMessage.MessageID)
	}
}

func TestCancelPendingUpgradeResumeWithManager(t *testing.T) {
	t.Parallel()
	srv, database, predictableService := newTestServer(t)
	convID := seedInterruptedConversation(t, database, nil)
	resume := reserveUpgradeResume(t, database, convID)
	manager, err := srv.getOrCreateConversationManager(t.Context(), convID, "")
	if err != nil {
		t.Fatalf("get conversation manager: %v", err)
	}
	if err := manager.CancelConversation(t.Context()); err != nil {
		t.Fatalf("CancelConversation: %v", err)
	}

	resolverCalled := false
	err = manager.ResumeInterruptedTurnAfterUpgrade(
		t.Context(),
		resume,
		"predictable",
		func(string) (llm.Service, error) {
			resolverCalled = true
			return predictableService, nil
		},
		resumeWarningText,
	)
	if !errors.Is(err, errInterruptedTurnNotApplicable) {
		t.Fatalf("resume error = %v, want not applicable", err)
	}
	if resolverCalled {
		t.Fatal("cancelled upgrade resume selected a service")
	}
	conversation, err := database.GetConversationByID(t.Context(), convID)
	if err != nil {
		t.Fatalf("GetConversationByID: %v", err)
	}
	if conversation.AgentWorking || conversation.TurnInterrupted {
		t.Fatalf("cancelled state working=%v interrupted=%v, want false/false", conversation.AgentWorking, conversation.TurnInterrupted)
	}
}

func TestCancelPendingUpgradeResumeWithoutManager(t *testing.T) {
	t.Parallel()
	srv, database, _ := newTestServer(t)
	convID := seedInterruptedConversation(t, database, nil)
	resume := reserveUpgradeResume(t, database, convID)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/conversation/"+convID+"/cancel", nil)
	srv.handleCancelConversation(w, r, convID)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, want 200: %s", w.Code, w.Body.String())
	}
	conversation, err := database.GetConversationByID(t.Context(), convID)
	if err != nil {
		t.Fatalf("GetConversationByID: %v", err)
	}
	if conversation.AgentWorking || conversation.TurnInterrupted {
		t.Fatalf("cancelled state working=%v interrupted=%v, want false/false", conversation.AgentWorking, conversation.TurnInterrupted)
	}
	if err := srv.resumeConversation(t.Context(), resume); err != nil {
		t.Fatalf("late resume worker: %v", err)
	}
	if got := countByType(listMessages(t, database, convID), db.MessageTypeWarning); got != 0 {
		t.Fatalf("resume warning count = %d, want 0 after cancellation", got)
	}
}

func TestManagerCreationFailureBecomesManualInterruption(t *testing.T) {
	t.Parallel()
	srv, database, _ := newTestServer(t)
	convID := seedInterruptedConversation(t, database, nil)
	resume := reserveUpgradeResume(t, database, convID)
	srv.mu.Lock()
	srv.deletingConversations[convID] = true
	srv.mu.Unlock()

	if err := srv.resumeConversation(t.Context(), resume); err == nil {
		t.Fatal("resume succeeded while manager creation was blocked")
	}
	conversation, err := database.GetConversationByID(t.Context(), convID)
	if err != nil {
		t.Fatalf("GetConversationByID: %v", err)
	}
	if conversation.AgentWorking || !conversation.TurnInterrupted {
		t.Fatalf("failed manager state working=%v interrupted=%v, want false/true", conversation.AgentWorking, conversation.TurnInterrupted)
	}
}

func TestPreclaimFailureBecomesManualInterruption(t *testing.T) {
	t.Parallel()
	srv, database, predictableService := newTestServer(t)
	conversation, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if err := database.SetConversationAgentWorking(t.Context(), conversation.ConversationID, true); err != nil {
		t.Fatalf("SetConversationAgentWorking: %v", err)
	}
	resume := reserveUpgradeResume(t, database, conversation.ConversationID)
	manager := NewConversationManager(
		conversation.ConversationID,
		database,
		srv.logger,
		srv.toolSetConfig,
		srv.integrationSkills,
		nil,
		nil,
		nil,
		nil,
		srv.streamPub,
	)
	manager.mu.Lock()
	manager.hydrated = true
	manager.agentWorking = true
	manager.mu.Unlock()

	err = manager.ResumeInterruptedTurnAfterUpgrade(
		t.Context(),
		resume,
		"predictable",
		func(string) (llm.Service, error) { return predictableService, nil },
		resumeWarningText,
	)
	if err == nil {
		t.Fatal("resume unexpectedly succeeded without an actionable message")
	}
	got, err := database.GetConversationByID(t.Context(), conversation.ConversationID)
	if err != nil {
		t.Fatalf("GetConversationByID: %v", err)
	}
	if got.AgentWorking || !got.TurnInterrupted {
		t.Fatalf("preclaim failure state working=%v interrupted=%v, want false/true", got.AgentWorking, got.TurnInterrupted)
	}
}

func TestFailedUpgradeResumeBecomesManualInterruption(t *testing.T) {
	t.Parallel()
	srv, database, _ := newTestServer(t)
	convID := seedInterruptedConversation(t, database, nil)
	resume := reserveUpgradeResume(t, database, convID)
	manager, err := srv.getOrCreateConversationManager(context.Background(), convID, "")
	if err != nil {
		t.Fatalf("get conversation manager: %v", err)
	}

	err = manager.ResumeInterruptedTurnAfterUpgrade(
		context.Background(),
		resume,
		"predictable",
		func(string) (llm.Service, error) { return nil, errors.New("service unavailable") },
		resumeWarningText,
	)
	if err == nil {
		t.Fatal("upgrade resume succeeded when service resolution failed")
	}
	conversation, err := database.GetConversationByID(context.Background(), convID)
	if err != nil {
		t.Fatalf("GetConversationByID: %v", err)
	}
	if conversation.AgentWorking || !conversation.TurnInterrupted {
		t.Fatalf("failed resume state working=%v interrupted=%v, want false/true", conversation.AgentWorking, conversation.TurnInterrupted)
	}
	if got := countByType(listMessages(t, database, convID), db.MessageTypeWarning); got != 0 {
		t.Fatalf("resume warning count = %d, want 0 when setup failed", got)
	}
	if manager.IsAgentWorking() {
		t.Fatal("manager working should be false after failed resume")
	}
}

func TestUpgradeResumeFailureAfterInterruptedResult(t *testing.T) {
	t.Parallel()
	for _, newerInput := range []bool{false, true} {
		name := "recover_interrupted_turn"
		if newerInput {
			name = "preserve_newer_input"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv, database, service := newTestServer(t)
			defer stopActiveConversationLoops(srv)
			ctx := t.Context()
			convID := seedInterruptedConversation(t, database, nil)
			if _, err := database.CreateMessage(ctx, db.CreateMessageParams{
				ConversationID: convID,
				Type:           db.MessageTypeAgent,
				LLMData: llm.Message{
					Role:    llm.MessageRoleAssistant,
					Content: []llm.Content{{Type: llm.ContentTypeToolUse, ID: "toolu_recovery", ToolName: "bash"}},
				},
			}); err != nil {
				t.Fatal(err)
			}
			resume := reserveUpgradeResume(t, database, convID)
			manager, err := srv.getOrCreateConversationManager(ctx, convID, "")
			if err != nil {
				t.Fatal(err)
			}
			setupErr := errors.New("loop setup failed after recording interrupted result")
			var resultSequence int64
			manager.decorateService = func(llm.Service) (llm.Service, error) {
				latest, err := database.GetLatestActionableMessage(ctx, convID)
				if err != nil {
					return nil, err
				}
				var message llm.Message
				if latest.LlmData == nil {
					return nil, errors.New("missing interrupted result")
				}
				if err := json.Unmarshal([]byte(*latest.LlmData), &message); err != nil {
					return nil, err
				}
				if len(message.Content) != 1 || message.Content[0].ToolUseID != "toolu_recovery" ||
					!message.Content[0].ToolError || latest.SequenceID <= resume.MaxUserSequenceID {
					return nil, errors.New("interrupted result was not persisted before setup")
				}
				resultSequence = latest.SequenceID
				if newerInput {
					if _, err := database.CreateMessage(ctx, db.CreateMessageParams{
						ConversationID: convID,
						Type:           db.MessageTypeUser,
						LLMData:        llm.UserStringMessage("newer input"),
						MarkAgentStart: true,
					}); err != nil {
						return nil, err
					}
				}
				return nil, setupErr
			}
			err = manager.ResumeInterruptedTurnAfterUpgrade(ctx, resume, "predictable",
				func(string) (llm.Service, error) { return service, nil }, resumeWarningText)
			if !errors.Is(err, setupErr) {
				t.Fatalf("resume error = %v, want %v", err, setupErr)
			}
			if resultSequence == 0 {
				t.Fatal("setup hook never observed the interrupted result")
			}
			conversation, err := database.GetConversationByID(ctx, convID)
			if err != nil {
				t.Fatal(err)
			}
			if conversation.AgentWorking != newerInput || conversation.TurnInterrupted == newerInput {
				t.Fatalf("working=%v interrupted=%v, want %v/%v",
					conversation.AgentWorking, conversation.TurnInterrupted, newerInput, !newerInput)
			}
			if manager.IsAgentWorking() != newerInput {
				t.Fatalf("manager working=%v, want %v", manager.IsAgentWorking(), newerInput)
			}
		})
	}
}

// TestResumeAfterUpgradeSkips covers startup classification: conversations that
// are not working, managed children, and conversations whose turn already
// finished are not reserved for automatic resume and have stale working state
// cleared before listeners open.
func TestResumeAfterUpgradeSkips(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// seed returns the conversation id that must not be reserved.
		seed func(t *testing.T, database *db.DB) string
	}{
		{
			name: "not working",
			seed: func(t *testing.T, database *db.DB) string {
				id := seedInterruptedConversation(t, database, nil)
				if err := database.SetConversationAgentWorking(t.Context(), id, false); err != nil {
					t.Fatalf("SetConversationAgentWorking: %v", err)
				}
				return id
			},
		},
		{
			name: "subagent conversation",
			seed: func(t *testing.T, database *db.DB) string {
				parent := seedInterruptedConversation(t, database, nil)
				return seedInterruptedConversation(t, database, &parent)
			},
		},
		{
			name: "turn already finished with trailing bookkeeping",
			seed: func(t *testing.T, database *db.DB) string {
				id := seedInterruptedConversation(t, database, nil)
				if _, err := database.CreateMessage(t.Context(), db.CreateMessageParams{
					ConversationID: id,
					Type:           db.MessageTypeAgent,
					LLMData: llm.Message{
						Role:      llm.MessageRoleAssistant,
						Content:   []llm.Content{{Type: llm.ContentTypeText, Text: "done"}},
						EndOfTurn: true,
					},
				}); err != nil {
					t.Fatalf("CreateMessage: %v", err)
				}
				if _, err := database.CreateMessage(context.Background(), db.CreateMessageParams{
					ConversationID: id,
					Type:           db.MessageTypeUser,
					LLMData:        llm.UserStringMessage("changed cwd"),
					UserData:       map[string]any{"cwd_change": true, "to": "/tmp"},
				}); err != nil {
					t.Fatalf("create cwd-change message: %v", err)
				}
				return id
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, database, _ := newTestServer(t)
			convID := tt.seed(t, database)
			before := listMessages(t, database, convID)
			if err := database.SetSetting(t.Context(), db.ResumeAfterUpgradeSettingKey, "1"); err != nil {
				t.Fatalf("SetSetting: %v", err)
			}

			resumes, err := database.ConsumeResumeAfterUpgrade(t.Context())
			if err != nil {
				t.Fatalf("ConsumeResumeAfterUpgrade: %v", err)
			}
			for _, resume := range resumes {
				if resume.ConversationID == convID {
					t.Fatalf("skipped conversation %s was reserved for resume: %v", convID, resumes)
				}
			}

			if got := len(listMessages(t, database, convID)); got != len(before) {
				t.Errorf("message count = %d, want unchanged %d", got, len(before))
			}
			conv, err := database.GetConversationByID(t.Context(), convID)
			if err != nil {
				t.Fatalf("GetConversationByID: %v", err)
			}
			if conv.AgentWorking || conv.TurnInterrupted {
				t.Errorf("skipped state working=%v interrupted=%v, want false/false", conv.AgentWorking, conv.TurnInterrupted)
			}
		})
	}
}

// An upgrade restart mid-tool resumes automatically without ever showing the
// interrupted state, so it must persist the orphaned call's result itself.
func TestResumeAfterUpgradePersistsInterruptedToolResult(t *testing.T) {
	t.Parallel()
	srv, database, predictableService := newTestServer(t)
	defer stopActiveConversationLoops(srv)
	convID := seedInterruptedConversation(t, database, nil)
	if _, err := database.CreateMessage(t.Context(), db.CreateMessageParams{
		ConversationID: convID,
		Type:           db.MessageTypeAgent,
		LLMData: llm.Message{
			Role:    llm.MessageRoleAssistant,
			Content: []llm.Content{{Type: llm.ContentTypeToolUse, ID: "toolu_upgrade", ToolName: "bash"}},
		},
	}); err != nil {
		t.Fatalf("create tool call: %v", err)
	}
	resume := reserveUpgradeResume(t, database, convID)
	manager, err := srv.getOrCreateConversationManager(context.Background(), convID, "")
	if err != nil {
		t.Fatalf("get conversation manager: %v", err)
	}
	if err := manager.ResumeInterruptedTurnAfterUpgrade(
		context.Background(),
		resume,
		"predictable",
		func(string) (llm.Service, error) { return predictableService, nil },
		resumeWarningText,
	); err != nil {
		t.Fatalf("resume: %v", err)
	}

	var found int
	for _, message := range listMessages(t, database, convID) {
		if message.UserData == nil || message.LlmData == nil {
			continue
		}
		var data llm.Message
		if err := json.Unmarshal([]byte(*message.LlmData), &data); err != nil {
			t.Fatal(err)
		}
		for _, content := range data.Content {
			if content.Type == llm.ContentTypeToolResult && content.ToolUseID == "toolu_upgrade" &&
				content.ToolError && len(content.ToolResult) == 1 && content.ToolResult[0].Text == "Interrupted" {
				found++
			}
		}
	}
	if found != 1 {
		t.Fatalf("persisted interrupted results = %d, want 1", found)
	}
}
