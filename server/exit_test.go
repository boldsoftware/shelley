package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
)

func TestHandleExitResume(t *testing.T) {
	tests := []struct {
		name         string
		url          string
		wantSetting  string
		wantMessage  string
		wantExitCode int
	}{
		{
			name:        "ordinary exit",
			url:         "/exit",
			wantMessage: "Exiting...",
		},
		{
			name:         "resume after restart",
			url:          "/exit?resume=true",
			wantSetting:  "1",
			wantMessage:  "Exiting; active conversations will resume after restart...",
			wantExitCode: restartExitCode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, database, _ := newTestServer(t)
			exited := make(chan int, 1)
			server.exitDelay = 0
			server.exitProcess = func(code int) { exited <- code }

			req := httptest.NewRequest(http.MethodPost, tt.url, nil)
			w := httptest.NewRecorder()
			server.handleExit(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tt.wantMessage) {
				t.Errorf("body = %q, want message %q", w.Body.String(), tt.wantMessage)
			}
			select {
			case code := <-exited:
				if code != tt.wantExitCode {
					t.Errorf("exit code = %d, want %d", code, tt.wantExitCode)
				}
			case <-time.After(time.Second):
				t.Fatal("exit was not scheduled")
			}

			got, err := database.GetSetting(context.Background(), db.ResumeAfterUpgradeSettingKey)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.wantSetting {
				t.Errorf("resume setting = %q, want %q", got, tt.wantSetting)
			}
		})
	}
}
