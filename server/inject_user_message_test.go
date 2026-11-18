package server

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

// A message sent for immediate delivery is delivered to the running turn
// even while other work (here, a subagent-style injection) is queued.
func TestUserMessageDeliveredDespiteQueuedWork(t *testing.T) {
	t.Parallel()
	gate := newGateLLMService("bash: true")
	server, database, h := channelTestServer(t, gate)
	conv, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	chat := func(text string) {
		t.Helper()
		w := postJSON(t, h, "/api/conversation/"+conv.ConversationID+"/chat", ChatRequest{Message: text, Model: "predictable"})
		if w.Code != 202 || !strings.Contains(w.Body.String(), `"accepted"`) {
			t.Fatalf("chat %q: %d %s", text, w.Code, w.Body.String())
		}
	}
	chat("bash: true")
	gate.waitStarted(t, "bash: true")
	manager, err := server.getOrCreateConversationManager(t.Context(), conv.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.InjectMessage(t.Context(), "predictable", llm.UserStringMessage("report")); err != nil {
		t.Fatal(err)
	}
	chat("second")
	chat("third")
	queued, err := database.GetQueuedMessages(t.Context(), conv.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	// The running turn takes them all, in order, at its next round.
	if len(queued) != 3 || !queued[0].Inject || !queued[1].Inject || !queued[2].Inject {
		t.Fatalf("want the injection and both messages queued for the turn: %+v", queued)
	}
	gate.release("bash: true")
	waitFor(t, 5*time.Second, func() bool {
		c, err := database.GetConversationByID(t.Context(), conv.ConversationID)
		return err == nil && !c.AgentWorking && c.QueuedMessages == "[]"
	})
	// All three reach the turn before it first ends.
	want := map[string]bool{"report": false, "second": false, "third": false}
	for _, m := range listMessages(t, database, conv.ConversationID) {
		msg := storedLLMMessage(t, &m)
		if m.Type == string(db.MessageTypeAgent) && msg.EndOfTurn {
			break
		}
		for text := range want {
			if m.Type == string(db.MessageTypeUser) && strings.Contains(messageText(msg), text) {
				want[text] = true
			}
		}
	}
	for text, seen := range want {
		if !seen {
			t.Errorf("%q not delivered before the turn ended", text)
		}
	}
}

// A message sent after a voice note that is still transcribing follows the
// note into the running turn once it is ready, rather than waiting for the
// turn to end.
func TestUserMessageFollowsTranscriptionIntoRunningTurn(t *testing.T) {
	t.Parallel()
	gate := newGateLLMService("bash: true")
	server, database, h := channelTestServer(t, gate)
	server.transcriber = successfulRecordingTranscriber("spoken words")
	mediaPath := transcriptionTestFile(t, "voice.webm")
	probed, release := make(chan struct{}), make(chan struct{})
	server.mediaRun = func(context.Context, string, ...string) ([]byte, error) {
		close(probed)
		<-release
		return []byte(`{"streams":[{"codec_type":"audio"}],"format":{"duration":"2"}}`), nil
	}
	conv, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	chat := func(text, status string) *httptest.ResponseRecorder {
		t.Helper()
		w := postJSON(t, h, "/api/conversation/"+conv.ConversationID+"/chat", ChatRequest{Message: text, Model: "predictable"})
		if w.Code != 202 || !strings.Contains(w.Body.String(), `"`+status+`"`) {
			t.Fatalf("chat %q: %d %s", text, w.Code, w.Body.String())
		}
		return w
	}
	chat("bash: true", "accepted")
	gate.waitStarted(t, "bash: true")
	receipt := queuedTranscriptionReceipt(t, chat("/transcription "+mediaPath, "queued"), database, conv.ConversationID)
	done := transcriptionDone(t, server, receipt.ID)
	<-probed
	chat("follow-up", "queued")
	queued := queuedMessages(t, database, conv.ConversationID)
	if len(queued) != 2 || !queued[1].Inject {
		t.Fatalf("follow-up must wait behind the transcription for mid-turn delivery: %+v", queued)
	}
	close(release)
	<-done
	gate.release("bash: true")
	waitFor(t, 5*time.Second, func() bool {
		c, err := database.GetConversationByID(t.Context(), conv.ConversationID)
		return err == nil && !c.AgentWorking && c.QueuedMessages == "[]"
	})
	var got []string
	for _, m := range listMessages(t, database, conv.ConversationID) {
		msg := storedLLMMessage(t, &m)
		if m.Type == string(db.MessageTypeAgent) && msg.EndOfTurn {
			break
		}
		if m.Type != string(db.MessageTypeUser) {
			continue
		}
		for _, text := range []string{"spoken words", "follow-up"} {
			if strings.Contains(messageText(msg), text) {
				got = append(got, text)
			}
		}
	}
	if want := []string{"spoken words", "follow-up"}; !slices.Equal(got, want) {
		t.Fatalf("delivered before the turn ended = %q, want %q", got, want)
	}
}

// A user message, typed or spoken, that arrives while a bash command runs
// moves the command to the background within seconds, rather than after the
// usual minute, so the running turn's next request carries the message.
func TestWaitingUserMessageBackgroundsBash(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir()) // keep the job log out of /tmp
	for _, tc := range []struct {
		name string
		// send sends the message, returning text the model then sees.
		send func(t *testing.T, server *Server, id string) string
	}{
		{"typed", func(t *testing.T, server *Server, id string) string {
			postChatMessage(t, server, id, "echo: noted")
			return "echo: noted"
		}},
		{"voice", func(t *testing.T, server *Server, id string) string {
			server.transcriber = successfulRecordingTranscriber("spoken words")
			server.mediaRun = func(context.Context, string, ...string) ([]byte, error) {
				return []byte(`{"streams":[{"codec_type":"audio"}],"format":{"duration":"2"}}`), nil
			}
			postChatMessage(t, server, id, "/transcription "+transcriptionTestFile(t, "voice.webm"))
			return "spoken words"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, database, llmSvc := newTestServer(t)
			defer stopActiveConversationLoops(server)
			server.toolSetConfig.BashUserWaitingBackgroundAfter = 50 * time.Millisecond

			conv, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
			if err != nil {
				t.Fatal(err)
			}
			id := conv.ConversationID
			dir := t.TempDir()
			started, gate := filepath.Join(dir, "started"), filepath.Join(dir, "gate")
			if err := syscall.Mkfifo(gate, 0o600); err != nil {
				t.Fatal(err)
			}

			postChatMessage(t, server, id, "bash: touch "+started+"; read -r _ < "+gate)
			waitFor(t, 10*time.Second, func() bool { _, err := os.Stat(started); return err == nil })
			text := tc.send(t, server, id)
			waitForMessageContaining(t, database, id, "with a user message waiting; moved to background job", 10*time.Second)
			// The turn's next request carries the message.
			waitFor(t, 10*time.Second, func() bool {
				for _, req := range llmSvc.GetRecentRequests() {
					if strings.Contains(messageText(req.Messages[len(req.Messages)-1]), text) {
						return true
					}
				}
				return false
			})

			if err := os.WriteFile(gate, []byte("go\n"), 0); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 10*time.Second, func() bool { return len(backgroundJobNotices(t, database, id)) == 1 })
			waitForIdle(t, server, id)
		})
	}
}

// Whether a user message waits for the running turn's next request follows
// the queue: a message held behind an unfinished voice note waits once the
// note is cancelled, until the turn takes it.
func TestUserWaitingFollowsQueue(t *testing.T) {
	t.Parallel()
	gate := newGateLLMService("bash: true")
	server, database, h := channelTestServer(t, gate)
	server.transcriber = successfulRecordingTranscriber("spoken words")
	probed, release := make(chan struct{}), make(chan struct{})
	server.mediaRun = func(context.Context, string, ...string) ([]byte, error) {
		close(probed)
		<-release
		return []byte(`{"streams":[{"codec_type":"audio"}],"format":{"duration":"2"}}`), nil
	}
	conv, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conv.ConversationID
	chat := func(text, status string) *httptest.ResponseRecorder {
		t.Helper()
		w := postJSON(t, h, "/api/conversation/"+id+"/chat", ChatRequest{Message: text, Model: "predictable"})
		if w.Code != 202 || !strings.Contains(w.Body.String(), `"`+status+`"`) {
			t.Fatalf("chat %q: %d %s", text, w.Code, w.Body.String())
		}
		return w
	}
	chat("bash: true", "accepted")
	gate.waitStarted(t, "bash: true")
	manager, err := server.getOrCreateConversationManager(t.Context(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	waits := func(want bool, why string) {
		t.Helper()
		if got, _ := manager.userMessageWaiting(); got != want {
			t.Fatalf("user waiting = %v %s", got, why)
		}
	}

	notice := contextWithTurnUserData(t.Context(), backgroundJobUserData{BackgroundJobID: "job", Text: "job done"})
	if err := manager.InjectMessage(notice, "predictable", llm.UserStringMessage("job done")); err != nil {
		t.Fatal(err)
	}
	waits(false, "for a job notice")
	voice := queuedTranscriptionReceipt(t, chat("/transcription "+transcriptionTestFile(t, "voice.webm"), "queued"), database, id)
	done := transcriptionDone(t, server, voice.ID)
	<-probed
	waits(false, "for an unfinished voice note")
	chat("follow-up", "queued")
	waits(false, "for a message behind an unfinished voice note")
	if w := postJSON(t, h, "/api/conversation/"+id+"/cancel-queued?queued_id="+voice.ID, nil); w.Code != 200 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	waits(true, "once the voice note ahead is cancelled")
	close(release)
	<-done
	gate.release("bash: true")
	waitFor(t, 5*time.Second, func() bool {
		c, err := database.GetConversationByID(t.Context(), id)
		return err == nil && !c.AgentWorking && c.QueuedMessages == "[]"
	})
	waits(false, "once the turn took it")
}
