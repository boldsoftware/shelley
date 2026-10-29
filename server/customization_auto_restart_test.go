package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"shelley.exe.dev/db"
)

func (f *customizationFixture) idle(d time.Duration) {
	f.s.customizations.activity.Store(time.Now().Add(-d).UnixNano())
}

func (f *customizationFixture) tick(t *testing.T) bool {
	t.Helper()
	scheduled, _ := f.s.autoRestartTick(t.Context())
	return scheduled
}

func (f *customizationFixture) noExit(t *testing.T) {
	t.Helper()
	select {
	case code := <-f.exited:
		t.Fatalf("unexpected exit %d", code)
	default:
	}
}

func (f *customizationFixture) autoRestartEvent(t *testing.T, sha string) string {
	t.Helper()
	event, err := f.database.LastCustomizationAutoRestart(t.Context(), sha)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func (f *customizationFixture) listed(t *testing.T) db.Customization {
	t.Helper()
	for _, state := range workspaceResponse[[]db.Customization](t, workspaceAPI(f.s, "GET", "/api/customizations", ""), http.StatusOK) {
		if state.ConversationID == f.id {
			return state
		}
	}
	t.Fatal("conversation not listed")
	return db.Customization{}
}

func TestAutoRestartWaitsForIdleUserAndNeverRepeats(t *testing.T) {
	t.Parallel()
	f := newCustomizationFixture(t)
	state := f.verify(t)
	if state.Status != "installed" {
		t.Fatalf("state=%+v", state)
	}
	f.idle(10 * time.Second)
	if f.tick(t) {
		t.Fatal("restarted while the user was active")
	}
	f.noExit(t)
	if listed := f.listed(t); listed.AutoRestart != "pending" || listed.AutoRestartInMS == nil ||
		*listed.AutoRestartInMS <= 0 || *listed.AutoRestartInMS > 20_000 {
		t.Fatalf("pending restart not reported: %+v", listed)
	}
	// The heartbeat endpoint counts as activity.
	f.idle(time.Minute)
	api := http.NewServeMux()
	f.s.registerWorkspaceRoutes(api)
	heartbeat := httptest.NewRecorder()
	f.s.trackUserActivity(api).ServeHTTP(heartbeat, httptest.NewRequest("POST", "/api/activity", nil))
	if heartbeat.Code != http.StatusNoContent {
		t.Fatalf("heartbeat: %d", heartbeat.Code)
	}
	if f.tick(t) {
		t.Fatal("restarted right after a heartbeat")
	}

	f.idle(31 * time.Second)
	if !f.tick(t) {
		t.Fatal("idle restart not scheduled")
	}
	if code := <-f.exited; code != restartExitCode {
		t.Fatalf("exit code %d", code)
	}
	if current := f.state(t); current.Status != "restarting" {
		t.Fatalf("state=%+v", current)
	}
	if marker, err := f.database.GetSetting(t.Context(), db.ResumeAfterUpgradeSettingKey); err != nil || marker != "1" {
		t.Fatalf("conversations not marked for continuation: %q %v", marker, err)
	}
	if event := f.autoRestartEvent(t, state.ArtifactSHA256); event != "attempted" {
		t.Fatalf("event=%q", event)
	}
	// Say the process came back on the old binary: never restart into that
	// build again.
	f.s.releaseRestart()
	if _, err := f.database.UpdateCustomization(t.Context(), f.id, state.Generation, func(current *db.Customization) {
		current.Status = "installed"
	}); err != nil {
		t.Fatal(err)
	}
	if f.tick(t) {
		t.Fatal("restarted into the same build twice")
	}
	f.noExit(t)
	if listed := f.listed(t); listed.AutoRestart != "" {
		t.Fatalf("attempted build still planned: %+v", listed)
	}
}

func TestAutoRestartWaitsForWork(t *testing.T) {
	t.Parallel()
	f := newCustomizationFixture(t)
	f.verify(t)
	f.idle(time.Minute)
	busy := func(reason string) {
		t.Helper()
		if f.tick(t) {
			t.Fatalf("restarted while %s", reason)
		}
		f.noExit(t)
		plan := f.s.customizations.autoRestart.Load()
		if plan == nil || plan.State != "busy" || plan.Reason != reason {
			t.Fatalf("plan=%+v want busy: %s", plan, reason)
		}
		if listed := f.listed(t); listed.AutoRestart != "busy" || listed.AutoRestartInMS != nil {
			t.Fatalf("busy not reported: %+v", listed)
		}
	}

	manager := &ConversationManager{agentWorking: true}
	f.s.mu.Lock()
	f.s.activeConversations["working"] = manager
	f.s.mu.Unlock()
	busy("a conversation is working")
	f.s.mu.Lock()
	delete(f.s.activeConversations, "working")
	f.s.mu.Unlock()

	f.s.workspace.mu.Lock()
	f.s.workspace.initializing = map[string]bool{"starting": true}
	f.s.workspace.mu.Unlock()
	busy("a task is starting")
	f.s.workspace.mu.Lock()
	delete(f.s.workspace.initializing, "starting")
	f.s.workspace.mu.Unlock()

	run, _, _, err := f.database.BeginAction(t.Context(), f.actionConversation(t), "run")
	if err != nil {
		t.Fatal(err)
	}
	busy("a task action is running")
	if err := f.database.InterruptActionRun(t.Context(), run.ID, "test finished"); err != nil {
		t.Fatal(err)
	}

	other := f.conversation(t)
	verifying, err := f.database.BeginCustomization(t.Context(), other, f.state(t).Commit, true)
	if err != nil {
		t.Fatal(err)
	}
	busy("a Shelley build is verifying")
	if _, err := f.database.UpdateCustomization(t.Context(), other, verifying.Generation, func(current *db.Customization) {
		current.Status = "stale"
	}); err != nil {
		t.Fatal(err)
	}

	if !f.tick(t) {
		t.Fatal("idle restart not scheduled once work finished")
	}
	if code := <-f.exited; code != restartExitCode {
		t.Fatalf("exit code %d", code)
	}
}

func TestAutoRestartFailureIsVisibleAndNotRetried(t *testing.T) {
	t.Parallel()
	f := newCustomizationFixture(t)
	state := f.verify(t)
	f.idle(time.Minute)
	f.s.customizations.deps.targetExecutable = func() (string, error) { return "", errors.New("no target") }
	if f.tick(t) {
		t.Fatal("failed restart reported as scheduled")
	}
	f.noExit(t)
	current := f.state(t)
	if current.Status != "installed" || current.Error != "Restart failed: no target" {
		t.Fatalf("failure not surfaced: %+v", current)
	}
	if event := f.autoRestartEvent(t, state.ArtifactSHA256); event != "failed" {
		t.Fatalf("event=%q", event)
	}
	if marker, err := f.database.GetSetting(t.Context(), db.ResumeAfterUpgradeSettingKey); err != nil || marker != "" {
		t.Fatalf("failed restart left resume marker=%q err=%v", marker, err)
	}
	// The fence is released, and the same build is never tried again.
	if err := f.s.reserveRestart(); err != nil {
		t.Fatal(err)
	}
	f.s.releaseRestart()
	f.s.customizations.deps.targetExecutable = func() (string, error) { return f.live, nil }
	if f.tick(t) {
		t.Fatal("failed build retried")
	}
	f.noExit(t)
}

func TestAutoRestartCancel(t *testing.T) {
	t.Parallel()
	f := newCustomizationFixture(t)
	state := f.verify(t)
	f.idle(time.Second)
	if f.tick(t) || f.listed(t).AutoRestart != "pending" {
		t.Fatal("no restart planned")
	}
	if response := workspaceAPI(f.s, "POST", "/api/conversation/"+f.id+"/customization/auto-restart/cancel", `{"artifact_sha256":"other"}`); response.Code != http.StatusConflict {
		t.Fatalf("cancelled another build: %d %s", response.Code, response.Body.String())
	}
	cancelled := workspaceResponse[db.Customization](t, workspaceAPI(f.s, "POST", "/api/conversation/"+f.id+"/customization/auto-restart/cancel",
		`{"artifact_sha256":"`+state.ArtifactSHA256+`"}`), http.StatusOK)
	if cancelled.AutoRestart != "" || f.listed(t).AutoRestart != "" {
		t.Fatalf("cancel not reported: %+v", cancelled)
	}
	f.idle(time.Minute)
	if f.tick(t) {
		t.Fatal("restarted after cancel")
	}
	f.noExit(t)
	// The manual restart still works.
	response := workspaceAPI(f.s, "POST", "/api/conversation/"+f.id+"/customization/restart", restartCustomizationBody(t, state))
	workspaceResponse[db.Customization](t, response, http.StatusAccepted)
	<-f.exited
}

func TestAutoRestartNeedsSystemd(t *testing.T) {
	t.Parallel()
	f := newCustomizationFixture(t)
	f.verify(t)
	f.idle(time.Minute)
	f.s.customizations.deps.invocationID = func() string { return "" }
	if f.tick(t) || f.listed(t).AutoRestart != "" {
		t.Fatal("auto-restart planned without systemd")
	}
}

func TestUserActivityTracking(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestServer(t)
	handler := s.trackUserActivity(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for _, method := range []string{"GET", "HEAD", "POST"} {
		s.customizations.activity.Store(0)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, "/api/anything", nil))
		if active := s.customizations.activity.Load() != 0; active != (method == "POST") {
			t.Fatalf("%s counted as activity: %v", method, active)
		}
	}
	s.customizations.activity.Store(0)
	if err := s.receiveChannelEvent(context.Background(), "", channelEvent{ChatID: "chat", Type: "message", Text: ""}); err != nil {
		t.Fatal(err)
	}
	if s.customizations.activity.Load() != 0 {
		t.Fatal("empty channel event counted as activity")
	}
}
