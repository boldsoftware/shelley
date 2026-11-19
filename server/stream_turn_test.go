package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
	"shelley.exe.dev/llm/streamtest"
)

// streamingTestService streams each reply's text blocks in small pieces
// before returning the reply, as a streaming provider does.
type streamingTestService struct {
	llm.Service

	mu   sync.Mutex
	text string // every text delta streamed, in order
}

// stream sends one delta and records its text.
func (s *streamingTestService) stream(req *llm.Request, d llm.StreamDelta) {
	if d.Type == "text" {
		s.mu.Lock()
		s.text += d.Text
		s.mu.Unlock()
	}
	req.OnStream(d)
}

func (s *streamingTestService) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	resp, err := s.Service.Do(ctx, req)
	if err != nil || req.OnStream == nil {
		return resp, err
	}
	for index, content := range resp.Content {
		if content.Type != llm.ContentTypeText {
			continue
		}
		for text := content.Text; text != ""; {
			n := min(5, len(text))
			s.stream(req, llm.StreamDelta{Type: "text", Text: text[:n], Index: index})
			text = text[n:]
		}
	}
	return resp, nil
}

// TestStreamedTurnDeltasMatchSavedMessages runs a real turn with a tool call
// and reads /api/stream2 as a client would. Each reply's deltas carry the
// message_id that reply is then published under, and a client that only
// appends text shows what the model streamed.
func TestStreamedTurnDeltasMatchSavedMessages(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	service := &streamingTestService{Service: predictable.NewService()}
	s.llmManager = &testLLMManager{service: service}
	conversation, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	conversationID := conversation.ConversationID
	manager, err := s.getOrCreateConversationManager(t.Context(), conversationID, "")
	if err != nil {
		t.Fatal(err)
	}

	// The client: connect, then append each delta's text to its message and
	// keep each saved message.
	ctx, cancel := context.WithCancel(t.Context())
	w := newResponseRecorderWithClose()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleStream(w, httptest.NewRequest(http.MethodGet, "/api/stream2", nil).WithContext(ctx))
	}()
	defer func() {
		w.Close()
		cancel()
		<-done
	}()
	var streamed streamtest.Client // text by message id
	var legacy streamtest.LegacyClient
	var saved map[string]APIMessage
	read := func() (live bool) {
		streamed, legacy, saved = streamtest.Client{}, streamtest.LegacyClient{}, map[string]APIMessage{}
		for _, frame := range decodeSSE(w.Snapshot()) {
			// The first frame (the conversation list) is written once the
			// connection is subscribed to live events.
			live = true
			if frame.ConversationID != conversationID {
				continue
			}
			if d := frame.StreamDelta; d != nil {
				if d.MessageID == "" {
					t.Fatalf("delta %+v has no message id", d)
				}
				streamed.Apply(t, *d)
				legacy.Apply(*d)
			}
			for _, m := range frame.Messages {
				saved[m.MessageID] = m
			}
		}
		return live
	}

	// Deltas are not replayed, so the turn starts once the stream is live.
	waitFor(t, 10*time.Second, read)
	if _, err := manager.AcceptUserMessage(t.Context(), service, "predictable", llm.UserStringMessage("bash: echo hi")); err != nil {
		t.Fatal(err)
	}
	// Two replies stream: the tool call's, and the final one.
	landed := func() bool {
		read()
		for id := range streamed {
			if _, ok := saved[id]; !ok {
				return false
			}
		}
		return len(streamed) == 2
	}
	waitFor(t, 10*time.Second, landed)

	for id, streamedReply := range streamed {
		message := saved[id]
		if message.Type != string(db.MessageTypeAgent) || message.LlmData == nil {
			t.Fatalf("deltas for %s landed as %+v, want an agent message", id, message)
		}
		var reply llm.Message
		if err := json.Unmarshal([]byte(*message.LlmData), &reply); err != nil {
			t.Fatal(err)
		}
		if got := reply.Content[0].Text; streamedReply.Text != got {
			t.Errorf("message %s: streamed %q, saved %q", id, streamedReply.Text, got)
		}
	}
	// A client that only appends text shows exactly what the model streamed.
	service.mu.Lock()
	streamedText := service.text
	service.mu.Unlock()
	if legacy.Text != streamedText {
		t.Errorf("an append-only client shows %q; the model streamed %q", legacy.Text, streamedText)
	}
	// The field name clients decode, as sent: every delta frame has a
	// message_id of its own, whatever Go calls the field.
	deltaFrames := 0
	for _, frame := range strings.Split(w.Snapshot(), "\n\n") {
		var raw struct {
			ConversationID string                     `json:"conversation_id"`
			StreamDelta    map[string]json.RawMessage `json:"stream_delta"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(frame), "data: ")), &raw) != nil ||
			raw.ConversationID != conversationID || raw.StreamDelta == nil {
			continue
		}
		deltaFrames++
		var id string
		if json.Unmarshal(raw.StreamDelta["message_id"], &id) != nil || id == "" {
			t.Errorf("stream_delta frame has no message_id: %s", frame)
		}
	}
	if deltaFrames == 0 {
		t.Error("stream body has no stream_delta frames")
	}
}

// TestPredictableReplyStreamsBeforeItLands sends a chat over HTTP and reads
// /api/stream2 as a client would: the predictable model's reply arrives in
// pieces before it is saved, and the saved message has its text once.
func TestPredictableReplyStreamsBeforeItLands(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	conversation, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	conversationID := conversation.ConversationID

	ctx, cancel := context.WithCancel(t.Context())
	w := newResponseRecorderWithClose()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleStream(w, httptest.NewRequest(http.MethodGet, "/api/stream2", nil).WithContext(ctx))
	}()
	defer func() {
		w.Close()
		cancel()
		<-done
	}()
	// Deltas are not replayed, so the turn starts once the stream is live.
	waitFor(t, 10*time.Second, func() bool { return len(decodeSSE(w.Snapshot())) > 0 })

	reply := "the quick brown fox jumps over the lazy dog, again and again"
	body, _ := json.Marshal(ChatRequest{Message: "stream: " + reply, Model: "predictable"})
	chat := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(body)))
	chat.Header.Set("Content-Type", "application/json")
	chatW := httptest.NewRecorder()
	s.handleChatConversation(chatW, chat, conversationID)
	if chatW.Code != http.StatusAccepted {
		t.Fatalf("chat: got %d: %s", chatW.Code, chatW.Body.String())
	}

	// What the client saw, in the order it arrived, up to the turn's end.
	var streamed string
	var deltaIDs map[string]bool
	var partials int
	var saved []APIMessage
	waitFor(t, 10*time.Second, func() bool {
		streamed, deltaIDs, partials, saved = "", map[string]bool{}, 0, nil
		working, ended := false, false
		for _, frame := range decodeSSE(w.Snapshot()) {
			if state := frame.ConversationState; state != nil && state.ConversationID == conversationID {
				ended = ended || (working && !state.Working)
				working = state.Working
			}
			if frame.ConversationID != conversationID {
				continue
			}
			if d := frame.StreamDelta; d != nil && d.Type == "text" {
				if len(saved) > 0 {
					t.Fatalf("delta %+v arrived after the saved reply", d)
				}
				streamed += d.Text
				deltaIDs[d.MessageID] = true
				if streamed != reply {
					partials++
				}
			}
			for _, m := range frame.Messages {
				if m.Type == string(db.MessageTypeAgent) {
					saved = append(saved, m)
				}
			}
		}
		return ended && len(saved) > 0
	})

	if partials == 0 {
		t.Errorf("the client never saw part of the reply before all of it; streamed %q", streamed)
	}
	if streamed != reply {
		t.Errorf("streamed %q, want %q", streamed, reply)
	}
	if len(saved) != 1 || saved[0].LlmData == nil {
		t.Fatalf("saved replies: %+v, want one", saved)
	}
	if len(deltaIDs) != 1 || !deltaIDs[saved[0].MessageID] {
		t.Errorf("deltas were for messages %v, saved as %q", deltaIDs, saved[0].MessageID)
	}
	var message llm.Message
	if err := json.Unmarshal([]byte(*saved[0].LlmData), &message); err != nil {
		t.Fatal(err)
	}
	var text string
	for _, c := range message.Content {
		text += c.Text
	}
	if text != reply {
		t.Errorf("saved %q, want the reply once: %q", text, reply)
	}
}
