package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	for kind, private := range map[string]StreamResponse{
		"read":   {ConversationRead: &generated.ConversationReadMarkersRow{ConversationID: "c", ReadAt: 7}, readUserID: "alice"},
		"unread": {ConversationUnread: &generated.ConversationUnreadMarksRow{ConversationID: "c", UnreadAt: 7}, readUserID: "alice"},
	} {
		for _, tc := range []struct {
			name string
			user string
			want bool
		}{
			{name: "owner", user: "alice", want: true},
			{name: "different user", user: "bob", want: false},
			{name: "anonymous", user: "", want: false},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, "/api/stream2", nil)
				if tc.user != "" {
					req.Header.Set("X-Test-User", tc.user)
				}
				if got := srv.streamEventVisible(req, private); got != tc.want {
					t.Fatalf("visible = %v, want %v", got, tc.want)
				}
			})
		}
	}
	if !srv.streamEventVisible(httptest.NewRequest(http.MethodGet, "/api/stream2", nil), StreamResponse{}) {
		t.Fatal("ordinary shared stream event should remain visible")
	}
}

// streamFrame waits for a frame of rec's stream satisfying ok.
func streamFrame(t *testing.T, rec *flusherRecorder, ok func(StreamResponse) bool) StreamResponse {
	t.Helper()
	timer := time.NewTimer(streamWaitTimeout)
	defer timer.Stop()
	for {
		for _, line := range strings.Split(rec.getString(), "\n") {
			var frame StreamResponse
			if data, found := strings.CutPrefix(line, "data: "); found && json.Unmarshal([]byte(data), &frame) == nil && ok(frame) {
				return frame
			}
		}
		select {
		case <-rec.flushed:
		case <-timer.C:
			t.Fatalf("no such frame; body=%s", rec.getString())
		}
	}
}

func TestConversationMarkUnread(t *testing.T) {
	srv, database, _ := newTestServer(t)
	srv.requireHeader = "X-Test-User"
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	ctx := t.Context()
	// The conversation has no reply to be unread about.
	conversation, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conversation.ConversationID
	request := func(user, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if user != "" {
			req.Header.Set("X-Test-User", user)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	stream := func(user string) *flusherRecorder {
		t.Helper()
		streamCtx, cancel := context.WithCancel(ctx)
		rec := newFlusherRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/stream2", nil).WithContext(streamCtx)
		req.Header.Set("X-Test-User", user)
		done := make(chan struct{})
		go func() {
			mux.ServeHTTP(rec, req)
			close(done)
		}()
		t.Cleanup(func() { cancel(); <-done })
		streamFrame(t, rec, func(f StreamResponse) bool { return f.ConversationReads != nil })
		return rec
	}
	list := func(user string) ConversationWithState {
		t.Helper()
		var rows []ConversationWithState
		if err := json.Unmarshal(request(user, http.MethodGet, "/api/conversations", "").Body.Bytes(), &rows); err != nil || len(rows) != 1 || rows[0].Unread == nil {
			t.Fatalf("%s list = %+v, %v", user, rows, err)
		}
		return rows[0]
	}
	snapshot := func(user string) db.ConversationReads {
		t.Helper()
		return *streamFrame(t, stream(user), func(f StreamResponse) bool { return f.ConversationReads != nil }).ConversationReads
	}

	phone, bob := stream("alice"), stream("bob")
	_, hashBefore, err := srv.conversationListStream.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	unreadPath := "/api/conversation/" + id + "/unread"
	if rec := request("", http.MethodPost, unreadPath, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous mark status = %d", rec.Code)
	}
	if rec := request("alice", http.MethodPost, "/api/conversation/missing/unread", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("missing conversation mark status = %d", rec.Code)
	}
	rec := request("alice", http.MethodPost, unreadPath, "")
	var mark generated.ConversationUnreadMarksRow
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &mark) != nil || mark.ConversationID != id || mark.UnreadAt <= 0 {
		t.Fatalf("mark = %d %s", rec.Code, rec.Body.String())
	}

	// Alice's other devices hear of it; Bob doesn't, and his list is unchanged.
	live := streamFrame(t, phone, func(f StreamResponse) bool { return f.ConversationUnread != nil })
	if *live.ConversationUnread != mark {
		t.Fatalf("live mark = %+v, want %+v", live.ConversationUnread, mark)
	}
	if row := list("alice"); !*row.Unread || row.UnreadAt != mark.UnreadAt || row.LastAttentionAt != 0 {
		t.Fatalf("alice's marked row = %+v", row)
	}
	if row := list("bob"); *row.Unread || row.UnreadAt != 0 {
		t.Fatalf("bob's row = %+v", row)
	}
	if reads := snapshot("alice"); len(reads.UnreadMarks) != 1 || reads.UnreadMarks[0] != mark {
		t.Fatalf("alice's snapshot = %+v", reads)
	}
	if reads := snapshot("bob"); len(reads.UnreadMarks) != 0 {
		t.Fatalf("bob's snapshot = %+v", reads)
	}
	// Bob's own mark follows Alice's on the stream; only his reaches him.
	if rec := request("bob", http.MethodPost, unreadPath, ""); rec.Code != http.StatusOK {
		t.Fatalf("bob's mark status = %d", rec.Code)
	}
	streamFrame(t, bob, func(f StreamResponse) bool { return f.ConversationUnread != nil })
	if n := strings.Count(bob.getString(), "conversation_unread"); n != 1 {
		t.Fatalf("bob received %d marks, want his own", n)
	}
	_, hashAfter, err := srv.conversationListStream.snapshot(ctx)
	if err != nil || hashAfter != hashBefore {
		t.Fatalf("private mark changed the shared list hash: %s -> %s, %v", hashBefore, hashAfter, err)
	}

	// Reading through the mark clears it.
	readPath := "/api/conversation/" + id + "/read"
	if rec := request("alice", http.MethodPost, readPath, fmt.Sprintf(`{"read_at":%d}`, mark.UnreadAt+1)); rec.Code != http.StatusBadRequest {
		t.Fatalf("read past the mark status = %d", rec.Code)
	}
	if rec := request("alice", http.MethodPost, readPath, fmt.Sprintf(`{"read_at":%d}`, mark.UnreadAt)); rec.Code != http.StatusOK {
		t.Fatalf("read of the mark status = %d: %s", rec.Code, rec.Body.String())
	}
	if row := list("alice"); *row.Unread || row.UnreadAt != 0 || *row.ReadAt != mark.UnreadAt {
		t.Fatalf("alice's read row = %+v", row)
	}
	if reads := snapshot("alice"); len(reads.UnreadMarks) != 0 {
		t.Fatalf("alice's snapshot after reading = %+v", reads)
	}
}
