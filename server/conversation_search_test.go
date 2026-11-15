package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
)

func TestConversationSearchErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cancel     bool
		deadline   bool
		closeDB    bool
		wantStatus int
	}{
		{name: "canceled", cancel: true},
		{name: "deadline", deadline: true, wantStatus: http.StatusInternalServerError},
		{name: "database failure", closeDB: true, wantStatus: http.StatusInternalServerError},
		{name: "database failure with canceled request", cancel: true, closeDB: true, wantStatus: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, database, _ := newTestServer(t)
			var logs bytes.Buffer
			srv.logger = slog.New(slog.NewTextHandler(&logs, nil))
			if tc.closeDB {
				if err := database.Close(); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			if tc.deadline {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithDeadline(ctx, time.Unix(0, 0))
				defer deadlineCancel()
			}
			req := httptest.NewRequest(http.MethodGet, "/api/conversations/search?q=pelican", nil).WithContext(ctx)
			recorder := &httptest.ResponseRecorder{Body: new(bytes.Buffer)}
			srv.handleSearchConversations(recorder, req)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("HTTP %d: %s, want %d", recorder.Code, recorder.Body.String(), tc.wantStatus)
			}
			if tc.wantStatus == 0 {
				if recorder.Body.Len() != 0 || logs.Len() != 0 {
					t.Fatalf("canceled search wrote response %q or logs %q", recorder.Body.String(), logs.String())
				}
			} else if !strings.Contains(logs.String(), "Failed to search conversations") {
				t.Fatalf("search failure was not logged: %s", logs.String())
			}
		})
	}
}

func TestConversationSearchArchiveScope(t *testing.T) {
	t.Parallel()
	srv, database, _ := newTestServer(t)
	ids := make(map[bool]string)
	for _, archived := range []bool{false, true} {
		slug := "pelican-active"
		if archived {
			slug = "pelican-archived"
		}
		conv, err := database.CreateConversation(t.Context(), &slug, true, nil, nil, db.ConversationOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ids[archived] = conv.ConversationID
		if archived {
			if _, err := database.ArchiveConversation(t.Context(), conv.ConversationID); err != nil {
				t.Fatal(err)
			}
		}
	}
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	for _, tc := range []struct {
		query    string
		archived bool
	}{
		{"q=pelican&limit=1", false},
		{"q=pelican&archived=false&limit=1", false},
		{"q=pelican&archived=true&limit=1", true},
	} {
		t.Run(tc.query, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest("GET", "/api/conversations/search?"+tc.query, nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("HTTP %d: %s", recorder.Code, recorder.Body.String())
			}
			var results []ConversationWithState
			if err := json.Unmarshal(recorder.Body.Bytes(), &results); err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].ConversationID != ids[tc.archived] || results[0].Archived != tc.archived {
				t.Fatalf("unexpected archive scope: %#v", results)
			}
		})
	}
}
