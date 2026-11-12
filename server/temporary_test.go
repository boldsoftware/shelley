package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// temporaryMessage records a message with mark; it returns its sequence id.
func (f *archiveStoreFixture) temporaryMessage(t *testing.T, id string, typ db.MessageType, mark db.TemporaryMark, data any, s string) int64 {
	t.Helper()
	role := llm.MessageRoleUser
	if typ == db.MessageTypeAgent {
		role = llm.MessageRoleAssistant
	}
	m, err := f.archiverDB.CreateMessage(t.Context(), db.CreateMessageParams{
		ConversationID: id, Type: typ, LLMData: llm.Message{Role: role, Content: []llm.Content{text(s)}},
		UserData: data, Temporary: mark,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m.SequenceID
}

func storedTexts(t *testing.T, msgs []generated.ArchiveStoreMessage) []string {
	t.Helper()
	var out []string
	for _, m := range msgs {
		var lm llm.Message
		if err := json.Unmarshal([]byte(*m.LlmData), &lm); err != nil {
			t.Fatal(err)
		}
		out = append(out, messageText(lm))
	}
	return out
}

func temporarySequenceIDs(t *testing.T, database *db.DB, id string) []int64 {
	t.Helper()
	ids, err := database.QueriesRes(t.Context(), func(q *generated.Queries) ([]int64, error) {
		return q.ListTemporarySequenceIDs(t.Context(), id)
	})
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

// A temporary message opens a run: it and everything after it stay off the
// archive until the user types a message that isn't temporary. A temporary
// notice opens a run mid-turn. The push cursor passes the temporary
// messages, so pushing is incremental and idempotent across the gaps. A
// fork's copy of a temporary message, and an in-place compaction summarizing
// one, are temporary.
func TestArchiverTemporaryMessages(t *testing.T) {
	t.Parallel()
	f := newArchiveStoreFixture(t)
	ctx := t.Context()
	f.pushOn(t)
	id := f.conversation(t, db.ConversationOptions{})
	in := db.TemporaryInherit
	f.temporaryMessage(t, id, db.MessageTypeUser, db.TemporaryEnd, nil, "one")
	f.temporaryMessage(t, id, db.MessageTypeAgent, in, nil, "reply one")
	secret := f.temporaryMessage(t, id, db.MessageTypeUser, db.TemporaryStart, nil, "secret")
	f.temporaryMessage(t, id, db.MessageTypeAgent, in, nil, "secret reply")
	f.push(t)
	// Notices and queued messages of the turn join the run.
	f.temporaryMessage(t, id, db.MessageTypeUser, in, map[string]string{"sender_relationship": "subagent"}, "secret notice")
	f.temporaryMessage(t, id, db.MessageTypeAgent, in, nil, "secret notice reply")
	f.push(t)
	f.temporaryMessage(t, id, db.MessageTypeUser, db.TemporaryEnd, nil, "two")
	f.temporaryMessage(t, id, db.MessageTypeAgent, in, nil, "reply two")
	// A late notice of temporary work, mid-turn, opens a run again.
	f.temporaryMessage(t, id, db.MessageTypeUser, db.TemporaryStart, nil, "late secret")
	last := f.temporaryMessage(t, id, db.MessageTypeAgent, in, nil, "late secret reply")
	f.push(t)
	if got := f.push(t); len(got) != 0 {
		t.Fatalf("second push sent %v", got)
	}
	want := []string{"one", "reply one", "two", "reply two"}
	if got := storedTexts(t, f.stored(t, id)); !slices.Equal(got, want) {
		t.Fatalf("stored %q, want %q", got, want)
	}
	have, err := f.storeDB.QueriesRes(ctx, func(q *generated.Queries) (int64, error) {
		return q.GetArchiveStoreHaveSeq(ctx, generated.GetArchiveStoreHaveSeqParams{SourceVm: "alice-vm", ConversationID: id})
	})
	if err != nil || have != last {
		t.Fatalf("store has through %d (%v), want %d", have, err, last)
	}

	// Copies and summaries of temporary messages are temporary.
	fork, err := f.archiverDB.ForkConversation(ctx, id, last)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(temporarySequenceIDs(t, f.archiverDB, fork.ConversationID)); got != 6 {
		t.Fatalf("fork has %d temporary messages, want 6", got)
	}
	f.temporaryMessage(t, id, db.MessageTypeUser, db.TemporaryEnd, nil, "three")
	for _, squish := range []struct {
		from, to  int64
		temporary bool
	}{{1, 2, false}, {1, secret, true}} {
		m, err := f.archiverDB.CreateMessage(ctx, db.CreateMessageParams{
			ConversationID: id, Type: db.MessageTypeInPlaceCompaction,
			UserData: db.InPlaceCompaction{Squishes: []db.CompactionSquish{{FromSequenceID: squish.from, ToSequenceID: squish.to, Summary: "s"}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := slices.Contains(temporarySequenceIDs(t, f.archiverDB, id), m.SequenceID); got != squish.temporary {
			t.Fatalf("squish %d-%d temporary = %v", squish.from, squish.to, got)
		}
	}
}

// A temporary turn: the user's message, the agent's tool calls and replies
// are temporary, and its commands have SHELLEY_TEMPORARY=1; the next typed
// message ends it. The UI learns the temporary messages' ranges.
func TestTemporaryTurn(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	conv, err := database.CreateConversation(t.Context(), nil, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conv.ConversationID
	send := func(message string, temporary bool) {
		t.Helper()
		w := postBtwChat(t, server, id, ChatRequest{Message: message, Model: "predictable", Temporary: temporary})
		if w.Code != http.StatusAccepted {
			t.Fatalf("chat: %d %s", w.Code, w.Body)
		}
		waitForIdle(t, server, id)
	}
	send("bash: echo temp=$SHELLEY_TEMPORARY", true)
	before := len(listMessages(t, database, id))
	send("bash: echo temp=$SHELLEY_TEMPORARY.", false)

	var temporary []string
	for _, m := range listMessages(t, database, id) {
		lm := storedLLMMessage(t, &m)
		got := slices.Contains(temporarySequenceIDs(t, database, id), m.SequenceID)
		raw, _ := json.Marshal(lm)
		if strings.Contains(string(raw), "temp=1") {
			temporary = append(temporary, m.Type)
		}
		if want := m.SequenceID > 1 && int(m.SequenceID) <= before; got != want && m.Type != string(db.MessageTypeSystem) {
			t.Errorf("message %d (%s %.40s) temporary = %v", m.SequenceID, m.Type, raw, got)
		}
	}
	// The temporary turn's command saw SHELLEY_TEMPORARY; the next one didn't.
	if !slices.Contains(temporary, string(db.MessageTypeTool)) && !slices.Contains(temporary, string(db.MessageTypeUser)) {
		t.Errorf("no tool result with temp=1 among %v", temporary)
	}
	for _, m := range listMessages(t, database, id)[before:] {
		if raw, _ := json.Marshal(storedLLMMessage(t, &m)); strings.Contains(string(raw), "temp=1.") {
			t.Errorf("command of a normal turn saw SHELLEY_TEMPORARY")
		}
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/conversation/"+id+"/temporary-messages", nil)
	r.SetPathValue("id", id)
	server.handleTemporaryMessages(w, r)
	var got TemporaryMessages
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	ids := temporarySequenceIDs(t, database, id)
	if len(got.Ranges) != 1 || got.Ranges[0] != [2]int64{ids[0], ids[len(ids)-1]} {
		t.Fatalf("ranges %v for %v", got.Ranges, ids)
	}
}

// A conversation a temporary message creates is temporary, and isn't
// archived.
func TestTemporaryNewConversation(t *testing.T) {
	t.Parallel()
	f := newArchiveStoreFixture(t)
	ctx := t.Context()
	f.pushOn(t)
	body, _ := json.Marshal(NewConversationRequest{ChatRequest: ChatRequest{Message: "echo: hi", Model: "predictable", Temporary: true}})
	w := httptest.NewRecorder()
	f.archiver.handleNewConversation(w, httptest.NewRequest("POST", "/api/conversations/new", strings.NewReader(string(body))))
	if w.Code != http.StatusCreated && w.Code != http.StatusAccepted && w.Code != http.StatusOK {
		t.Fatalf("new conversation: %d %s", w.Code, w.Body)
	}
	var created struct {
		ConversationID string `json:"conversation_id"`
	}
	json.Unmarshal(w.Body.Bytes(), &created)
	conv, err := f.archiverDB.GetConversationByID(ctx, created.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if !db.ParseConversationOptions(conv.ConversationOptions).Temporary {
		t.Fatalf("conversation of a temporary message is not temporary: %s", conv.ConversationOptions)
	}
	waitForIdle(t, f.archiver, created.ConversationID)
	f.push(t)
	if msgs := f.stored(t, created.ConversationID); len(msgs) != 0 {
		t.Fatalf("temporary conversation archived %d messages", len(msgs))
	}
}

// A subagent a temporary turn starts is temporary, and isn't archived; its
// report to its parent is temporary too.
func TestTemporaryPropagation(t *testing.T) {
	t.Parallel()
	f := newArchiveStoreFixture(t)
	ctx := t.Context()
	f.pushOn(t)
	held := newHeldLLMService()
	f.archiver.llmManager = &testLLMManager{service: held}
	t.Cleanup(func() { stopActiveConversationLoops(f.archiver) })

	slug := "assistant"
	parent, err := f.archiverDB.CreateConversation(ctx, &slug, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	prompt := "subagent: scout message_parent_final: found it"
	if w := postBtwChat(t, f.archiver, parent.ConversationID, ChatRequest{Message: prompt, Model: "predictable", Temporary: true}); w.Code != http.StatusAccepted {
		t.Fatalf("chat: %d %s", w.Code, w.Body)
	}
	held.waitCall(t, prompt).Release()
	held.waitCall(t, parentMessage(parent, "message_parent_final: found it")).Release()
	report := "found it"
	children, err := f.archiverDB.GetSubagents(ctx, parent.ConversationID)
	if err != nil || len(children) != 1 {
		t.Fatalf("subagents %v, %v", children, err)
	}
	child := children[0]
	wrapped := `<subagent_message conversation_id="` + child.ConversationID + `" slug="scout">` + "\n" + report + "\n</subagent_message>"
	// The parent's round after the subagent tool, then the report.
	held.waitCall(t, prompt).Release()
	reported := held.waitCall(t, wrapped)
	if !db.ParseConversationOptions(child.ConversationOptions).Temporary {
		t.Fatalf("subagent of a temporary turn is not temporary: %s", child.ConversationOptions)
	}
	m := userMessageContaining(t, f.archiverDB, parent.ConversationID, report)
	if m == nil || !slices.Contains(temporarySequenceIDs(t, f.archiverDB, parent.ConversationID), m.SequenceID) {
		t.Fatal("the temporary subagent's report is not temporary")
	}
	f.push(t)
	if msgs := f.stored(t, child.ConversationID); len(msgs) != 0 {
		t.Fatalf("temporary subagent archived %d messages", len(msgs))
	}
	if got := storedTexts(t, f.stored(t, parent.ConversationID)); len(got) != 0 {
		t.Fatalf("archived %q of a temporary turn", got)
	}
	reported.Release()
}

// The notice of a background job is temporary when the message that started
// it was (deliverBackgroundJobNotice).
func TestToolUseTemporary(t *testing.T) {
	t.Parallel()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ctx := t.Context()
	conv, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		id   string
		mark db.TemporaryMark
	}{{"toolu_normal", db.TemporaryEnd}, {"toolu_secret", db.TemporaryStart}} {
		if _, err := database.CreateMessage(ctx, db.CreateMessageParams{
			ConversationID: conv.ConversationID, Type: db.MessageTypeUser, Temporary: c.mark,
			LLMData: llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{text("go")}},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := database.CreateMessage(ctx, db.CreateMessageParams{
			ConversationID: conv.ConversationID, Type: db.MessageTypeAgent,
			LLMData: llm.Message{Role: llm.MessageRoleAssistant, Content: []llm.Content{{Type: llm.ContentTypeToolUse, ID: c.id, ToolName: "bash"}}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for id, want := range map[string]bool{"toolu_normal": false, "toolu_secret": true, "toolu_none": false} {
		got, err := database.QueriesRes(ctx, func(q *generated.Queries) (bool, error) {
			return q.ToolUseTemporary(ctx, generated.ToolUseTemporaryParams{ConversationID: conv.ConversationID, ToolUseID: id})
		})
		if err != nil || got != want {
			t.Errorf("%s: temporary = %v (%v), want %v", id, got, err, want)
		}
	}
}
