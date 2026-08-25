package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shelley.exe.dev/db"
)

// newScopeTestSessions builds a TerminalSessions rooted in a temp dir with the
// in-process spawner, so spawned sessions die with the test.
func newScopeTestSessions(t *testing.T) *TerminalSessions {
	t.Helper()
	ts, err := NewTerminalSessions(t.TempDir(), slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})))
	if err != nil {
		t.Fatalf("NewTerminalSessions: %v", err)
	}
	ts.SetSpawner(InProcessSpawner)
	return ts
}

// spawnScoped starts a long-lived session owned by conversationID ("" for
// global) and returns its record. The command blocks so the session stays
// alive for the duration of the test.
func spawnScoped(t *testing.T, ts *TerminalSessions, conversationID string) *TerminalSession {
	t.Helper()
	sess, dc, err := ts.Spawn("sleep 60", t.TempDir(), conversationID, 80, 24, nil)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() {
		dc.Close()
		_ = ts.Kill(sess.ID)
	})
	return sess
}

func TestTerminalSpawnRecordsOwner(t *testing.T) {
	t.Parallel()
	ts := newScopeTestSessions(t)

	local := spawnScoped(t, ts, "conv-1")
	if local.ConversationID != "conv-1" {
		t.Errorf("local terminal owner = %q, want conv-1", local.ConversationID)
	}
	global := spawnScoped(t, ts, "")
	if global.ConversationID != "" {
		t.Errorf("global terminal owner = %q, want empty", global.ConversationID)
	}

	// The owner must be on disk, not just in memory, or it would not survive a
	// restart.
	var onDisk TerminalSession
	data, err := os.ReadFile(filepath.Join(ts.dir, local.ID+".json"))
	if err != nil {
		t.Fatalf("read session record: %v", err)
	}
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("unmarshal session record: %v", err)
	}
	if onDisk.ConversationID != "conv-1" {
		t.Errorf("persisted owner = %q, want conv-1", onDisk.ConversationID)
	}
}

// Records written before terminals had a scope have no conversation_id field.
// They must come back as global rather than becoming invisible everywhere.
func TestTerminalRestoresLegacyRecordAsGlobal(t *testing.T) {
	t.Parallel()
	ts := newScopeTestSessions(t)
	sess := spawnScoped(t, ts, "conv-1")

	// Rewrite the record without a conversation_id, the way an older shelley
	// would have written it, then rescan.
	raw := map[string]any{}
	data, err := os.ReadFile(filepath.Join(ts.dir, sess.ID+".json"))
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal record: %v", err)
	}
	delete(raw, "conversation_id")
	out, _ := json.Marshal(raw)
	if err := os.WriteFile(filepath.Join(ts.dir, sess.ID+".json"), out, 0o600); err != nil {
		t.Fatalf("write record: %v", err)
	}

	reloaded := newSessionsAt(t, ts.dir)
	got := reloaded.Get(sess.ID)
	if got == nil {
		t.Fatalf("session %s not restored", sess.ID)
	}
	if got.ConversationID != "" {
		t.Errorf("legacy record owner = %q, want empty (global)", got.ConversationID)
	}
	client, err := reloaded.Attach(got, 80, 24)
	if err != nil {
		t.Fatalf("attach legacy dtach session: %v", err)
	}
	client.Close()
}

// newSessionsAt reopens an existing sessions dir, exercising the scan path.
func newSessionsAt(t *testing.T, dir string) *TerminalSessions {
	t.Helper()
	ts, err := NewTerminalSessions(dir, slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})))
	if err != nil {
		t.Fatalf("reopen TerminalSessions: %v", err)
	}
	return ts
}

func TestTerminalScopeRoundTripPersists(t *testing.T) {
	t.Parallel()
	ts := newScopeTestSessions(t)
	sess := spawnScoped(t, ts, "conv-1")

	// local -> global
	if _, err := ts.SetConversationID(sess.ID, ""); err != nil {
		t.Fatalf("SetConversationID to global: %v", err)
	}
	if got := newSessionsAt(t, ts.dir).Get(sess.ID); got == nil || got.ConversationID != "" {
		t.Fatalf("after globalize, reloaded owner = %v, want empty", got)
	}

	// global -> local
	if _, err := ts.SetConversationID(sess.ID, "conv-2"); err != nil {
		t.Fatalf("SetConversationID to local: %v", err)
	}
	if got := newSessionsAt(t, ts.dir).Get(sess.ID); got == nil || got.ConversationID != "conv-2" {
		t.Fatalf("after localize, reloaded owner = %v, want conv-2", got)
	}
}

func TestTerminalSetConversationIDUnknownTerminal(t *testing.T) {
	t.Parallel()
	ts := newScopeTestSessions(t)
	if _, err := ts.SetConversationID("nope", "conv-1"); err == nil {
		t.Fatal("expected an error for an unknown terminal id")
	}
}

// The scope endpoint is the only way the browser changes ownership, so its
// validation is worth pinning: null means global, and nothing else may.
func TestHandleTerminalScopeHTTP(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	if server.terminals == nil {
		t.Skip("no terminal sessions configured")
	}
	server.terminals.SetSpawner(InProcessSpawner)

	conv, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	convID := conv.ConversationID

	sess := spawnScoped(t, server.terminals, convID)

	mux := http.NewServeMux()
	server.RegisterRoutes(mux)

	put := func(id, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PUT", "/api/terminals/"+id+"/scope", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	t.Run("null makes it global", func(t *testing.T) {
		w := put(sess.ID, `{"conversation_id": null}`)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
		}
		var dto terminalDTO
		if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if dto.ConversationID != nil {
			t.Errorf("response conversation_id = %v, want null", *dto.ConversationID)
		}
		if got := server.terminals.Get(sess.ID); got.ConversationID != "" {
			t.Errorf("stored owner = %q, want empty", got.ConversationID)
		}
	})

	t.Run("an id makes it local", func(t *testing.T) {
		w := put(sess.ID, `{"conversation_id": "`+convID+`"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
		}
		var dto terminalDTO
		if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if dto.ConversationID == nil || *dto.ConversationID != convID {
			t.Errorf("response conversation_id = %v, want %s", dto.ConversationID, convID)
		}
	})

	// A missing field and an empty string both decode to a nil pointer in Go,
	// which would silently read as "global". Both must be rejected so a
	// malformed client cannot publish a terminal to every conversation.
	rejects := []struct {
		name string
		body string
		want int
	}{
		{"malformed json", `{`, http.StatusBadRequest},
		{"absent field", `{}`, http.StatusBadRequest},
		{"empty string", `{"conversation_id": ""}`, http.StatusBadRequest},
		{"unknown conversation", `{"conversation_id": "no-such-conversation"}`, http.StatusNotFound},
	}
	for _, tc := range rejects {
		t.Run(tc.name, func(t *testing.T) {
			w := put(sess.ID, tc.body)
			if w.Code != tc.want {
				t.Errorf("status = %d, want %d (body %s)", w.Code, tc.want, w.Body.String())
			}
		})
	}

	t.Run("unknown terminal", func(t *testing.T) {
		w := put("no-such-terminal", `{"conversation_id": null}`)
		if w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 (body %s)", w.Code, w.Body.String())
		}
	})
}

// The list endpoint is what the browser hydrates from, so global must arrive as
// null and local as the id.
func TestHandleTerminalsListReportsScope(t *testing.T) {
	t.Parallel()
	server, _, _ := newTestServer(t)
	if server.terminals == nil {
		t.Skip("no terminal sessions configured")
	}
	server.terminals.SetSpawner(InProcessSpawner)

	local := spawnScoped(t, server.terminals, "conv-1")
	global := spawnScoped(t, server.terminals, "")

	req := httptest.NewRequest("GET", "/api/terminals", nil)
	w := httptest.NewRecorder()
	server.handleTerminalsList(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var got []terminalDTO
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	byID := map[string]terminalDTO{}
	for _, d := range got {
		byID[d.ID] = d
	}
	if d, ok := byID[local.ID]; !ok {
		t.Errorf("local terminal missing from list")
	} else if d.ConversationID == nil || *d.ConversationID != "conv-1" {
		t.Errorf("local conversation_id = %v, want conv-1", d.ConversationID)
	}
	if d, ok := byID[global.ID]; !ok {
		t.Errorf("global terminal missing from list")
	} else if d.ConversationID != nil {
		t.Errorf("global conversation_id = %v, want null", *d.ConversationID)
	}
}

// Deleting a conversation must not leave its terminals pointing at something
// that no longer exists; they become global instead.
func TestDeleteConversationGlobalizesItsTerminals(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	if server.terminals == nil {
		t.Skip("no terminal sessions configured")
	}
	server.terminals.SetSpawner(InProcessSpawner)

	conv, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	mine := spawnScoped(t, server.terminals, conv.ConversationID)
	someoneElse := spawnScoped(t, server.terminals, "other-conv")

	req := httptest.NewRequest("POST", "/conversation/"+conv.ConversationID+"/delete", nil)
	w := httptest.NewRecorder()
	server.handleDeleteConversation(w, req, conv.ConversationID)
	if w.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", w.Code, w.Body.String())
	}

	if got := server.terminals.Get(mine.ID); got == nil {
		t.Fatal("terminal was removed instead of made global")
	} else if got.ConversationID != "" {
		t.Errorf("owner = %q, want empty (global)", got.ConversationID)
	}
	// Other conversations' terminals are untouched.
	if got := server.terminals.Get(someoneElse.ID); got == nil || got.ConversationID != "other-conv" {
		t.Errorf("unrelated terminal was re-scoped: %v", got)
	}
}

// Reattaching by term_id must never rerun the command. A user who sees a
// terminal finish and comes back to it later must not silently kick off the
// same work again.
func TestAttachToDeadSessionDoesNotRespawn(t *testing.T) {
	t.Parallel()
	server, _, _ := newTestServer(t)
	if server.terminals == nil {
		t.Skip("no terminal sessions configured")
	}
	server.terminals.SetSpawner(InProcessSpawner)

	sess := spawnScoped(t, server.terminals, "conv-1")
	// Kill it the way an exiting command would, leaving the client holding a
	// stale id.
	if err := server.terminals.Kill(sess.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	// A term_id with a command attached is the shape the browser used to send.
	// Even then, the command must not run.
	before := len(server.terminals.List())
	_, _, err := server.attachOrSpawn(sess.ID, "sleep 60", t.TempDir(), "conv-1", 80, 24, nil)
	if err == nil {
		t.Fatal("expected an error attaching to a dead session")
	}
	if got := len(server.terminals.List()); got != before {
		t.Errorf("session count changed from %d to %d: a session was respawned", before, got)
	}
}
