package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestDistillWritesEditableTempFileAndUsesEditedContent(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	h.NewConversation("echo hello world", "")
	h.WaitResponse()
	sourceConvID := h.convID

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

	var distillFile string
	var storedContent string
	var messageText string
	for i := 0; i < 100; i++ {
		msgs, err := h.db.ListMessages(context.Background(), newConvID)
		if err != nil {
			t.Fatalf("failed to list messages: %v", err)
		}
		for _, msg := range msgs {
			if msg.Type != string(db.MessageTypeUser) || msg.UserData == nil || msg.LlmData == nil {
				continue
			}
			var userData map[string]string
			if err := json.Unmarshal([]byte(*msg.UserData), &userData); err != nil {
				continue
			}
			if userData["distilled"] != "true" {
				continue
			}
			distillFile = userData["distillation_file"]
			storedContent = userData["distillation_content"]
			var llmMsg llm.Message
			if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err == nil {
				for _, content := range llmMsg.Content {
					if content.Type == llm.ContentTypeText {
						messageText = content.Text
					}
				}
			}
		}
		if distillFile != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if distillFile == "" {
		t.Fatal("timed out waiting for distilled message with file metadata")
	}
	if storedContent == "" {
		t.Fatal("expected distillation_content in user_data")
	}
	if !strings.HasPrefix(messageText, "Distillation written to ") || !strings.Contains(messageText, distillFile) {
		t.Fatalf("message should refer to temp file, got %q for %q", messageText, distillFile)
	}
	fileBytes, err := os.ReadFile(distillFile)
	if err != nil {
		t.Fatalf("failed to read distillation file %q: %v", distillFile, err)
	}
	if string(fileBytes) != storedContent {
		t.Fatalf("file content differs from stored content")
	}

	editedContent := "edited distillation content from test"
	if err := os.WriteFile(distillFile, []byte(editedContent), 0o644); err != nil {
		t.Fatalf("failed to edit distillation file: %v", err)
	}

	h.llm.ClearRequests()
	h.convID = newConvID
	h.responsesCount = 0
	for i := 0; i < 100; i++ {
		h.server.mu.Lock()
		m, ok := h.server.activeConversations[newConvID]
		h.server.mu.Unlock()
		if ok {
			m.mu.Lock()
			distilling := m.distilling
			m.mu.Unlock()
			if !distilling {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.Chat("echo followup message")
	h.WaitResponse()

	foundEdited := false
	foundReference := false
	for _, req := range h.llm.GetRecentRequests() {
		for _, msg := range req.Messages {
			for _, content := range msg.Content {
				if content.Type != llm.ContentTypeText {
					continue
				}
				if content.Text == editedContent {
					foundEdited = true
				}
				if strings.Contains(content.Text, "Distillation written to ") {
					foundReference = true
				}
			}
		}
	}
	if !foundEdited {
		t.Fatal("expected edited distillation file content in follow-up LLM request")
	}
	if foundReference {
		t.Fatal("follow-up LLM request should receive distillation contents, not file-reference message")
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
			if msg.Type == string(db.MessageTypeUser) && msg.UserData != nil {
				var userData map[string]string
				if err := json.Unmarshal([]byte(*msg.UserData), &userData); err == nil {
					distilledText = userData["distillation_content"]
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
			if msg.Type == string(db.MessageTypeUser) && msg.UserData != nil {
				var userData map[string]string
				if err := json.Unmarshal([]byte(*msg.UserData), &userData); err == nil {
					distilledText = userData["distillation_content"]
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

func TestStartNewGenerationFiltersContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := NewTestHarness(t)
		h.NewConversation("old context", "")
		h.WaitResponse()
		ctx := context.Background()
		convID := h.convID

		oldMsgs, err := h.db.ListMessagesForContext(ctx, convID)
		if err != nil {
			t.Fatalf("failed to list old context: %v", err)
		}
		if len(oldMsgs) == 0 {
			t.Fatal("expected old generation messages in context before generation bump")
		}

		conversation, err := h.server.startNewGeneration(ctx, convID)
		if err != nil {
			t.Fatalf("failed to start new generation: %v", err)
		}
		if conversation.CurrentGeneration != 2 {
			t.Fatalf("expected current_generation=2, got %d", conversation.CurrentGeneration)
		}

		afterBump, err := h.db.ListMessagesForContext(ctx, convID)
		if err != nil {
			t.Fatalf("failed to list context after bump: %v", err)
		}
		if len(afterBump) != 1 {
			t.Fatalf("expected 1 generation 2 message (system prompt), got %d", len(afterBump))
		}
		if afterBump[0].Type != string(db.MessageTypeSystem) {
			t.Fatalf("expected new generation context to start with a system prompt, got type %q", afterBump[0].Type)
		}
		if afterBump[0].Generation != 2 {
			t.Fatalf("expected new system prompt at generation 2, got %d", afterBump[0].Generation)
		}

		_, err = h.db.CreateMessage(ctx, db.CreateMessageParams{
			ConversationID: convID,
			Type:           db.MessageTypeUser,
			LLMData: llm.Message{
				Role:    llm.MessageRoleUser,
				Content: []llm.Content{{Type: llm.ContentTypeText, Text: "new context"}},
			},
		})
		if err != nil {
			t.Fatalf("failed to create new generation message: %v", err)
		}

		newMsgs, err := h.db.ListMessagesForContext(ctx, convID)
		if err != nil {
			t.Fatalf("failed to list new context: %v", err)
		}
		if len(newMsgs) != 2 {
			t.Fatalf("expected system prompt + new user message in context, got %d messages", len(newMsgs))
		}
		for _, msg := range newMsgs {
			if msg.Generation != 2 {
				t.Fatalf("expected only generation 2 messages, got %+v", msg)
			}
		}
	})
}

// TestStartNewGenerationPreservesSlug verifies that starting a new generation
// does NOT cause the conversation's slug to be regenerated/overwritten.
// The slug is part of the conversation's identity (URL etc.) and should be
// stable across compaction.
func TestStartNewGenerationPreservesSlug(t *testing.T) {
	h := NewTestHarness(t)
	h.NewConversation("first message", "")
	h.WaitResponse()
	ctx := context.Background()
	convID := h.convID

	// Pin a known slug so we can detect any overwrite. Real first-message
	// flow generates one asynchronously, but we want a deterministic value.
	pinned := "pinned-slug"
	if _, err := h.db.UpdateConversationSlug(ctx, convID, pinned); err != nil {
		t.Fatalf("failed to set slug: %v", err)
	}

	// Bump generation, as the UI "compact" / "new generation" button does.
	if _, err := h.server.startNewGeneration(ctx, convID); err != nil {
		t.Fatalf("startNewGeneration: %v", err)
	}

	// Send a message after the generation bump. The handler will see this
	// as a "first message" (hasConversationEvents was cleared by ResetLoop)
	// and kick off async slug generation. The slug must not change.
	h.Chat("new gen first message")
	h.WaitResponse()

	// Poll briefly to give the async slug goroutine a chance to (incorrectly)
	// overwrite the slug. The slug-generation goroutine has a 15s timeout and
	// runs asynchronously; polling is the same pattern used elsewhere here.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fresh, err := h.db.GetConversationByID(ctx, convID)
		if err != nil {
			t.Fatalf("GetConversationByID: %v", err)
		}
		if fresh.Slug != nil && *fresh.Slug != pinned {
			t.Fatalf("slug after new generation = %q, want %q (slug must be preserved across generations)", *fresh.Slug, pinned)
		}
		time.Sleep(20 * time.Millisecond)
	}

	fresh, err := h.db.GetConversationByID(ctx, convID)
	if err != nil {
		t.Fatalf("GetConversationByID: %v", err)
	}
	if fresh.Slug == nil || *fresh.Slug != pinned {
		got := "<nil>"
		if fresh.Slug != nil {
			got = *fresh.Slug
		}
		t.Errorf("slug after new generation = %q, want %q", got, pinned)
	}
}

func TestChatDuringDistillationQueuesEvenWithoutClientQueueFlag(t *testing.T) {
	h := NewTestHarness(t)
	h.NewConversation("before distill", "")
	h.WaitResponse()
	ctx := context.Background()

	manager, err := h.server.getOrCreateConversationManager(ctx, h.convID, "")
	if err != nil {
		t.Fatalf("failed to get manager: %v", err)
	}
	manager.SetDistilling(true)
	defer manager.SetDistilling(false)

	body, err := json.Marshal(ChatRequest{Message: "first message after distill click", Model: "predictable"})
	if err != nil {
		t.Fatalf("marshal chat request: %v", err)
	}
	req := httptest.NewRequest("POST", "/api/conversation/"+h.convID+"/chat", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.server.handleChatConversation(w, req, h.convID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "queued") {
		t.Fatalf("expected queued response, got %s", w.Body.String())
	}

	messages, err := h.db.ListMessages(ctx, h.convID)
	if err != nil {
		t.Fatalf("failed to list messages: %v", err)
	}
	var queued *generated.Message
	for i := range messages {
		msg := messages[i]
		if msg.Type != string(db.MessageTypeUser) || msg.UserData == nil {
			continue
		}
		var userData map[string]bool
		if err := json.Unmarshal([]byte(*msg.UserData), &userData); err != nil {
			continue
		}
		if userData["queued"] {
			queued = &msg
		}
	}
	if queued == nil {
		t.Fatal("expected non-queue chat during distillation to be recorded as queued")
	}
	if !queued.ExcludedFromContext {
		t.Fatal("expected queued message excluded from context until distillation finishes")
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

// TestDistillNewGenerationResetsContextWindow verifies that after a
// distill-into-new-generation, the reported context window size is calculated
// only from the new generation. Otherwise the token bar would continue to
// display the previous generation's (much larger) usage until the next
// message round-trip.
func TestDistillNewGenerationResetsContextWindow(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := NewTestHarness(t)
		defer stopActiveConversationLoops(h.server)

		// Build up some context in the source conversation.
		h.NewConversation("echo hello world", "")
		h.WaitResponse()
		synctest.Wait()
		h.Chat("echo another message")
		h.WaitResponse()
		synctest.Wait()
		sourceConvID := h.convID

		beforeSize := h.GetContextWindowSize()
		if beforeSize == 0 {
			t.Fatal("expected non-zero context window before distill")
		}

		// Distill into a new generation of the same conversation.
		reqBody := DistillNewGenerationRequest{
			SourceConversationID: sourceConvID,
			Model:                "predictable",
		}
		body, _ := json.Marshal(reqBody)
		req := httptest.NewRequest("POST", "/api/conversations/distill-new-generation", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.server.handleDistillNewGeneration(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("expected status 201, got %d: %s", w.Code, w.Body.String())
		}

		waitForConversationDistillingToClear(t, h.server, sourceConvID)

		// The distilled message itself is recorded with empty usage, so the
		// context window for the new generation should be 0 — not the prior
		// generation's value.
		afterSize := h.GetContextWindowSize()
		if afterSize != 0 {
			t.Errorf("context window after distill-new-generation = %d, want 0 (prior gen=%d)", afterSize, beforeSize)
		}
	})
}
