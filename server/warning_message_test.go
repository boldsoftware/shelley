package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
)

func TestRoutingWarningBypassesRetryCap(t *testing.T) {
	for _, retries := range []int{0, 2, 3, 5} {
		t.Run(fmt.Sprint(retries), func(t *testing.T) {
			h := NewTestHarness(t)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			conversation, err := h.db.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
			if err != nil {
				t.Fatal(err)
			}
			cm, err := h.server.getOrCreateConversationManager(ctx, conversation.ConversationID, "")
			if err != nil {
				t.Fatal(err)
			}
			for range retries {
				if err := cm.recordWarning(ctx, "retrying"); err != nil {
					t.Fatal(err)
				}
			}
			before, err := h.db.ListMessages(ctx, conversation.ConversationID)
			if err != nil {
				t.Fatal(err)
			}
			retryWarnings := 0
			for _, message := range before {
				if message.Type == string(db.MessageTypeWarning) {
					retryWarnings++
				}
			}
			if retryWarnings != min(retries, maxConsecutiveWarnings) {
				t.Fatalf("stored %d retry warnings after %d retries", retryWarnings, retries)
			}
			next := h.server.streamPub.Subscribe(ctx, -1)
			agent := loop.NewLoop(loop.Config{
				LLM:                  h.llm,
				History:              []llm.Message{llm.UserStringMessage("routing demo: opus")},
				RecordMessage:        cm.recordMessage,
				RecordWarning:        cm.recordWarning,
				RecordRoutingWarning: cm.recordRoutingWarning,
			})
			if err := agent.ProcessOneTurn(ctx); err != nil {
				t.Fatal(err)
			}
			messages, err := h.db.ListMessages(ctx, conversation.ConversationID)
			if err != nil {
				t.Fatal(err)
			}
			if len(messages) != len(before)+2 {
				t.Fatalf("stored %d messages: retry cap or routing notice lost", len(messages))
			}
			notice, reply := messages[len(messages)-2], messages[len(messages)-1]
			if notice.Type != string(db.MessageTypeWarning) || !notice.ExcludedFromContext || reply.Type != string(db.MessageTypeAgent) || notice.SequenceID >= reply.SequenceID {
				t.Fatalf("expected context-excluded notice before reply: notice = %+v, reply = %+v", notice, reply)
			}
			var data map[string]any
			if notice.UserData == nil {
				t.Fatal("missing notice user data")
			}
			if err := json.Unmarshal([]byte(*notice.UserData), &data); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(data["text"].(string), "Provider routed") || data["suppression_text"] != nil || data["suppressed"] != nil {
				t.Fatalf("routing notice inherited retry suppression: %v", data)
			}
			routing, ok := data["routing"].(map[string]any)
			if !ok || routing["from"] != "Claude Fable 5.1" || routing["to"] != "Claude Opus 4.8" ||
				routing["title"] != "Provider routed this response" || routing["recovered"] != false || routing["detail"] == "" {
				t.Fatalf("missing structured routing data: %v", data)
			}
			for {
				frame, ok := next()
				if !ok {
					t.Fatal("routing notice never reached unified stream")
				}
				for _, message := range frame.Messages {
					if message.MessageID == notice.MessageID {
						return
					}
				}
			}
		})
	}
}

func TestPredictableFailRecordsWarningMessage(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	h.NewConversation("fail nope", "")

	var messages []string
	waitFor(t, 5*time.Second, func() bool {
		msgs, err := h.db.ListMessages(t.Context(), h.ConversationID())
		if err != nil {
			t.Fatalf("ListMessages: %v", err)
		}
		messages = messages[:0]
		for _, msg := range msgs {
			if msg.Type != string(db.MessageTypeWarning) || msg.UserData == nil {
				continue
			}
			var userData struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal([]byte(*msg.UserData), &userData); err != nil {
				t.Fatalf("warning user_data: %v", err)
			}
			messages = append(messages, userData.Text)
		}
		return len(messages) == 1 && strings.Contains(messages[0], "nope")
	})
}

// TestWarningMessageReachesUnifiedStream asserts that a warning message is
// delivered on /api/stream2, the stream the web UI actually listens to.
//
// Warnings are recorded by loop.RunConfig.retryWarningHook on LLM retry / overload /
// rate-limit events — ordinary operation, not damage. They get a real
// sequence_id from GetNextSequenceID, so if they are published only to the
// per-conversation subpub (the legacy /api/conversation/<id>/stream endpoint)
// and not to the server-wide streamPub, the web client never sees them AND its
// view of the sequence space acquires a hole: seq N (warning) is missing while
// seq N+1 arrives. The client's message cache detects that skip and correctly
// forfeits its cached history, so on a flaky-LLM day this would systematically
// force full conversation re-downloads (see ui/src/services/messageStore.ts).
func TestWarningMessageReachesUnifiedStream(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	h.NewConversation("hello", "")

	// Subscribe to the server-wide stream the way handleStream does, before
	// triggering the warning.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	next := h.server.streamPub.Subscribe(ctx, -1)

	// "fail <text>" makes the predictable service fail and retry, which is
	// what produces a retry warning.
	h.Chat("fail nope")

	deadline := time.Now().Add(10 * time.Second)
	type frame struct {
		data StreamResponse
		ok   bool
	}
	frames := make(chan frame)
	go func() {
		for {
			d, ok := next()
			frames <- frame{d, ok}
			if !ok {
				return
			}
		}
	}()
	for {
		select {
		case f := <-frames:
			if !f.ok {
				t.Fatal("stream closed before the warning arrived")
			}
			for _, m := range f.data.Messages {
				if m.Type != string(db.MessageTypeWarning) {
					continue
				}
				if m.SequenceID <= 0 {
					t.Errorf("warning has sequence_id %d", m.SequenceID)
				}
				return // delivered
			}
		case <-time.After(time.Until(deadline)):
			t.Fatal("warning message never arrived on the unified stream; recordWarning must use publishStream so it reaches streamPub, not just the per-conversation subpub")
		}
	}
}
