package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
	"shelley.exe.dev/models"
)

type chapterTestService struct {
	llm.Service
	calls     int
	err       error
	reply     string
	onRequest func()
	request   *llm.Request
}

func (s *chapterTestService) Do(_ context.Context, req *llm.Request) (*llm.Response, error) {
	s.calls++
	s.request = req
	if s.onRequest != nil {
		s.onRequest()
	}
	if len(req.Tools) != 0 || req.ThinkingLevel != llm.ThinkingLevelMedium {
		return nil, errors.New("chapter request must have no tools and medium thinking")
	}
	if s.err != nil {
		return nil, s.err
	}
	reply := s.reply
	if reply == "" {
		reply = `{"chapters":[]}`
	}
	return &llm.Response{Content: llm.TextContent(reply)}, nil
}

func newChapterIndexManager(t *testing.T, turns int, missing bool) (*ConversationManager, *chapterTestService, *chapterTestService) {
	t.Helper()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	id := seedConversation(t, database, turns*2)
	_, server := newTestStreamServer(t, database)
	conversation := &chapterTestService{Service: predictable.NewService()}
	workhorse := &chapterTestService{Service: predictable.NewService()}
	built := []models.Built{{ID: "gpt-6-sol", Provider: models.ProviderOpenAI, Service: conversation}}
	if !missing {
		built = append(built, models.Built{ID: "gpt-6-luna", Provider: models.ProviderOpenAI, Service: workhorse})
	}
	registry, err := models.NewManager(&models.Config{Models: built})
	if err != nil {
		t.Fatal(err)
	}
	server.llmManager = registry
	cm := &ConversationManager{server: server, db: database, conversationID: id, logger: server.logger}
	return cm, workhorse, conversation
}

func TestChapterIndexWorkhorse(t *testing.T) {
	for _, tc := range []struct {
		name      string
		turns     int
		missing   bool
		failed    bool
		wantCalls int
		role      conversationRole
	}{
		{name: "four turns use workhorse", turns: 4, wantCalls: 1},
		{name: "three turns do not call a model", turns: 3},
		{name: "two turns do not call a model", turns: 2},
		{name: "request failure does not fall back", turns: 4, failed: true, wantCalls: 1},
		{name: "missing workhorse does not fall back", turns: 4, missing: true},
		{name: "subagents do not index", turns: 4, role: roleSubagent},
		{name: "internal readers do not index", turns: 4, role: roleBtwReader},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cm, workhorse, conversation := newChapterIndexManager(t, tc.turns, tc.missing)
			cm.role = tc.role
			if tc.failed {
				workhorse.err = errors.New("workhorse unavailable")
			}
			messages := listMessages(t, cm.db, cm.conversationID)
			err := cm.indexChapters(t.Context(), messages[len(messages)-1].SequenceID, "gpt-6-sol")
			if (err != nil) != (tc.failed || tc.missing) {
				t.Fatalf("index error = %v", err)
			}
			if workhorse.calls != tc.wantCalls || conversation.calls != 0 {
				t.Fatalf("calls: workhorse %d, conversation %d; want %d, 0", workhorse.calls, conversation.calls, tc.wantCalls)
			}
			themes, err := cm.db.GetConversationThemes(t.Context(), cm.conversationID)
			if err != nil {
				t.Fatal(err)
			}
			wantCheckpoint := tc.wantCalls > 0 && !tc.failed && !tc.missing
			if (themes.ThroughMessageID != "") != wantCheckpoint {
				t.Fatalf("checkpoint = %q, want saved: %v", themes.ThroughMessageID, wantCheckpoint)
			}
		})
	}
}

func TestChapterIndexResults(t *testing.T) {
	for _, tc := range []struct {
		name         string
		reply        string
		wantError    bool
		cancel       bool
		wantChapters int
		anchor       int
	}{
		{name: "retrospective heading", reply: `{"chapters":[{"title":"Migration choices","message_id":"%s"}]}`, wantChapters: 1, anchor: 4},
		{name: "first opening message", reply: `{"chapters":[{"title":"Migration choices","message_id":"%s"}]}`, wantChapters: 1},
		{name: "second opening message", reply: `{"chapters":[{"title":"Migration choices","message_id":"%s"}]}`, wantChapters: 1, anchor: 2},
		{name: "no shift", reply: `{"chapters":[]}`},
		{name: "missing chapters", reply: `{}`, wantError: true},
		{name: "null chapters", reply: `{"chapters":null}`, wantError: true},
		{name: "invalid json", reply: `not json`, wantError: true},
		{name: "unknown anchor", reply: `{"chapters":[{"title":"Migration choices","message_id":"missing"}]}`, wantError: true},
		{name: "reversed anchors", reply: `{"chapters":[{"title":"Later topic","message_id":"u4"},{"title":"Earlier topic","message_id":"u3"}]}`, wantError: true},
		{name: "long title", reply: `{"chapters":[{"title":"` + strings.Repeat("é", 51) + `","message_id":"%s"}]}`, wantError: true, anchor: 4},
		{name: "cancelled response", reply: `{"chapters":[]}`, cancel: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cm, workhorse, _ := newChapterIndexManager(t, 4, false)
			messages := listMessages(t, cm.db, cm.conversationID)
			workhorse.reply = strings.ReplaceAll(tc.reply, "%s", fmt.Sprintf("u%d", tc.anchor/2+1))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				workhorse.onRequest = cancel
			}
			err := cm.indexChapters(ctx, messages[len(messages)-1].SequenceID, "gpt-6-sol")
			if (err != nil) != tc.wantError {
				t.Fatalf("index error = %v", err)
			}
			themes, err := cm.db.GetConversationThemes(t.Context(), cm.conversationID)
			if err != nil {
				t.Fatal(err)
			}
			if len(themes.Chapters) != tc.wantChapters || (themes.ThroughMessageID != "") == tc.wantError {
				t.Fatalf("themes = %+v", themes)
			}
			if tc.wantChapters > 0 && themes.Chapters[0].MessageID != messages[tc.anchor].MessageID {
				t.Fatal("heading was not anchored to the original topic shift")
			}
			rows := listMessages(t, cm.db, cm.conversationID)
			marker := rows[len(rows)-1]
			if marker.Type != string(db.MessageTypeSlug) || !marker.ExcludedFromContext || marker.LlmData != nil {
				t.Fatalf("indexer leaked into model context: %+v", marker)
			}
		})
	}
}

func TestChapterIndexCheckpoint(t *testing.T) {
	cm, workhorse, _ := newChapterIndexManager(t, 4, false)
	messages := listMessages(t, cm.db, cm.conversationID)
	if err := cm.indexChapters(t.Context(), messages[len(messages)-1].SequenceID, "gpt-6-sol"); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		message, err := cm.db.CreateMessage(t.Context(), db.CreateMessageParams{
			ConversationID: cm.conversationID, Type: db.MessageTypeUser, LLMData: llm.UserStringMessage("next turn"),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := cm.indexChapters(t.Context(), message.SequenceID, "gpt-6-sol"); err != nil {
			t.Fatal(err)
		}
		want := 1
		if i == 4 {
			want = 2
		}
		if workhorse.calls != want {
			t.Fatalf("after %d additional turns: calls = %d, want %d", i, workhorse.calls, want)
		}
	}
}

func TestChapterIndexIgnoresMachineMessages(t *testing.T) {
	cm, workhorse, _ := newChapterIndexManager(t, 3, false)
	var end int64
	for _, data := range []map[string]string{{"type": "system"}, {"compaction_carried": "true"}} {
		message, err := cm.db.CreateMessage(t.Context(), db.CreateMessageParams{
			ConversationID: cm.conversationID, Type: db.MessageTypeUser, LLMData: llm.UserStringMessage("hello"), UserData: data,
		})
		if err != nil {
			t.Fatal(err)
		}
		end = message.SequenceID
	}
	if err := cm.indexChapters(t.Context(), end, "gpt-6-sol"); err != nil {
		t.Fatal(err)
	}
	if workhorse.calls != 0 {
		t.Fatal("machine messages triggered chapter indexing")
	}
	message, err := cm.db.CreateMessage(t.Context(), db.CreateMessageParams{
		ConversationID: cm.conversationID, Type: db.MessageTypeUser, LLMData: llm.UserStringMessage("fourth turn"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := cm.indexChapters(t.Context(), end, "gpt-6-sol"); err != nil {
		t.Fatal(err)
	}
	if workhorse.calls != 0 {
		t.Fatal("indexing included messages after the completed turn")
	}
	if err := cm.indexChapters(t.Context(), message.SequenceID, "gpt-6-sol"); err != nil {
		t.Fatal(err)
	}
	if workhorse.calls != 1 {
		t.Fatal("fourth original turn did not trigger indexing")
	}
}

func TestChapterIndexPayload(t *testing.T) {
	cm, workhorse, _ := newChapterIndexManager(t, 4, false)
	messages := listMessages(t, cm.db, cm.conversationID)
	workhorse.reply = `{"chapters":[{"title":"Migration choices","message_id":"u3"}]}`
	if err := cm.indexChapters(t.Context(), messages[len(messages)-1].SequenceID, "gpt-6-sol"); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Transcript []struct {
			ID     string `json:"message_id"`
			Answer string `json:"answer_excerpt"`
		} `json:"transcript"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(workhorse.request.Messages[0].Content[0].Text, chapterIndexPrompt+"\n")), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Transcript) != 4 {
		t.Fatal(payload)
	}
	for i, m := range payload.Transcript {
		if m.ID != fmt.Sprintf("u%d", i+1) || (m.Answer != "") != (i < 3) {
			t.Fatal(payload)
		}
	}
	var end int64
	for range 4 {
		m, err := cm.db.CreateMessage(t.Context(), db.CreateMessageParams{ConversationID: cm.conversationID, Type: db.MessageTypeUser, LLMData: llm.UserStringMessage("follow-up")})
		if err != nil {
			t.Fatal(err)
		}
		end = m.SequenceID
	}
	workhorse.reply = `{"chapters":[{"title":"Opening task","message_id":"u1"}]}`
	if err := cm.indexChapters(t.Context(), end, "gpt-6-sol"); err != nil {
		t.Fatal(err)
	}
	themes, err := cm.db.GetConversationThemes(t.Context(), cm.conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(themes.Chapters) != 1 || themes.Chapters[0].MessageID != messages[0].MessageID || workhorse.calls != 2 {
		t.Fatal(themes)
	}
}
