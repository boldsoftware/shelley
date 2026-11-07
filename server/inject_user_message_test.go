package server

import (
	"context"
	"net/http/httptest"
	"slices"
	"strings"
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
