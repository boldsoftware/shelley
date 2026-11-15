package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shelley.exe.dev/db"
)

func TestHookHealthEpisodesDismissRecoveryAndSafePersistence(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	h := s.hookHealth
	h.complete(t.Context(), "first-chat", errors.New("stderr at /secret/path?token=secret"))
	first := h.status
	h.complete(t.Context(), "second-chat", errors.New("another private failure"))
	if !h.status.Active || h.status.EpisodeID != first.EpisodeID || h.status.Failures != 2 || h.status.ConversationID != "second-chat" {
		t.Fatalf("repeated failures: %+v", h.status)
	}
	dismissed, err := h.dismiss(t.Context(), h.status.EpisodeID)
	if err != nil || !dismissed.Dismissed {
		t.Fatalf("dismiss: %+v, %v", dismissed, err)
	}
	h.complete(t.Context(), "third-chat", errors.New("still failing"))
	if !h.status.Dismissed || h.status.Failures != 3 {
		t.Fatalf("failure should preserve dismissal: %+v", h.status)
	}
	h.complete(t.Context(), "recovered-chat", nil)
	if h.status.Active || h.status.Dismissed || h.status.Failures != 0 || h.status.ConversationID != "" {
		t.Fatalf("recovery: %+v", h.status)
	}
	h.complete(t.Context(), "new-episode", errors.New("new failure"))
	if !h.status.Active || h.status.Dismissed || h.status.EpisodeID <= first.EpisodeID || h.status.Failures != 1 {
		t.Fatalf("rearmed episode: %+v", h.status)
	}
	saved, err := database.GetSetting(t.Context(), hookHealthSettingKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"stderr", "/secret/path", "token=secret", "private failure"} {
		if strings.Contains(saved, forbidden) {
			t.Fatalf("unsafe setting contains %q: %s", forbidden, saved)
		}
	}
	var status HookHealthStatus
	if err := json.Unmarshal([]byte(saved), &status); err != nil || status != h.status {
		t.Fatalf("persisted status: %+v, err=%v; want %+v", status, err, h.status)
	}
}

func TestHookHealthRestartAndMissingHookClear(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	s.hooksDir = t.TempDir()
	hookPath := filepath.Join(s.hooksDir, hookEndOfTurn)
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.hookHealth.complete(t.Context(), "chat", errors.New("private"))
	dismissed, err := s.hookHealth.dismiss(t.Context(), s.hookHealth.status.EpisodeID)
	if err != nil {
		t.Fatal(err)
	}
	path := database.Path()
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := db.New(db.Config{DSN: path})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := NewServer(reopened, s.llmManager, s.toolSetConfig, s.logger, true, "predictable", "")
	restarted.hooksDir = s.hooksDir
	if err := restarted.initHookHealth(t.Context()); err != nil {
		t.Fatal(err)
	}
	if restarted.hookHealth.status != dismissed {
		t.Fatalf("restart lost status: %+v, want %+v", restarted.hookHealth.status, dismissed)
	}
	if err := os.Remove(hookPath); err != nil {
		t.Fatal(err)
	}
	if err := restarted.initHookHealth(t.Context()); err != nil {
		t.Fatal(err)
	}
	if restarted.hookHealth.status.Active || restarted.hookHealth.status.Dismissed {
		t.Fatalf("removed hook left warning: %+v", restarted.hookHealth.status)
	}
}

func TestHookHealthStartupCleanupPersistenceFailure(t *testing.T) {
	for _, hook := range []string{"missing", "non-executable"} {
		t.Run(hook, func(t *testing.T) {
			t.Parallel()
			s, database, _ := newTestServer(t)
			if hook == "non-executable" {
				if err := os.WriteFile(filepath.Join(s.hooksDir, hookEndOfTurn), []byte("#!/bin/sh\nexit 1\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			s.hookHealth.complete(t.Context(), "chat", errors.New("private"))
			before, err := s.hookHealth.dismiss(t.Context(), s.hookHealth.status.EpisodeID)
			if err != nil {
				t.Fatal(err)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			readonly, err := db.New(db.Config{DSN: "file:" + database.Path() + "?mode=ro"})
			if err != nil {
				t.Fatal(err)
			}
			defer readonly.Close()
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			restarted := NewServer(readonly, s.llmManager, s.toolSetConfig, logger, true, "predictable", "")
			restarted.hooksDir = s.hooksDir
			if err := restarted.initHookHealth(t.Context()); err != nil {
				t.Fatalf("advisory cleanup blocked startup: %v", err)
			}
			want := HookHealthStatus{EpisodeID: before.EpisodeID, Revision: before.Revision + 1}
			if got := restarted.hookHealth.status; got != want {
				t.Fatalf("startup cleanup: %+v, want %+v", got, want)
			}
			saved, err := readonly.GetSetting(t.Context(), hookHealthSettingKey)
			if err != nil {
				t.Fatal(err)
			}
			var persisted HookHealthStatus
			if err := json.Unmarshal([]byte(saved), &persisted); err != nil || persisted != before {
				t.Fatalf("read-only persisted status: %+v, err=%v; want %+v", persisted, err, before)
			}
			if !strings.Contains(logs.String(), "Failed to persist end-of-turn hook health") {
				t.Fatalf("cleanup write failure was not logged: %s", logs.String())
			}
		})
	}
}

func TestHookHealthStreamSnapshotAndLiveDelivery(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestServer(t)
	next, _, _, snapshot := s.subscribeStream(t.Context())
	if snapshot != (HookHealthStatus{}) {
		t.Fatalf("initial snapshot: %+v", snapshot)
	}
	s.hookHealth.complete(context.Background(), "chat", errors.New("private failure"))
	event, ok := next()
	if !ok || event.HookHealth == nil || event.HookHealth.ConversationID != "chat" {
		t.Fatalf("live event: %+v, ok=%v", event, ok)
	}
}

func TestHookHealthFailedPersistenceStillPublishes(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	next, _, _, _ := s.subscribeStream(t.Context())
	s.hookHealth.complete(context.Background(), "chat", errors.New("private"))
	event, ok := next()
	if !ok || event.HookHealth == nil || !event.HookHealth.Active {
		t.Fatalf("failed persistence hid warning: %+v, ok=%v", event, ok)
	}
}

func TestSendTurnEndUpdatesHookHealthFromLocalHook(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	s.hooksDir = t.TempDir()
	hookPath := filepath.Join(s.hooksDir, hookEndOfTurn)
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\necho private-stderr >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := database.SetSetting(t.Context(), exeNotifySettingKey, "false"); err != nil {
		t.Fatal(err)
	}
	conv, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	next, _, _, _ := s.subscribeStream(t.Context())
	s.sendTurnEnd(t.Context(), conv, "predictable")
	event, ok := next()
	if !ok || event.HookHealth == nil || !event.HookHealth.Active || event.HookHealth.ConversationID != conv.ConversationID {
		t.Fatalf("hook execution health event: %+v, ok=%v", event, ok)
	}
	saved, err := database.GetSetting(t.Context(), hookHealthSettingKey)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(saved, "private-stderr") {
		t.Fatalf("stderr leaked into setting: %s", saved)
	}
}

func TestHookHealthDismissRouteValidationAndPersistenceFailure(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	s.hookHealth.complete(t.Context(), "chat", errors.New("private"))
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	for _, body := range []string{`{`, `{}`, `{"episode_id":0}`, `{"episode_id":-1}`, `{"episode_id":"1"}`} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/hooks/end-of-turn/dismiss", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid body %s: HTTP %d", body, w.Code)
		}
	}
	episodeID := s.hookHealth.status.EpisodeID
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/end-of-turn/dismiss", strings.NewReader(
		fmt.Sprintf(`{"episode_id":%d}`, episodeID),
	))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("persistence failure: HTTP %d, body=%s", w.Code, w.Body.String())
	}
	if s.hookHealth.status.Dismissed {
		t.Fatalf("failed persistence changed dismissal: %+v", s.hookHealth.status)
	}
}
