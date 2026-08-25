package server

import (
	"context"
	"encoding/json"
	"net"
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
	ctx := context.Background()
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
	if err := database.SetSetting(context.Background(), db.ResumeAfterUpgradeSettingKey, "1"); err != nil {
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
	if v, err := database.GetSetting(context.Background(), db.ResumeAfterUpgradeSettingKey); err != nil || v != "" {
		t.Errorf("resume flag after boot = %q, %v; want consumed", v, err)
	}
}

// TestResumeAfterUpgradeSkips covers the resume filter: conversations that are
// not working, subagent conversations, and conversations whose turn already
// finished are left alone (and their agent_working flag cleared).
func TestResumeAfterUpgradeSkips(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// seed returns the conversation id to attempt to resume.
		seed func(t *testing.T, database *db.DB) string
	}{
		{
			name: "not working",
			seed: func(t *testing.T, database *db.DB) string {
				id := seedInterruptedConversation(t, database, nil)
				if err := database.SetConversationAgentWorking(context.Background(), id, false); err != nil {
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
			name: "turn already finished",
			seed: func(t *testing.T, database *db.DB) string {
				id := seedInterruptedConversation(t, database, nil)
				if _, err := database.CreateMessage(context.Background(), db.CreateMessageParams{
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
				return id
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, database, _ := newTestServer(t)
			convID := tt.seed(t, database)
			before := listMessages(t, database, convID)

			if err := srv.resumeConversation(context.Background(), convID); err != nil {
				t.Fatalf("resumeConversation: %v", err)
			}

			if got := len(listMessages(t, database, convID)); got != len(before) {
				t.Errorf("message count = %d, want unchanged %d (conversation must not be resumed)", got, len(before))
			}
			conv, err := database.GetConversationByID(context.Background(), convID)
			if err != nil {
				t.Fatalf("GetConversationByID: %v", err)
			}
			if conv.AgentWorking {
				t.Error("agent_working should be cleared for a skipped conversation")
			}
		})
	}
}
