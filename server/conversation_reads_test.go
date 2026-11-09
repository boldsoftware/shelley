package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

func TestConversationReadHTTPIdentityAndSharedListHash(t *testing.T) {
	srv, database, _ := newTestServer(t)
	srv.requireHeader = "X-Test-User"
	ctx := t.Context()
	for _, user := range []string{"alice", "bob"} {
		if _, err := database.ConversationReads(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	conversation, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conversation.ConversationID,
		Type:           db.MessageTypeAgent,
		LLMData:        llm.Message{Content: []llm.Content{{Type: llm.ContentTypeText, Text: "persisted answer"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	marks, err := database.ConversationAttention(ctx, []string{conversation.ConversationID})
	if err != nil {
		t.Fatal(err)
	}
	stamp := marks[conversation.ConversationID]
	_, hashBefore, err := srv.conversationListStream.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	readPath := "/api/conversation/" + conversation.ConversationID + "/read"
	request := func(user, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, readPath, strings.NewReader(body))
		if user != "" {
			req.Header.Set("X-Test-User", user)
		}
		rec := httptest.NewRecorder()
		srv.handleConversationRead(rec, req, conversation.ConversationID)
		return rec
	}
	if rec := request("", `{"read_at":0}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous acknowledgement status = %d", rec.Code)
	}
	for _, body := range []string{`{}`, `{"read_at":-1}`, fmt.Sprintf(`{"read_at":%d}`, stamp+10), `{"read_at":0,"user_id":"bob"}`, `{"read_at":0} {}`} {
		if rec := request("alice", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("acknowledgement %s status = %d, want 400", body, rec.Code)
		}
	}
	ack := request("alice", fmt.Sprintf(`{"read_at":%d}`, stamp))
	if ack.Code != http.StatusOK {
		t.Fatalf("acknowledgement status = %d: %s", ack.Code, ack.Body.String())
	}
	var event generated.ConversationReadMarkersRow
	if err := json.Unmarshal(ack.Body.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event.ConversationID != conversation.ConversationID || event.ReadAt != stamp {
		t.Fatalf("ack response = %+v", event)
	}

	for _, user := range []string{"alice", "bob"} {
		req := httptest.NewRequest(http.MethodGet, "/api/conversations", nil)
		req.Header.Set("X-Test-User", user)
		rec := httptest.NewRecorder()
		srv.handleConversations(rec, req)
		var conversations []ConversationWithState
		if err := json.Unmarshal(rec.Body.Bytes(), &conversations); err != nil {
			t.Fatal(err)
		}
		if len(conversations) != 1 || conversations[0].ReadAt == nil || conversations[0].Unread == nil {
			t.Fatalf("%s conversation list = %+v", user, conversations)
		}
		wantUnread := user == "bob"
		if *conversations[0].Unread != wantUnread {
			t.Fatalf("%s unread = %v, want %v", user, *conversations[0].Unread, wantUnread)
		}
	}

	for _, path := range []string{"/api/conversations", "/api/conversations?q=persisted&search_content=true"} {
		t.Run("local CLI "+path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			srv.handleConversations(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("local list status = %d: %s", rec.Code, rec.Body.String())
			}
			var rows []map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 {
				t.Fatalf("local list has %d rows, want 1", len(rows))
			}
			for _, field := range []string{"read_at", "unread"} {
				if _, ok := rows[0][field]; ok {
					t.Fatalf("local list includes private %s", field)
				}
			}
		})
	}

	t.Run("single-user local identity", func(t *testing.T) {
		srv.requireHeader = ""
		defer func() { srv.requireHeader = "X-Test-User" }()
		rec := httptest.NewRecorder()
		srv.handleConversations(rec, httptest.NewRequest(http.MethodGet, "/api/conversations", nil))
		var rows []ConversationWithState
		if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusOK || len(rows) != 1 || rows[0].ReadAt == nil || rows[0].Unread == nil {
			t.Fatalf("single-user list status = %d, rows = %+v", rec.Code, rows)
		}
	})

	_, hashAfter, err := srv.conversationListStream.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hashAfter != hashBefore {
		t.Fatalf("private acknowledgement changed shared list hash: %s -> %s", hashBefore, hashAfter)
	}
}

func TestConversationReadStreamFramesArePrivate(t *testing.T) {
	srv, _, _ := newTestServer(t)
	srv.requireHeader = "X-Test-User"
	private := StreamResponse{ConversationRead: &generated.ConversationReadMarkersRow{ConversationID: "c", ReadAt: 7}, readUserID: "alice"}
	for _, tc := range []struct {
		name string
		user string
		want bool
	}{
		{name: "owner", user: "alice", want: true},
		{name: "different user", user: "bob", want: false},
		{name: "anonymous", user: "", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/stream2", nil)
			if tc.user != "" {
				req.Header.Set("X-Test-User", tc.user)
			}
			if got := srv.streamEventVisible(req, private); got != tc.want {
				t.Fatalf("visible = %v, want %v", got, tc.want)
			}
		})
	}
	if !srv.streamEventVisible(httptest.NewRequest(http.MethodGet, "/api/stream2", nil), StreamResponse{}) {
		t.Fatal("ordinary shared stream event should remain visible")
	}
}
