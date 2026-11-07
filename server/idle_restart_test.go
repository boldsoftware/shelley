package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"shelley.exe.dev/db"
)

func TestIdleRestart(t *testing.T) {
	tests := []struct {
		name     string
		pending  bool
		activity bool // the UI reported activity just now
		want     bool
	}{
		{name: "nothing installed", pending: false, want: false},
		{name: "installed, user active", pending: true, activity: true, want: false},
		{name: "installed, user idle", pending: true, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, database, _ := newTestServer(t)
			exited := make(chan int, 1)
			server.exitDelay = 0
			server.exitProcess = func(code int) { exited <- code }
			server.restartPendingNow = func() bool { return tt.pending }
			server.lastUserActivity.Store(time.Now().Add(-idleRestartAfter).UnixNano())
			if tt.activity {
				req := httptest.NewRequest(http.MethodPost, "/api/activity", nil)
				req.Header.Set("X-Shelley-Request", "1")
				w := httptest.NewRecorder()
				mux := http.NewServeMux()
				server.RegisterRoutes(mux)
				mux.ServeHTTP(w, req)
				if w.Code != http.StatusNoContent {
					t.Fatalf("POST /api/activity = %d: %s", w.Code, w.Body)
				}
			}

			if got := server.idleRestartTick(context.Background()); got != tt.want {
				t.Fatalf("idleRestartTick() = %v, want %v", got, tt.want)
			}
			resume, _ := database.GetSetting(context.Background(), db.ResumeAfterUpgradeSettingKey)
			if !tt.want {
				if resume != "" {
					t.Errorf("resume marker = %q without a restart", resume)
				}
				return
			}
			if resume != "1" {
				t.Errorf("resume marker = %q, want 1", resume)
			}
			select {
			case code := <-exited:
				if code != restartExitCode {
					t.Errorf("exit code = %d, want %d", code, restartExitCode)
				}
			case <-time.After(time.Second):
				t.Fatal("exit was not scheduled")
			}
		})
	}
}
