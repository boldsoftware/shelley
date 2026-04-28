package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

func TestDistillConversation(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	// Create a conversation with some messages
	h.NewConversation("echo hello world", "")
	h.WaitResponse()
	sourceConvID := h.convID

	// Now call the distill endpoint
	reqBody := DistillConversationRequest{
		SourceConversationID: sourceConvID,
		Model:                "predictable",
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/api/conversations/distill", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.server.handleDistillConversation(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	newConvID, ok := resp["conversation_id"].(string)
	if !ok || newConvID == "" {
		t.Fatal("expected conversation_id in response")
	}

	// The new conversation should exist
	newConv, err := h.db.GetConversationByID(context.Background(), newConvID)
	if err != nil {
		t.Fatalf("failed to get new conversation: %v", err)
	}
	if newConv.Model == nil || *newConv.Model != "predictable" {
		t.Fatalf("expected model 'predictable', got %v", newConv.Model)
	}

	// There should be a system message initially (the status message)
	var hasSystemMsg bool
	for i := 0; i < 50; i++ {
		msgs, err := h.db.ListMessages(context.Background(), newConvID)
		if err != nil {
			t.Fatalf("failed to list messages: %v", err)
		}
		for _, msg := range msgs {
			if msg.Type == string(db.MessageTypeSystem) {
				hasSystemMsg = true
			}
		}
		if hasSystemMsg {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !hasSystemMsg {
		t.Fatal("expected a system status message")
	}

	// Wait for the distillation to complete (a user message should appear)
	var userMsg *string
	for i := 0; i < 100; i++ {
		msgs, err := h.db.ListMessages(context.Background(), newConvID)
		if err != nil {
			t.Fatalf("failed to list messages: %v", err)
		}
		for _, msg := range msgs {
			if msg.Type == string(db.MessageTypeUser) && msg.LlmData != nil {
				var llmMsg llm.Message
				if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err == nil {
					for _, content := range llmMsg.Content {
						if content.Type == llm.ContentTypeText && content.Text != "" {
							userMsg = &content.Text
						}
					}
				}
			}
		}
		if userMsg != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if userMsg == nil {
		t.Fatal("expected a user message with distilled content")
	}

	// The distilled message should contain some text (from the predictable service)
	if len(*userMsg) == 0 {
		t.Fatal("distilled message was empty")
	}

	// The status message should be updated to "complete"
	msgs, err := h.db.ListMessages(context.Background(), newConvID)
	if err != nil {
		t.Fatalf("failed to list messages: %v", err)
	}
	var statusComplete bool
	for _, msg := range msgs {
		if msg.Type == string(db.MessageTypeSystem) && msg.UserData != nil {
			var userData map[string]string
			if err := json.Unmarshal([]byte(*msg.UserData), &userData); err == nil {
				if userData["distill_status"] == "complete" {
					statusComplete = true
				}
			}
		}
	}
	if !statusComplete {
		t.Fatal("expected distill status to be 'complete'")
	}
}

func TestDistillConversationMissingSource(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	reqBody := DistillConversationRequest{
		SourceConversationID: "nonexistent-id",
		Model:                "predictable",
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/api/conversations/distill", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.server.handleDistillConversation(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDistillConversationEmptySource(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	reqBody := DistillConversationRequest{
		SourceConversationID: "",
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/api/conversations/distill", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.server.handleDistillConversation(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestBuildDistillTranscript(t *testing.T) {
	t.Parallel()
	// Nil messages: only slug header.
	transcript := buildDistillTranscript("test-convo", nil)
	if !strings.Contains(transcript, "test-convo") {
		t.Fatal("expected slug in transcript")
	}

	makeMsg := func(typ string, llmMsg llm.Message) generated.Message {
		data, _ := json.Marshal(llmMsg)
		s := string(data)
		return generated.Message{Type: typ, LlmData: &s}
	}

	// User text message
	msgs := []generated.Message{
		makeMsg(string(db.MessageTypeUser), llm.Message{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello world"}},
		}),
	}
	transcript = buildDistillTranscript("slug", msgs)
	if !strings.Contains(transcript, "User: hello world") {
		t.Fatalf("expected user text, got: %s", transcript)
	}

	// Agent text gets truncated at 2000 bytes
	longText := strings.Repeat("x", 3000)
	msgs = []generated.Message{
		makeMsg(string(db.MessageTypeAgent), llm.Message{
			Role:    llm.MessageRoleAssistant,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: longText}},
		}),
	}
	transcript = buildDistillTranscript("slug", msgs)
	if strings.Contains(transcript, longText) {
		t.Fatal("expected long text to be truncated")
	}
	if !strings.Contains(transcript, "...") {
		t.Fatal("expected truncation indicator")
	}

	// Tool use with long input
	msgs = []generated.Message{
		makeMsg(string(db.MessageTypeAgent), llm.Message{
			Role: llm.MessageRoleAssistant,
			Content: []llm.Content{{
				Type:      llm.ContentTypeToolUse,
				ToolName:  "bash",
				ToolInput: json.RawMessage(`"` + strings.Repeat("a", 600) + `"`),
			}},
		}),
	}
	transcript = buildDistillTranscript("slug", msgs)
	if !strings.Contains(transcript, "[Tool: bash]") {
		t.Fatalf("expected tool use, got: %s", transcript)
	}

	// Tool result with error flag
	msgs = []generated.Message{
		makeMsg(string(db.MessageTypeUser), llm.Message{
			Role: llm.MessageRoleUser,
			Content: []llm.Content{{
				Type:       llm.ContentTypeToolResult,
				ToolError:  true,
				ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: "command not found"}},
			}},
		}),
	}
	transcript = buildDistillTranscript("slug", msgs)
	if !strings.Contains(transcript, "(error)") {
		t.Fatalf("expected error flag, got: %s", transcript)
	}
	if !strings.Contains(transcript, "command not found") {
		t.Fatalf("expected error text, got: %s", transcript)
	}

	// System messages are skipped
	msgs = []generated.Message{
		{Type: string(db.MessageTypeSystem)},
		makeMsg(string(db.MessageTypeUser), llm.Message{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "visible"}},
		}),
	}
	transcript = buildDistillTranscript("slug", msgs)
	if strings.Contains(transcript, "System") {
		t.Fatal("system messages should be skipped")
	}
	if !strings.Contains(transcript, "visible") {
		t.Fatal("user message should be present")
	}

	// Nil LlmData is skipped
	msgs = []generated.Message{
		{Type: string(db.MessageTypeUser), LlmData: nil},
	}
	transcript = buildDistillTranscript("slug", msgs)
	// Should just have the slug header with no crash
	if !strings.Contains(transcript, "slug") {
		t.Fatal("expected slug")
	}
}

func TestTruncateUTF8(t *testing.T) {
	t.Parallel()
	// No truncation needed
	result := truncateUTF8("hello", 10)
	if result != "hello" {
		t.Fatalf("expected 'hello', got %q", result)
	}

	result = truncateUTF8("hello world", 5)
	if result != "hello..." {
		t.Fatalf("expected 'hello...', got %q", result)
	}

	// Multi-byte: don't split a rune. "é" is 2 bytes (0xC3 0xA9).
	// "aé" = 3 bytes. Truncating at 2 should not split the é.
	result = truncateUTF8("aé", 2)
	if result != "a..." {
		t.Fatalf("expected 'a...', got %q", result)
	}

	// Exactly fitting multi-byte
	result = truncateUTF8("aé", 3)
	if result != "aé" {
		t.Fatalf("expected 'aé', got %q", result)
	}

	// Empty string
	result = truncateUTF8("", 5)
	if result != "" {
		t.Fatalf("expected empty, got %q", result)
	}

	// 4-byte char (emoji: 🎉)
	result = truncateUTF8("a🎉b", 2)
	if result != "a..." {
		t.Fatalf("expected 'a...', got %q", result)
	}
}

// TestDistillContentSentToLLM verifies that after distillation completes,
// the distilled user message is actually included in the LLM request
// when the user sends a follow-up message in the distilled conversation.
func TestDistillContentSentToLLM(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	// Create a source conversation with some messages
	h.NewConversation("echo hello world", "")
	h.WaitResponse()
	sourceConvID := h.convID

	// Distill the source conversation
	reqBody := DistillConversationRequest{
		SourceConversationID: sourceConvID,
		Model:                "predictable",
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/api/conversations/distill", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.server.handleDistillConversation(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var distillResp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &distillResp); err != nil {
		t.Fatalf("failed to parse distill response: %v", err)
	}
	newConvID := distillResp["conversation_id"].(string)

	// Wait for the distillation to produce a user message
	var distilledText string
	for i := 0; i < 100; i++ {
		msgs, err := h.db.ListMessages(context.Background(), newConvID)
		if err != nil {
			t.Fatalf("failed to list messages: %v", err)
		}
		for _, msg := range msgs {
			if msg.Type == string(db.MessageTypeUser) && msg.LlmData != nil {
				var llmMsg llm.Message
				if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err == nil {
					for _, content := range llmMsg.Content {
						if content.Type == llm.ContentTypeText && content.Text != "" {
							distilledText = content.Text
						}
					}
				}
			}
		}
		if distilledText != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if distilledText == "" {
		t.Fatal("timed out waiting for distilled user message")
	}
	t.Logf("Distilled text: %q", distilledText)

	// Clear LLM request history so we can see only the next request
	h.llm.ClearRequests()

	// Now send a follow-up message to the distilled conversation
	h.convID = newConvID
	h.responsesCount = 0

	// Wait for distilling to fully complete (defer runs after slug gen)
	for i := 0; i < 100; i++ {
		h.server.mu.Lock()
		m, ok := h.server.activeConversations[newConvID]
		h.server.mu.Unlock()
		if !ok {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		m.mu.Lock()
		d := m.distilling
		m.mu.Unlock()
		if !d {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	h.Chat("echo followup message")
	respText := h.WaitResponse()
	t.Logf("Follow-up agent response: %q", respText)

	// Inspect the LLM request that was sent
	reqs := h.llm.GetRecentRequests()
	if len(reqs) == 0 {
		t.Fatal("no LLM requests recorded after sending follow-up message")
	}

	// Find the request that contains our follow-up message
	var targetReq *llm.Request
	for i := range reqs {
		for _, msg := range reqs[i].Messages {
			for _, c := range msg.Content {
				if c.Type == llm.ContentTypeText && c.Text == "echo followup message" {
					targetReq = reqs[i]
				}
			}
		}
	}
	if targetReq == nil {
		t.Fatalf("could not find LLM request containing follow-up message; got %d requests", len(reqs))
	}
	firstReq := targetReq

	t.Logf("LLM request has %d messages", len(firstReq.Messages))
	for i, msg := range firstReq.Messages {
		for _, content := range msg.Content {
			if content.Type == llm.ContentTypeText {
				t.Logf("  Message[%d] role=%s text=%q", i, msg.Role, truncateForLog(content.Text, 100))
			}
		}
	}

	// Verify the distilled text appears in the messages sent to the LLM
	found := false
	for _, msg := range firstReq.Messages {
		for _, content := range msg.Content {
			if content.Type == llm.ContentTypeText && content.Text == distilledText {
				found = true
				break
			}
		}
		if found {
			break
		}
	}

	if !found {
		t.Fatalf("distilled text was NOT found in the LLM request messages!\n"+
			"Distilled text: %q\n"+
			"This means the distillation content is not being sent to the LLM.",
			distilledText)
	}

	t.Log("SUCCESS: distilled content IS being sent to the LLM")
}

func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// TestDistillContentSentToLLM_WithEarlySSE verifies that if the SSE stream
// is opened BEFORE distillation completes (causing Hydrate to run early),
// the distilled user message is still included in the LLM request.
func TestDistillContentSentToLLM_WithEarlySSE(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	// Create a source conversation with some messages
	h.NewConversation("echo hello world", "")
	h.WaitResponse()
	sourceConvID := h.convID

	// Distill the source conversation
	reqBody := DistillConversationRequest{
		SourceConversationID: sourceConvID,
		Model:                "predictable",
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/api/conversations/distill", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.server.handleDistillConversation(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var distillResp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &distillResp); err != nil {
		t.Fatalf("failed to parse distill response: %v", err)
	}
	newConvID := distillResp["conversation_id"].(string)

	// Simulate what the UI does: open the SSE stream immediately,
	// which triggers getOrCreateConversationManager -> Hydrate BEFORE
	// the distilled message is written.
	// This forces hydration with an empty history.
	manager, err := h.server.getOrCreateConversationManager(context.Background(), newConvID, "")
	if err != nil {
		t.Fatalf("failed to get/create conversation manager: %v", err)
	}
	_ = manager // just force it to exist and be hydrated

	// Now wait for the distillation to produce a user message
	var distilledText string
	for i := 0; i < 100; i++ {
		msgs, err := h.db.ListMessages(context.Background(), newConvID)
		if err != nil {
			t.Fatalf("failed to list messages: %v", err)
		}
		for _, msg := range msgs {
			if msg.Type == string(db.MessageTypeUser) && msg.LlmData != nil {
				var llmMsg llm.Message
				if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err == nil {
					for _, content := range llmMsg.Content {
						if content.Type == llm.ContentTypeText && content.Text != "" {
							distilledText = content.Text
						}
					}
				}
			}
		}
		if distilledText != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if distilledText == "" {
		t.Fatal("timed out waiting for distilled user message")
	}
	t.Logf("Distilled text: %q", distilledText)

	// Clear LLM request history
	h.llm.ClearRequests()

	// Wait for distilling to fully complete (defer runs after slug gen)
	for i := 0; i < 100; i++ {
		manager.mu.Lock()
		d := manager.distilling
		manager.mu.Unlock()
		if !d {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Now send a follow-up message to the distilled conversation
	h.convID = newConvID
	h.responsesCount = 0
	h.Chat("echo followup message")
	h.WaitResponse()

	// Wait a moment for any async slug generation to also complete
	time.Sleep(200 * time.Millisecond)

	// Inspect ALL LLM requests that were sent
	reqs := h.llm.GetRecentRequests()
	if len(reqs) == 0 {
		t.Fatal("no LLM requests recorded after sending follow-up message")
	}

	t.Logf("Total LLM requests: %d", len(reqs))
	for ri, r := range reqs {
		t.Logf("Request[%d] has %d messages:", ri, len(r.Messages))
		for i, msg := range r.Messages {
			for _, content := range msg.Content {
				if content.Type == llm.ContentTypeText {
					t.Logf("  Message[%d] role=%s text=%q", i, msg.Role, truncateForLog(content.Text, 120))
				}
			}
		}
	}

	// Verify the distilled text appears in at least one of the LLM requests
	found := false
	for _, r := range reqs {
		for _, msg := range r.Messages {
			for _, content := range msg.Content {
				if content.Type == llm.ContentTypeText && content.Text == distilledText {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if found {
			break
		}
	}

	if !found {
		t.Fatalf("BUG CONFIRMED: distilled text was NOT found in ANY LLM request messages!\n"+
			"Distilled text: %q\n"+
			"When the SSE stream is opened before distillation completes, "+
			"the ConversationManager hydrates with empty history and never reloads.",
			distilledText)
	}

	t.Log("SUCCESS: distilled content IS being sent to the LLM even with early SSE")
}

// TestDistillStatusUpdateReachesSSESubscriber verifies that when an SSE subscriber
// has already received the "in_progress" system message, the subsequent "complete"
// status update is delivered via broadcast (not publish), so the subscriber sees it.
// This is a regression test for https://github.com/boldsoftware/shelley/issues/117
// where the spinner would spin forever because Publish skipped subscribers that
// already had the message's sequence ID.
func TestDistillStatusUpdateReachesSSESubscriber(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	// Create a source conversation with some messages
	h.NewConversation("echo hello world", "")
	h.WaitResponse()
	sourceConvID := h.convID

	// Create the new conversation and status message manually (same as
	// handleDistillConversation but without starting the goroutine) so we
	// can establish the SSE subscription before triggering distillation.
	model := "predictable"
	newConv, err := h.server.db.CreateConversation(context.Background(), nil, true, nil, &model, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	newConvID := newConv.ConversationID

	// Insert the "in_progress" status message
	statusUserData := map[string]string{
		"distill_status": "in_progress",
		"source_slug":    "test-source",
	}
	_, err = h.server.db.CreateMessage(context.Background(), db.CreateMessageParams{
		ConversationID:      newConvID,
		Type:                db.MessageTypeSystem,
		UserData:            statusUserData,
		ExcludedFromContext: true,
	})
	if err != nil {
		t.Fatalf("failed to create status message: %v", err)
	}

	// Get the conversation manager and mark it as distilling
	manager, err := h.server.getOrCreateConversationManager(context.Background(), newConvID, "")
	if err != nil {
		t.Fatalf("failed to create conversation manager: %v", err)
	}
	manager.SetDistilling(true)

	// Open an SSE stream to the new conversation (like the UI does).
	// This will receive the initial "in_progress" system message.
	sseRecorder := newFlusherRecorder()
	sseCtx, sseCancel := context.WithCancel(context.Background())
	defer sseCancel()
	sseReq := httptest.NewRequest("GET", "/api/conversations/"+newConvID+"/stream", nil)
	sseReq = sseReq.WithContext(sseCtx)
	go func() {
		h.server.handleStreamConversation(sseRecorder, sseReq, newConvID)
	}()

	// Wait for the initial SSE message (should contain "in_progress")
	select {
	case <-sseRecorder.flushed:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for initial SSE message")
	}

	// Now trigger the distillation. The SSE handler is already subscribed.
	sourceMsgs, err := h.server.db.ListMessages(context.Background(), sourceConvID)
	if err != nil {
		t.Fatalf("failed to list source messages: %v", err)
	}
	go func() {
		h.server.runDistillation(context.Background(), newConvID, "test-source", "predictable", sourceMsgs)
	}()

	// Now wait for the distillation to complete by watching the SSE stream
	// for a message containing "complete" status
	var sawComplete bool
	for i := 0; i < 200; i++ {
		body := sseRecorder.getString()
		if strings.Contains(body, `distill_status\":\"complete`) {
			sawComplete = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !sawComplete {
		// Check what was actually in the SSE stream
		t.Fatalf("SSE subscriber never received the 'complete' status update.\n"+
			"This is the distill spinner bug: the subscriber already had the message's sequence_id,\n"+
			"so Publish skipped it. The fix is to use Broadcast for in-place message updates.\n"+
			"SSE body: %s", sseRecorder.getString())
	}
}

func TestDistillReplaceConversation(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := NewTestHarness(t)
		defer stopActiveConversationLoops(h.server)

		// Create a source conversation with some messages
		h.NewConversation("echo hello world", "")
		h.WaitResponse()
		synctest.Wait()
		sourceConvID := h.convID

		// Give the source conversation a slug
		originalSlug := "test-original-slug"
		_, err := h.db.UpdateConversationSlug(context.Background(), sourceConvID, originalSlug)
		if err != nil {
			t.Fatalf("failed to set source slug: %v", err)
		}

		// Call the distill-replace endpoint
		reqBody := DistillReplaceRequest{
			SourceConversationID: sourceConvID,
			Model:                "predictable",
		}
		body, _ := json.Marshal(reqBody)
		req := httptest.NewRequest("POST", "/api/conversations/distill-replace", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		h.server.handleDistillReplace(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("expected status 201, got %d: %s", w.Code, w.Body.String())
		}

		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		newConvID, ok := resp["conversation_id"].(string)
		if !ok || newConvID == "" {
			t.Fatal("expected conversation_id in response")
		}

		waitForConversationDistillingToClear(t, h.server, newConvID)
		requireDistilledUserMessage(t, h.db, newConvID)

		// Verify slug swap: new conversation should have the original slug
		newConv, err := h.db.GetConversationByID(context.Background(), newConvID)
		if err != nil {
			t.Fatalf("failed to get new conversation: %v", err)
		}
		if newConv.Slug == nil || *newConv.Slug != originalSlug {
			t.Fatalf("expected new conv slug %q, got %v", originalSlug, newConv.Slug)
		}

		// Verify source conversation was renamed
		sourceConv, err := h.db.GetConversationByID(context.Background(), sourceConvID)
		if err != nil {
			t.Fatalf("failed to get source conversation: %v", err)
		}
		if sourceConv.Slug == nil || !strings.HasPrefix(*sourceConv.Slug, originalSlug+"-prev") {
			t.Fatalf("expected source slug to start with %q, got %v", originalSlug+"-prev", sourceConv.Slug)
		}

		// Verify source is now a child of the new conversation
		if sourceConv.ParentConversationID == nil || *sourceConv.ParentConversationID != newConvID {
			t.Fatalf("expected source parent_conversation_id=%q, got %v", newConvID, sourceConv.ParentConversationID)
		}

		// Verify source is archived
		if !sourceConv.Archived {
			t.Fatal("expected source conversation to be archived")
		}

		// Verify status message has replace=true
		msgs, err := h.db.ListMessages(context.Background(), newConvID)
		if err != nil {
			t.Fatalf("failed to list messages: %v", err)
		}
		var hasReplaceStatus bool
		for _, msg := range msgs {
			if msg.Type == string(db.MessageTypeSystem) && msg.UserData != nil {
				var userData map[string]string
				if err := json.Unmarshal([]byte(*msg.UserData), &userData); err == nil {
					if userData["replace"] == "true" && userData["distill_status"] == "complete" {
						hasReplaceStatus = true
					}
				}
			}
		}
		if !hasReplaceStatus {
			t.Fatal("expected system message with replace=true and distill_status=complete")
		}
	})
}

func TestDistillReplaceConversationMissingSource(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	reqBody := DistillReplaceRequest{
		SourceConversationID: "nonexistent-id",
		Model:                "predictable",
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/api/conversations/distill-replace", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.server.handleDistillReplace(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDistillReplaceConversationNoSlug(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := NewTestHarness(t)
		defer stopActiveConversationLoops(h.server)

		// Create a source conversation and wait for the response to complete.
		h.NewConversation("echo hello world", "")
		h.WaitResponse()
		synctest.Wait()
		sourceConvID := h.convID

		// Explicitly clear the source slug so we deterministically test the
		// no-slug path.
		_, err := h.db.ClearConversationSlug(context.Background(), sourceConvID)
		if err != nil {
			t.Fatalf("failed to clear source slug: %v", err)
		}

		// Confirm the source has no slug.
		sourceConvBefore, err := h.db.GetConversationByID(context.Background(), sourceConvID)
		if err != nil {
			t.Fatalf("failed to get source conversation: %v", err)
		}
		if sourceConvBefore.Slug != nil {
			t.Fatalf("expected source slug to be nil after clearing, got %q", *sourceConvBefore.Slug)
		}

		// Call the distill-replace endpoint
		reqBody := DistillReplaceRequest{
			SourceConversationID: sourceConvID,
			Model:                "predictable",
		}
		body, _ := json.Marshal(reqBody)
		req := httptest.NewRequest("POST", "/api/conversations/distill-replace", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		h.server.handleDistillReplace(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("expected status 201, got %d: %s", w.Code, w.Body.String())
		}

		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}
		newConvID := resp["conversation_id"].(string)

		waitForConversationDistillingToClear(t, h.server, newConvID)
		requireDistilledUserMessage(t, h.db, newConvID)

		// The new conversation should have gotten its own generated slug (not
		// transferred from source, since source had none).
		newConv, err := h.db.GetConversationByID(context.Background(), newConvID)
		if err != nil {
			t.Fatalf("failed to get new conversation: %v", err)
		}
		if newConv.Slug == nil {
			t.Fatal("expected new conversation to have a generated slug")
		}

		// Verify source is archived and parented
		sourceConvAfter, err := h.db.GetConversationByID(context.Background(), sourceConvID)
		if err != nil {
			t.Fatalf("failed to get source conversation: %v", err)
		}
		if !sourceConvAfter.Archived {
			t.Fatal("expected source conversation to be archived")
		}
		if sourceConvAfter.ParentConversationID == nil || *sourceConvAfter.ParentConversationID != newConvID {
			t.Fatalf("expected source parent=%q, got %v", newConvID, sourceConvAfter.ParentConversationID)
		}
	})
}

func TestDistillReplaceMultiPass(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := NewTestHarness(t)
		defer stopActiveConversationLoops(h.server)

		// Create a source conversation with messages
		h.NewConversation("echo hello world", "")
		h.WaitResponse()
		synctest.Wait()
		sourceConvID := h.convID

		// Give it a slug
		originalSlug := "multi-pass-test"
		_, err := h.db.UpdateConversationSlug(context.Background(), sourceConvID, originalSlug)
		if err != nil {
			t.Fatalf("failed to set source slug: %v", err)
		}

		// First distill-replace
		reqBody := DistillReplaceRequest{
			SourceConversationID: sourceConvID,
			Model:                "predictable",
		}
		body, _ := json.Marshal(reqBody)
		req := httptest.NewRequest("POST", "/api/conversations/distill-replace", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.server.handleDistillReplace(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("first distill: expected 201, got %d: %s", w.Code, w.Body.String())
		}

		var resp1 map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp1)
		conv1ID := resp1["conversation_id"].(string)

		waitForConversationDistillingToClear(t, h.server, conv1ID)
		requireDistilledUserMessage(t, h.db, conv1ID)

		// Verify first pass: conv1 has the original slug, source renamed to -prev
		conv1, err := h.db.GetConversationByID(context.Background(), conv1ID)
		if err != nil {
			t.Fatalf("first pass: failed to get conv1: %v", err)
		}
		if conv1.Slug == nil || *conv1.Slug != originalSlug {
			t.Fatalf("first pass: expected conv1 slug %q, got %v", originalSlug, conv1.Slug)
		}
		source, err := h.db.GetConversationByID(context.Background(), sourceConvID)
		if err != nil {
			t.Fatalf("first pass: failed to get source: %v", err)
		}
		if source.Slug == nil || *source.Slug != originalSlug+"-prev" {
			t.Fatalf("first pass: expected source slug %q, got %v", originalSlug+"-prev", source.Slug)
		}

		// Second distill-replace on the NEW conversation (conv1)
		reqBody2 := DistillReplaceRequest{
			SourceConversationID: conv1ID,
			Model:                "predictable",
		}
		body2, _ := json.Marshal(reqBody2)
		req2 := httptest.NewRequest("POST", "/api/conversations/distill-replace", strings.NewReader(string(body2)))
		req2.Header.Set("Content-Type", "application/json")
		w2 := httptest.NewRecorder()
		h.server.handleDistillReplace(w2, req2)
		if w2.Code != http.StatusCreated {
			t.Fatalf("second distill: expected 201, got %d: %s", w2.Code, w2.Body.String())
		}

		var resp2 map[string]interface{}
		json.Unmarshal(w2.Body.Bytes(), &resp2)
		conv2ID := resp2["conversation_id"].(string)

		waitForConversationDistillingToClear(t, h.server, conv2ID)
		requireDistilledUserMessage(t, h.db, conv2ID)

		// Verify second pass:
		// - conv2 has the original slug
		// - conv1 renamed to -prev-2 (since -prev is taken by the original source)
		// - original source still has -prev
		conv2, err := h.db.GetConversationByID(context.Background(), conv2ID)
		if err != nil {
			t.Fatalf("second pass: failed to get conv2: %v", err)
		}
		if conv2.Slug == nil || *conv2.Slug != originalSlug {
			t.Fatalf("second pass: expected conv2 slug %q, got %v", originalSlug, conv2.Slug)
		}
		conv1After, err := h.db.GetConversationByID(context.Background(), conv1ID)
		if err != nil {
			t.Fatalf("second pass: failed to get conv1: %v", err)
		}
		if conv1After.Slug == nil || *conv1After.Slug != originalSlug+"-prev-2" {
			t.Fatalf("second pass: expected conv1 slug %q, got %v", originalSlug+"-prev-2", conv1After.Slug)
		}
		sourceAfter, err := h.db.GetConversationByID(context.Background(), sourceConvID)
		if err != nil {
			t.Fatalf("second pass: failed to get original source: %v", err)
		}
		if sourceAfter.Slug == nil || *sourceAfter.Slug != originalSlug+"-prev" {
			t.Fatalf("second pass: source slug changed unexpectedly to %v", sourceAfter.Slug)
		}

		// Third distill-replace on conv2
		reqBody3 := DistillReplaceRequest{
			SourceConversationID: conv2ID,
			Model:                "predictable",
		}
		body3, _ := json.Marshal(reqBody3)
		req3 := httptest.NewRequest("POST", "/api/conversations/distill-replace", strings.NewReader(string(body3)))
		req3.Header.Set("Content-Type", "application/json")
		w3 := httptest.NewRecorder()
		h.server.handleDistillReplace(w3, req3)
		if w3.Code != http.StatusCreated {
			t.Fatalf("third distill: expected 201, got %d: %s", w3.Code, w3.Body.String())
		}

		var resp3 map[string]interface{}
		json.Unmarshal(w3.Body.Bytes(), &resp3)
		conv3ID := resp3["conversation_id"].(string)

		waitForConversationDistillingToClear(t, h.server, conv3ID)
		requireDistilledUserMessage(t, h.db, conv3ID)

		// Verify third pass:
		// - conv3 has the original slug
		// - conv2 renamed to -prev-3
		conv3, err := h.db.GetConversationByID(context.Background(), conv3ID)
		if err != nil {
			t.Fatalf("third pass: failed to get conv3: %v", err)
		}
		if conv3.Slug == nil || *conv3.Slug != originalSlug {
			t.Fatalf("third pass: expected conv3 slug %q, got %v", originalSlug, conv3.Slug)
		}
		conv2After, err := h.db.GetConversationByID(context.Background(), conv2ID)
		if err != nil {
			t.Fatalf("third pass: failed to get conv2: %v", err)
		}
		if conv2After.Slug == nil || *conv2After.Slug != originalSlug+"-prev-3" {
			t.Fatalf("third pass: expected conv2 slug %q, got %v", originalSlug+"-prev-3", conv2After.Slug)
		}

		t.Log("SUCCESS: three-pass distill-replace completed, all slugs correct")
	})
}

func TestDistillReplaceQueuedMessagesDuringDistillation(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := NewTestHarness(t)
		defer stopActiveConversationLoops(h.server)

		// Create a source conversation
		h.NewConversation("echo hello world", "")
		h.WaitResponse()
		synctest.Wait()
		sourceConvID := h.convID

		originalSlug := "queued-msg-test"
		_, err := h.db.UpdateConversationSlug(context.Background(), sourceConvID, originalSlug)
		if err != nil {
			t.Fatalf("failed to set source slug: %v", err)
		}

		// Distill-replace
		reqBody := DistillReplaceRequest{
			SourceConversationID: sourceConvID,
			Model:                "predictable",
		}
		body, _ := json.Marshal(reqBody)
		req := httptest.NewRequest("POST", "/api/conversations/distill-replace", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.server.handleDistillReplace(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
		}

		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)
		newConvID := resp["conversation_id"].(string)

		// Immediately queue a message to the new conversation while distillation is in progress.
		// The ConversationManager should exist (created in handleDistillReplace) with distilling=true.
		manager, err := h.server.getOrCreateConversationManager(context.Background(), newConvID, "")
		if err != nil {
			t.Fatalf("failed to get conversation manager: %v", err)
		}

		// Verify the manager is marked as distilling
		manager.mu.Lock()
		isDistilling := manager.distilling
		manager.mu.Unlock()
		if !isDistilling {
			t.Fatal("expected conversation manager to be in distilling state")
		}

		// Queue a message
		userMsg := llm.Message{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "echo queued during distill"}},
		}
		if err := manager.QueueMessage(context.Background(), h.server, "predictable", userMsg); err != nil {
			t.Fatalf("failed to queue message: %v", err)
		}

		// Verify the message is pending (not drained yet)
		manager.mu.Lock()
		pendingCount := len(manager.pendingMessages)
		manager.mu.Unlock()
		if pendingCount != 1 {
			t.Fatalf("expected 1 pending message, got %d", pendingCount)
		}

		waitForConversationDistillingToClear(t, h.server, newConvID)
		requireDistilledUserMessage(t, h.db, newConvID)
		synctest.Wait()

		// After distillation completes, the deferred cleanup should have cleared
		// the distilling flag and drained the pending messages.
		msgs, err := h.db.ListMessages(context.Background(), newConvID)
		if err != nil {
			t.Fatalf("failed to list messages after distillation: %v", err)
		}
		var agentResponse bool
		for _, msg := range msgs {
			if msg.Type == string(db.MessageTypeAgent) {
				agentResponse = true
				break
			}
		}
		if !agentResponse {
			t.Fatal("expected agent response after queued message was drained")
		}

		// Verify distilling flag is cleared
		manager.mu.Lock()
		stillDistilling := manager.distilling
		manager.mu.Unlock()
		if stillDistilling {
			t.Fatal("distilling flag should be cleared after distillation completes")
		}

		t.Log("SUCCESS: queued message was properly held during distillation and drained after")
	})
}

func waitForConversationDistillingToClear(t *testing.T, server *Server, convID string) {
	t.Helper()

	server.mu.Lock()
	manager, ok := server.activeConversations[convID]
	server.mu.Unlock()
	if !ok {
		t.Fatalf("expected active conversation manager for %s", convID)
	}

	synctest.Wait()

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.distilling {
		t.Fatalf("expected distilling=false for %s after synctest wait", convID)
	}
}

func requireDistilledUserMessage(t *testing.T, database *db.DB, convID string) {
	t.Helper()

	msgs, err := database.ListMessages(context.Background(), convID)
	if err != nil {
		t.Fatalf("failed to list messages for %s: %v", convID, err)
	}

	for _, msg := range msgs {
		if msg.Type != string(db.MessageTypeUser) || msg.LlmData == nil || msg.UserData == nil {
			continue
		}

		var userData map[string]string
		if err := json.Unmarshal([]byte(*msg.UserData), &userData); err != nil {
			continue
		}
		if userData["distilled"] != "true" {
			continue
		}

		var llmMsg llm.Message
		if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
			t.Fatalf("failed to parse distilled message llm_data in %s: %v", convID, err)
		}

		for _, content := range llmMsg.Content {
			if content.Type == llm.ContentTypeText && content.Text != "" {
				return
			}
		}

		t.Fatalf("expected distilled user message in %s to contain text", convID)
	}

	t.Fatalf("expected distilled user message in %s", convID)
}

func stopActiveConversationLoops(server *Server) {
	server.mu.Lock()
	managers := make([]*ConversationManager, 0, len(server.activeConversations))
	for _, manager := range server.activeConversations {
		managers = append(managers, manager)
	}
	server.mu.Unlock()

	for _, manager := range managers {
		manager.stopLoop()
	}
}
