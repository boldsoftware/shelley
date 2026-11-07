package server

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

type senderSourceUserData struct {
	ConversationID string `json:"sender_conversation_id"`
	Slug           string `json:"sender_slug"`
	Relationship   string `json:"sender_relationship"`
	Text           string `json:"Text"`
}

func assertSenderSource(t *testing.T, raw []byte, conversationID, slug, relationship, text string) {
	t.Helper()
	if len(raw) == 0 {
		t.Fatal("user_data is empty; expected sender source")
	}
	var data senderSourceUserData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("decode user_data: %v", err)
	}
	if data.ConversationID != conversationID || data.Slug != slug || data.Relationship != relationship || data.Text != text {
		t.Fatalf("sender source = %#v, want conversation_id=%q slug=%q relationship=%q Text=%q", data, conversationID, slug, relationship, text)
	}
}

func userMessageContaining(t *testing.T, database *db.DB, conversationID, text string) *generated.Message {
	t.Helper()
	var messages []generated.Message
	if err := database.Queries(t.Context(), func(q *generated.Queries) error {
		var err error
		messages, err = q.ListMessages(t.Context(), conversationID)
		return err
	}); err != nil {
		t.Fatalf("list messages: %v", err)
	}
	for i := range messages {
		if messages[i].Type != string(db.MessageTypeUser) || messages[i].LlmData == nil {
			continue
		}
		var message llm.Message
		if err := json.Unmarshal([]byte(*messages[i].LlmData), &message); err != nil {
			continue
		}
		if strings.Contains(messageText(message), text) {
			return &messages[i]
		}
	}
	return nil
}

func storedLLMMessage(t *testing.T, message *generated.Message) llm.Message {
	t.Helper()
	if message == nil || message.LlmData == nil {
		t.Fatal("message llm_data is nil")
	}
	var decoded llm.Message
	if err := json.Unmarshal([]byte(*message.LlmData), &decoded); err != nil {
		t.Fatalf("decode llm_data: %v", err)
	}
	return decoded
}

func storedUserData(t *testing.T, message *generated.Message) []byte {
	t.Helper()
	if message == nil {
		t.Fatal("message not found")
	}
	if message.UserData == nil {
		t.Fatal("message user_data is nil")
	}
	return []byte(*message.UserData)
}

func senderChatRequest(t *testing.T, targetID string, request ChatRequest, trusted bool) *http.Request {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/conversation/"+targetID+"/chat", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	if trusted {
		req = req.WithContext(contextWithLocalCLIRequest(req.Context()))
	}
	return req
}

func TestLocalCLIHandlerMarksRequestsTrusted(t *testing.T) {
	var trusted bool
	handler := localCLIHandler(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		trusted = isLocalCLIRequest(r.Context())
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !trusted {
		t.Fatal("local CLI handler did not mark request trusted")
	}
}

func TestDirectManagedChatProvenanceReachesModelWithoutChangingStoredText(t *testing.T) {
	cases := []struct {
		name         string
		reverse      bool
		relationship string
		tag          string
	}{
		{name: "child to parent", relationship: "subagent", tag: "subagent_message"},
		{name: "parent to child", reverse: true, relationship: "parent", tag: "parent_message"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, database, _ := newTestServer(t)
			held := newHeldLLMService()
			server.llmManager = &testLLMManager{service: held}
			t.Cleanup(func() { stopActiveConversationLoops(server) })

			parentSlug := `parent & "lead"`
			parent, err := database.CreateConversation(t.Context(), &parentSlug, true, nil, strPtr("predictable"), db.ConversationOptions{})
			if err != nil {
				t.Fatal(err)
			}
			childSlug := `backend <worker> & "one"`
			child, err := database.CreateSubagentConversation(t.Context(), childSlug, parent.ConversationID, nil)
			if err != nil {
				t.Fatal(err)
			}

			targetID, senderID, senderSlug := parent.ConversationID, child.ConversationID, childSlug
			if tc.reverse {
				targetID, senderID, senderSlug = child.ConversationID, parent.ConversationID, parentSlug
			}
			rawText := "progress <ready> & \"quoted\" 'apostrophe'\n\tgo test ./server"
			escapedSlug := `backend &lt;worker&gt; &amp; &#34;one&#34;`
			if tc.reverse {
				escapedSlug = `parent &amp; &#34;lead&#34;`
			}
			wrapped := "<" + tc.tag + ` conversation_id="` + senderID + `" slug="` + escapedSlug + `">` +
				"\nprogress &lt;ready&gt; &amp; \"quoted\" 'apostrophe'\n\tgo test ./server\n</" + tc.tag + ">"
			w := httptest.NewRecorder()
			server.handleChatConversation(w, senderChatRequest(t, targetID, ChatRequest{
				Message:              rawText,
				Model:                "predictable",
				SenderConversationID: senderID,
			}, true), targetID)
			if w.Code != http.StatusAccepted {
				t.Fatalf("chat status = %d: %s", w.Code, w.Body.String())
			}

			call := held.waitCall(t, wrapped)
			if !requestHasText(call.request, wrapped) {
				t.Fatalf("model request did not contain wrapped provenance: %#v", call.request.Messages)
			}
			message := userMessageContaining(t, database, targetID, rawText)
			if got := messageText(storedLLMMessage(t, message)); got != rawText {
				t.Fatalf("stored user message = %q, want raw %q", got, rawText)
			}
			assertSenderSource(t, storedUserData(t, message), senderID, senderSlug, tc.relationship, rawText)

			if !tc.reverse {
				results, err := database.SearchConversationsFTS(t.Context(), "progress", false, 10, 0)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, result := range results {
					found = found || result.ConversationID == targetID
				}
				if !found {
					t.Fatalf("provenance message was not searchable: %#v", results)
				}
			}
			releaseAndWaitIdle(t, server, targetID, call)
		})
	}
}

// An ordinary conversation, one without message_user, gives the model exactly
// what the user typed: no wrapper, sequence_id, or escaping, whether the
// message is hydrated from history, sent live, or drained from the queue.
func TestOrdinaryChatReachesModelUnwrapped(t *testing.T) {
	server, database, model := newTestServer(t)
	t.Cleanup(func() { stopActiveConversationLoops(server) })

	slug := "ordinary"
	conversation, err := database.CreateConversation(t.Context(), &slug, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	const hydrated, live, queued = `hydrated <a> & "b"`, `live <text> & "quotes"`, `queued <c> & "d"`
	if _, err := database.CreateMessage(t.Context(), db.CreateMessageParams{
		ConversationID: conversation.ConversationID,
		Type:           db.MessageTypeUser,
		LLMData:        llm.UserStringMessage(hydrated),
	}); err != nil {
		t.Fatal(err)
	}
	send := func(text string, queue bool) {
		t.Helper()
		w := httptest.NewRecorder()
		server.handleChatConversation(w, senderChatRequest(t, conversation.ConversationID, ChatRequest{
			Message: text,
			Model:   "predictable",
			Queue:   queue,
		}, true), conversation.ConversationID)
		if w.Code != http.StatusAccepted {
			t.Fatalf("chat %q status = %d: %s", text, w.Code, w.Body.String())
		}
	}
	// sentUserMessages returns, block by block, the user messages of the
	// model request whose last user message mentions word. Only a block's
	// type and text are kept: cache breakpoints are not the message.
	type block struct {
		Type llm.ContentType
		Text string
	}
	sentUserMessages := func(word string) [][]block {
		t.Helper()
		var got [][]block
		waitFor(t, 5*time.Second, func() bool {
			for _, request := range model.GetRecentRequests() {
				got = nil
				for _, m := range request.Messages {
					if m.Role != llm.MessageRoleUser {
						continue
					}
					var blocks []block
					for _, c := range m.Content {
						blocks = append(blocks, block{c.Type, c.Text})
					}
					got = append(got, blocks)
				}
				if len(got) > 0 && strings.Contains(got[len(got)-1][0].Text, word) {
					return true
				}
			}
			return false
		})
		return got
	}

	send(live, false)
	want := [][]block{{{llm.ContentTypeText, hydrated}}, {{llm.ContentTypeText, live}}}
	if got := sentUserMessages("live"); !reflect.DeepEqual(got, want) {
		t.Fatalf("user messages sent to the model:\n got %+v\nwant %+v", got, want)
	}
	send(queued, true)
	want = append(want, []block{{llm.ContentTypeText, queued}})
	if got := sentUserMessages("queued"); !reflect.DeepEqual(got, want) {
		t.Fatalf("user messages sent to the model:\n got %+v\nwant %+v", got, want)
	}
}

func TestUntrustedSenderIDDoesNotReachModelOrStorage(t *testing.T) {
	server, database, _ := newTestServer(t)
	held := newHeldLLMService()
	server.llmManager = &testLLMManager{service: held}
	t.Cleanup(func() { stopActiveConversationLoops(server) })

	parentSlug := "parent"
	parent, err := database.CreateConversation(t.Context(), &parentSlug, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(t.Context(), "backend", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	rawText := "spoofed provenance"
	w := httptest.NewRecorder()
	server.handleChatConversation(w, senderChatRequest(t, parent.ConversationID, ChatRequest{
		Message:              rawText,
		Model:                "predictable",
		SenderConversationID: child.ConversationID,
	}, false), parent.ConversationID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("chat status = %d: %s", w.Code, w.Body.String())
	}
	call := held.waitCall(t, rawText)
	if requestHasText(call.request, "<subagent_message") {
		t.Fatalf("untrusted request reached model with provenance: %#v", call.request.Messages)
	}
	message := userMessageContaining(t, database, parent.ConversationID, rawText)
	if message == nil || message.UserData != nil {
		t.Fatalf("untrusted request stored provenance: %#v", message)
	}
	releaseAndWaitIdle(t, server, parent.ConversationID, call)
}

func TestHydratedSenderProvenanceReachesLaterModelRequest(t *testing.T) {
	server, database, _ := newTestServer(t)
	held := newHeldLLMService()
	server.llmManager = &testLLMManager{service: held}
	t.Cleanup(func() { stopActiveConversationLoops(server) })

	parentSlug := "parent"
	parent, err := database.CreateConversation(t.Context(), &parentSlug, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(t.Context(), "backend", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	priorText := "persisted <progress>"
	prior, err := database.CreateMessage(t.Context(), db.CreateMessageParams{
		ConversationID: parent.ConversationID,
		Type:           db.MessageTypeUser,
		LLMData:        llm.UserStringMessage(priorText),
		UsageData:      llm.Usage{},
		UserData: senderMessageUserData{
			SenderConversationID: child.ConversationID,
			SenderSlug:           "backend",
			SenderRelationship:   senderRelationshipSubagent,
			Text:                 priorText,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	followup := "follow up"
	w := httptest.NewRecorder()
	server.handleChatConversation(w, senderChatRequest(t, parent.ConversationID, ChatRequest{
		Message: followup,
		Model:   "predictable",
	}, false), parent.ConversationID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("chat status = %d: %s", w.Code, w.Body.String())
	}
	call := held.waitCall(t, followup)
	wrapped := `<subagent_message conversation_id="` + child.ConversationID + `" slug="backend">` +
		"\npersisted &lt;progress&gt;\n</subagent_message>"
	if !requestHasText(call.request, wrapped) {
		t.Fatalf("hydrated model request lost provenance: %#v", call.request.Messages)
	}
	if got := messageText(storedLLMMessage(t, prior)); got != priorText {
		t.Fatalf("hydrated source llm_data = %q, want raw %q", got, priorText)
	}
	releaseAndWaitIdle(t, server, parent.ConversationID, call)
}

func TestQueuedChatCapturesRawSenderProvenance(t *testing.T) {
	server, database, _ := newTestServer(t)
	parentSlug := "parent"
	parent, err := database.CreateConversation(t.Context(), &parentSlug, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(t.Context(), "backend", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := server.getOrCreateConversationManager(t.Context(), parent.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	// Hold the drain, as ApplySettings does, so the message stays queued.
	manager.mu.Lock()
	manager.changingSettings = true
	manager.mu.Unlock()

	rawText := "queued raw <progress>"
	w := httptest.NewRecorder()
	server.handleChatConversation(w, senderChatRequest(t, parent.ConversationID, ChatRequest{
		Message:              rawText,
		Model:                "predictable",
		Queue:                true,
		SenderConversationID: child.ConversationID,
	}, true), parent.ConversationID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("chat status = %d: %s", w.Code, w.Body.String())
	}
	queued, err := database.GetQueuedMessages(t.Context(), parent.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 {
		t.Fatalf("queued messages = %d, want 1", len(queued))
	}
	assertSenderSource(t, queued[0].UserData, child.ConversationID, "backend", "subagent", rawText)
	var stored llm.Message
	if err := json.Unmarshal(queued[0].Llm, &stored); err != nil {
		t.Fatal(err)
	}
	if got := messageText(stored); got != rawText {
		t.Fatalf("queued llm text = %q, want raw %q", got, rawText)
	}
}

func TestQueuedProvenanceWrapsAfterHydrationAndDrain(t *testing.T) {
	server, database, _ := newTestServer(t)
	held := newHeldLLMService()
	server.llmManager = &testLLMManager{service: held}
	t.Cleanup(func() { stopActiveConversationLoops(server) })

	parentSlug := "parent"
	parent, err := database.CreateConversation(t.Context(), &parentSlug, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(t.Context(), "backend", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	rawText := "queued <progress>"
	message := llm.UserStringMessage(rawText)
	llmJSON, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	userData, err := json.Marshal(senderMessageUserData{
		SenderConversationID: child.ConversationID,
		SenderSlug:           "backend",
		SenderRelationship:   senderRelationshipSubagent,
		Text:                 rawText,
	})
	if err != nil {
		t.Fatal(err)
	}
	queued := db.QueuedMessage{ID: "queued-provenance", Llm: llmJSON, Model: "predictable", UserData: userData}
	if _, err := database.AppendQueuedMessage(t.Context(), parent.ConversationID, queued); err != nil {
		t.Fatal(err)
	}

	manager, err := server.getOrCreateConversationManager(t.Context(), parent.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	manager.drainPendingMessages()
	wrapped := `<subagent_message conversation_id="` + child.ConversationID + `" slug="backend">` +
		"\nqueued &lt;progress&gt;\n</subagent_message>"
	call := held.waitCall(t, wrapped)
	stored := userMessageContaining(t, database, parent.ConversationID, rawText)
	if got := messageText(storedLLMMessage(t, stored)); got != rawText {
		t.Fatalf("drained queued message = %q, want raw %q", got, rawText)
	}
	assertSenderSource(t, storedUserData(t, stored), child.ConversationID, "backend", "subagent", rawText)
	releaseAndWaitIdle(t, server, parent.ConversationID, call)
}

func TestTranscriptionSenderProvenanceAppliesOnlyToFinalUserMessage(t *testing.T) {
	server, database, _ := newTestServer(t)
	held := newHeldLLMService()
	server.llmManager = &testLLMManager{service: held}
	server.transcriber = successfulRecordingTranscriber("spoken <progress>")
	t.Cleanup(func() { stopActiveConversationLoops(server) })

	parent, err := database.CreateConversation(t.Context(), strPtr("parent"), true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(t.Context(), "backend", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	mediaPath := transcriptionTestFile(t, "provenance.webm")
	started := make(chan struct{})
	release := make(chan struct{})
	server.mediaRun = func(context.Context, string, ...string) ([]byte, error) {
		close(started)
		<-release
		return []byte(`{"streams":[{"codec_type":"audio"}],"format":{"duration":"2"}}`), nil
	}

	req := senderChatRequest(t, parent.ConversationID, ChatRequest{
		Message:              "/transcription " + mediaPath + "\nPreserve <context>",
		Model:                "predictable",
		SenderConversationID: child.ConversationID,
	}, true)
	req.Header.Set("X-ExeDev-Email", "sender@example.com")
	w := httptest.NewRecorder()
	server.handleChatConversation(w, req, parent.ConversationID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("chat status = %d: %s", w.Code, w.Body.String())
	}
	receipt := queuedTranscriptionReceipt(t, w, database, parent.ConversationID)
	done := transcriptionDone(t, server, receipt.ID)
	<-started

	server.mu.Lock()
	manager := server.activeConversations[parent.ConversationID]
	server.mu.Unlock()
	manager.SetAgentWorking(true)
	close(release)
	<-done

	queued := queuedMessages(t, database, parent.ConversationID)
	if len(queued) != 1 || queued[0].State != db.QueuedMessageStateReady {
		t.Fatalf("ready queue = %#v", queued)
	}
	var final llm.Message
	if err := json.Unmarshal(queued[0].Llm, &final); err != nil {
		t.Fatal(err)
	}
	finalText := messageText(final)
	assertSenderSource(t, queued[0].UserData, child.ConversationID, "backend", "subagent", finalText)

	manager.SetAgentWorking(false)
	<-manager.drainPendingMessages()
	wrapped, err := messageWithSenderProvenance(final, queued[0].UserData)
	if err != nil {
		t.Fatal(err)
	}
	call := held.waitCall(t, strings.TrimSpace(messageText(wrapped)))
	if !requestHasText(call.request, messageText(wrapped)) {
		t.Fatalf("model request lost final transcript provenance: %#v", call.request.Messages)
	}
	for _, message := range call.request.Messages {
		for _, content := range message.Content {
			if content.ToolName == "openai_audio_transcription" || content.Type == llm.ContentTypeToolResult {
				t.Fatalf("synthetic transcription audit reached model: %#v", call.request.Messages)
			}
		}
	}

	rows, err := database.ListMessages(t.Context(), parent.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	var auditRows, finalRows int
	for i := range rows {
		row := &rows[i]
		if row.LlmData == nil {
			continue
		}
		message := storedLLMMessage(t, row)
		isAudit := false
		for _, content := range message.Content {
			isAudit = isAudit || content.ToolName == "openai_audio_transcription" || content.Type == llm.ContentTypeToolResult
		}
		switch {
		case isAudit:
			auditRows++
			if row.UserData != nil || row.UserEmail != nil {
				t.Fatalf("audit row has sender attribution: %#v", row)
			}
		case messageText(message) == finalText:
			finalRows++
			if got := messageText(message); got != finalText || strings.Contains(got, "<subagent_message") {
				t.Fatalf("stored transcript = %q, want raw %q", got, finalText)
			}
			assertSenderSource(t, storedUserData(t, row), child.ConversationID, "backend", "subagent", finalText)
			if row.UserEmail == nil || *row.UserEmail != "sender@example.com" {
				t.Fatalf("transcript user email = %v", row.UserEmail)
			}
		}
	}
	if auditRows != 2 || finalRows != 1 {
		t.Fatalf("transcription rows: audit=%d final=%d", auditRows, finalRows)
	}
	results, err := database.SearchConversationsFTS(t.Context(), "spoken", false, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, result := range results {
		found = found || result.ConversationID == parent.ConversationID
	}
	if !found {
		t.Fatalf("final transcript was not searchable: %#v", results)
	}
	releaseAndWaitIdle(t, server, parent.ConversationID, call)
}

func TestSenderProvenanceRejectsUntrustedUnrelatedAndSpecialChildren(t *testing.T) {
	server, database, _ := newTestServer(t)
	parentSlug := "parent"
	parent, err := database.CreateConversation(t.Context(), &parentSlug, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := database.CreateSubagentConversation(t.Context(), "direct", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := database.CreateConversation(t.Context(), strPtr("other"), true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := database.CreateSubagentConversation(t.Context(), "unrelated", other.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := database.CreateSubagentConversation(t.Context(), "grandchild", direct.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	transcription, err := database.CreateSubagentConversation(t.Context(), "transcription", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateConversationOptions(t.Context(), transcription.ConversationID, db.ConversationOptions{Kind: transcriptionKind}); err != nil {
		t.Fatal(err)
	}
	transcription, err = database.GetConversationByID(t.Context(), transcription.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	btw, err := database.CreateBtwReaderConversation(t.Context(), db.CreateBtwReaderConversationParams{
		ConversationID: "btw-child",
		SlugBase:       "btw",
		ParentID:       parent.ConversationID,
		ParentPointer:  db.BtwParentPointer{Generation: 1, SequenceID: 1},
		SystemMessage:  llm.UserStringMessage("system"),
		UserMessage:    llm.UserStringMessage("question"),
	})
	if err != nil {
		t.Fatal(err)
	}

	trusted := contextWithLocalCLIRequest(context.Background())
	cases := []struct {
		name   string
		ctx    context.Context
		target generated.Conversation
		sender string
	}{
		{name: "untrusted", ctx: context.Background(), target: *parent, sender: direct.ConversationID},
		{name: "self", ctx: trusted, target: *parent, sender: parent.ConversationID},
		{name: "missing", ctx: trusted, target: *parent, sender: "missing"},
		{name: "unrelated", ctx: trusted, target: *parent, sender: unrelated.ConversationID},
		{name: "grandchild to grandparent", ctx: trusted, target: *parent, sender: grandchild.ConversationID},
		{name: "grandparent to grandchild", ctx: trusted, target: *grandchild, sender: parent.ConversationID},
		{name: "transcription child", ctx: trusted, target: *parent, sender: transcription.ConversationID},
		{name: "transcription target", ctx: trusted, target: *transcription, sender: parent.ConversationID},
		{name: "btw child", ctx: trusted, target: *parent, sender: btw.ConversationID},
		{name: "btw target", ctx: trusted, target: *btw, sender: parent.ConversationID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := server.senderUserData(tc.ctx, tc.target, tc.sender)
			if err != nil {
				t.Fatal(err)
			}
			if got != nil {
				t.Fatalf("sender provenance = %#v, want nil", got)
			}
		})
	}
}

func TestQueuedSenderUserDataUpdatesSearchText(t *testing.T) {
	raw := json.RawMessage(`{"sender_conversation_id":"child","sender_slug":"backend","sender_relationship":"subagent","Text":"/transcription old"}`)
	updated, err := queuedUserDataWithMessageText(raw, "spoken progress")
	if err != nil {
		t.Fatal(err)
	}
	assertSenderSource(t, updated, "child", "backend", "subagent", "spoken progress")
}

func TestXMLProvenanceAttributesEscapeNormalizedWhitespace(t *testing.T) {
	got, err := xmlProvenanceOpeningTag("parent_message",
		xml.Attr{Name: xml.Name{Local: "conversation_id"}, Value: "parent\nid"},
		xml.Attr{Name: xml.Name{Local: "slug"}, Value: "slug\tline\r"})
	if err != nil {
		t.Fatal(err)
	}
	want := `<parent_message conversation_id="parent&#xA;id" slug="slug&#x9;line&#xD;">`
	if got != want {
		t.Fatalf("opening tag = %q, want %q", got, want)
	}
}

func TestSenderProvenanceWrapsCurrentTextAcrossContentBlocks(t *testing.T) {
	data, err := json.Marshal(senderMessageUserData{
		SenderConversationID: "child",
		SenderSlug:           "backend",
		SenderRelationship:   senderRelationshipSubagent,
		Text:                 "stale search text",
	})
	if err != nil {
		t.Fatal(err)
	}
	message := llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{
		{Type: llm.ContentTypeText, Text: "first <line>"},
		{Type: llm.ContentTypeText, MediaType: "image/png", Data: "image-data"},
		{Type: llm.ContentTypeText, Text: "second & line"},
	}}
	wrapped, err := messageWithSenderProvenance(message, data)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := wrapped.Content[0].Text, "<subagent_message conversation_id=\"child\" slug=\"backend\">\nfirst &lt;line&gt;"; got != want {
		t.Fatalf("first text block = %q, want %q", got, want)
	}
	if got, want := wrapped.Content[1].Data, "image-data"; got != want || wrapped.Content[1].Text != "" {
		t.Fatalf("image block changed: %#v", wrapped.Content[1])
	}
	if got, want := wrapped.Content[2].Text, "second &amp; line\n</subagent_message>"; got != want {
		t.Fatalf("last text block = %q, want %q", got, want)
	}
	if strings.Contains(messageText(wrapped), "stale search text") {
		t.Fatal("wrapper used FTS duplicate instead of current model text")
	}
}

func TestSenderProvenancePropagatesLookupFailure(t *testing.T) {
	server, database, _ := newTestServer(t)
	parent, err := database.CreateConversation(t.Context(), strPtr("parent"), true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(t.Context(), "backend", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(contextWithLocalCLIRequest(t.Context()))
	cancel()
	if _, err := server.senderUserData(ctx, *parent, child.ConversationID); !errors.Is(err, context.Canceled) {
		t.Fatalf("sender lookup error = %v, want context cancellation", err)
	}
}

func TestUnnamedParentStillHasMessageProvenance(t *testing.T) {
	server, database, _ := newTestServer(t)
	parent, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(t.Context(), "backend", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	source, err := server.senderUserData(contextWithLocalCLIRequest(t.Context()), *child, parent.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if source == nil || source.SenderRelationship != senderRelationshipParent || source.SenderConversationID != parent.ConversationID {
		t.Fatalf("unnamed parent provenance = %#v", source)
	}
	source.Text = "progress"
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	message, err := messageWithSenderProvenance(llm.UserStringMessage(source.Text), raw)
	if err != nil {
		t.Fatal(err)
	}
	want := `<parent_message conversation_id="` + parent.ConversationID + `" slug="">` + "\nprogress\n</parent_message>"
	if got := messageText(message); got != want {
		t.Fatalf("model message = %q, want %q", got, want)
	}
}

func TestSenderProvenanceDoesNotRequireSearchText(t *testing.T) {
	raw := []byte(`{"sender_conversation_id":"parent","sender_slug":"planning","sender_relationship":"parent"}`)
	message, err := messageWithSenderProvenance(llm.UserStringMessage("current text"), raw)
	if err != nil {
		t.Fatal(err)
	}
	want := "<parent_message conversation_id=\"parent\" slug=\"planning\">\ncurrent text\n</parent_message>"
	if got := messageText(message); got != want {
		t.Fatalf("model message = %q, want %q", got, want)
	}
}

func TestXMLProvenanceBodyPreservesFormattingWithoutAllowingMarkup(t *testing.T) {
	text := "first line\n\t\"quoted\" 'value' </parent_message> ]]> & &#xA;"
	got := xmlProvenanceText(text)
	want := "first line\n\t\"quoted\" 'value' &lt;/parent_message&gt; ]]&gt; &amp; &amp;#xA;"
	if got != want {
		t.Fatalf("XML body = %q, want %q", got, want)
	}
	var decoded struct {
		Text string `xml:",chardata"`
	}
	if err := xml.Unmarshal([]byte("<message>"+got+"</message>"), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Text != text {
		t.Fatalf("decoded XML = %q, want %q", decoded.Text, text)
	}
}
