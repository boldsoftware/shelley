package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

// TestStreamResumeWithLastSequenceID verifies that using last_sequence_id
// parameter only sends messages newer than the given sequence ID.
func TestStreamResumeWithLastSequenceID(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	ctx := context.Background()

	// Create a non-user-initiated conversation so that Hydrate() doesn't
	// try to generate a system prompt (which races with the request context
	// and adds an unexpected message that throws off sequence ID accounting).
	conv, err := database.CreateConversation(ctx, nil, false, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create conversation: %v", err)
	}

	// Add a user message
	userMsg := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hello"}},
	}
	_, err = database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           db.MessageTypeUser,
		LLMData:        userMsg,
	})
	if err != nil {
		t.Fatalf("Failed to create user message: %v", err)
	}

	// Add an agent message
	agentMsg := llm.Message{
		Role:      llm.MessageRoleAssistant,
		Content:   []llm.Content{{Type: llm.ContentTypeText, Text: "Hi there!"}},
		EndOfTurn: true,
	}
	_, err = database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           db.MessageTypeAgent,
		LLMData:        agentMsg,
	})
	if err != nil {
		t.Fatalf("Failed to create agent message: %v", err)
	}

	mux := http.NewServeMux()
	server.RegisterRoutes(mux)

	// Test 1: Fresh connection (no last_sequence_id) - should get all messages.
	// We also capture lastSeqID here for use in subsequent subtests.
	var lastSeqID int64
	t.Run("fresh_connection", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		req := httptest.NewRequest("GET", "/api/conversation/"+conv.ConversationID+"/stream", nil).WithContext(ctx)
		req.Header.Set("Accept", "text/event-stream")

		w := newResponseRecorderWithClose()

		done := make(chan struct{})
		go func() {
			defer close(done)
			mux.ServeHTTP(w, req)
		}()

		body := waitForSSEData(t, w, 5*time.Second)
		w.Close()
		cancel()
		<-done

		if !strings.HasPrefix(body, "data: ") {
			t.Fatalf("Expected SSE data, got: %s", body)
		}

		jsonData := strings.TrimPrefix(strings.Split(body, "\n")[0], "data: ")
		var response StreamResponse
		if err := json.Unmarshal([]byte(jsonData), &response); err != nil {
			t.Fatalf("Failed to parse response: %v", err)
		}

		if len(response.Messages) != 2 {
			t.Errorf("Expected 2 messages, got %d", len(response.Messages))
		}
		if response.Heartbeat {
			t.Error("Fresh connection should not be a heartbeat")
		}

		// Record the last sequence ID for resume tests.
		for _, m := range response.Messages {
			if m.SequenceID > lastSeqID {
				lastSeqID = m.SequenceID
			}
		}
	})

	// Test 2: Resume with no new messages - should get heartbeat
	t.Run("resume_no_new_messages", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		url := fmt.Sprintf("/api/conversation/%s/stream?last_sequence_id=%d", conv.ConversationID, lastSeqID)
		req := httptest.NewRequest("GET", url, nil).WithContext(ctx)
		req.Header.Set("Accept", "text/event-stream")

		w := newResponseRecorderWithClose()
		done := make(chan struct{})
		go func() { defer close(done); mux.ServeHTTP(w, req) }()
		body := waitForSSEData(t, w, 5*time.Second)
		w.Close()
		cancel()
		<-done

		jsonData := strings.TrimPrefix(strings.Split(body, "\n")[0], "data: ")
		var response StreamResponse
		if err := json.Unmarshal([]byte(jsonData), &response); err != nil {
			t.Fatalf("Failed to parse response: %v", err)
		}
		if len(response.Messages) != 0 {
			t.Errorf("Expected 0 messages, got %d", len(response.Messages))
		}
		if !response.Heartbeat {
			t.Error("Resume with no new messages should be a heartbeat")
		}
	})

	// Test 3: Resume with missed messages - should get the missed messages
	t.Run("resume_with_missed_messages", func(t *testing.T) {
		// Add a new message with usage data (simulating what happens while client is disconnected)
		newMsg := llm.Message{
			Role:    llm.MessageRoleAssistant,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "You missed this!"}},
		}
		usage := llm.Usage{InputTokens: 5000, OutputTokens: 200}
		_, err := database.CreateMessage(ctx, db.CreateMessageParams{
			ConversationID: conv.ConversationID,
			Type:           db.MessageTypeAgent,
			LLMData:        newMsg,
			UsageData:      &usage,
		})
		if err != nil {
			t.Fatalf("Failed to create message: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		url := fmt.Sprintf("/api/conversation/%s/stream?last_sequence_id=%d", conv.ConversationID, lastSeqID)
		req := httptest.NewRequest("GET", url, nil).WithContext(ctx)
		req.Header.Set("Accept", "text/event-stream")

		w := newResponseRecorderWithClose()
		done := make(chan struct{})
		go func() { defer close(done); mux.ServeHTTP(w, req) }()
		body := waitForSSEData(t, w, 5*time.Second)
		w.Close()
		cancel()
		<-done

		jsonData := strings.TrimPrefix(strings.Split(body, "\n")[0], "data: ")
		var response StreamResponse
		if err := json.Unmarshal([]byte(jsonData), &response); err != nil {
			t.Fatalf("Failed to parse response: %v", err)
		}
		if len(response.Messages) != 1 {
			t.Errorf("Expected 1 missed message, got %d", len(response.Messages))
		}
		if response.Heartbeat {
			t.Error("Should not be a heartbeat when there are missed messages")
		}
		if response.ConversationState == nil {
			t.Error("Expected ConversationState")
		}
		if response.ContextWindowSize != 0 {
			t.Errorf("Resume should not send context_window_size (got %d)", response.ContextWindowSize)
		}
	})
}
