package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
)

func textItem(seq int64, role llm.MessageRole, text string) contextItem {
	typ := string(db.MessageTypeAgent)
	if role == llm.MessageRoleUser {
		typ = string(db.MessageTypeUser)
	}
	return contextItem{from: seq, to: seq, source: &generated.Message{SequenceID: seq, Type: typ}, message: llm.Message{Role: role, Content: []llm.Content{{Type: llm.ContentTypeText, Text: text}}}}
}

func TestApplyInPlaceCompaction(t *testing.T) {
	toolUse := contextItem{from: 3, to: 3, source: &generated.Message{SequenceID: 3}, message: llm.Message{Role: llm.MessageRoleAssistant, Content: []llm.Content{{Type: llm.ContentTypeToolUse, ID: "t1", ToolName: "bash"}}}}
	toolResult := contextItem{from: 4, to: 4, source: &generated.Message{SequenceID: 4}, message: llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{{
		Type: llm.ContentTypeToolResult, ToolUseID: "t1", ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: "lots of output"}},
	}}}}
	base := []contextItem{
		textItem(1, llm.MessageRoleUser, "hi"),
		textItem(2, llm.MessageRoleAssistant, "hello"),
		toolUse,
		toolResult,
		textItem(6, llm.MessageRoleAssistant, "done"),
	}
	squish := func(from, to int64, summary string) db.InPlaceCompaction {
		return db.InPlaceCompaction{Squishes: []db.CompactionSquish{{FromSequenceID: from, ToSequenceID: to, Summary: summary}}}
	}
	spans := func(items []contextItem) string {
		var s []string
		for _, it := range items {
			s = append(s, strconv.FormatInt(it.from, 10)+"-"+strconv.FormatInt(it.to, 10))
		}
		return strings.Join(s, " ")
	}

	got, err := applyInPlaceCompaction(base, squish(2, 4, "ran bash"), "c1", 7)
	if err != nil {
		t.Fatal(err)
	}
	if spans(got) != "1-1 2-4 6-6" {
		t.Fatalf("spans = %s", spans(got))
	}
	if s := got[1]; s.source != nil || s.message.Role != llm.MessageRoleUser || s.noteID != "s7.0" || s.message.Content[0].Text != compactionNoteText("c1", 2, 4, "ran bash") {
		t.Fatalf("summary item = %+v", s)
	}
	if spans(base) != "1-1 2-2 3-3 4-4 6-6" {
		t.Fatal("input modified")
	}

	// A later squish may enclose an earlier summary but not cut into it.
	if again, err := applyInPlaceCompaction(got, squish(1, 6, "all"), "c1", 8); err != nil || spans(again) != "1-6" {
		t.Fatalf("enclosing squish = %s, %v", spans(again), err)
	}
	if _, err := applyInPlaceCompaction(got, squish(3, 6, "x"), "c1", 8); err == nil {
		t.Fatal("squish cutting into a summary should fail")
	}

	for name, c := range map[string]db.InPlaceCompaction{
		"unknown start":       squish(5, 6, "x"),
		"reversed":            squish(6, 1, "x"),
		"ends with tool use":  squish(1, 3, "x"),
		"starts with result":  squish(4, 6, "x"),
		"trim without result": {Trims: []db.CompactionTrim{{SequenceID: 2, ToolUseID: "t1"}}},
		"trim other result":   {Trims: []db.CompactionTrim{{SequenceID: 4, ToolUseID: "t2"}}},
		"trim unknown":        {Trims: []db.CompactionTrim{{SequenceID: 5, ToolUseID: "t1"}}},
	} {
		if _, err := applyInPlaceCompaction(base, c, "c1", 7); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}

	trimmed, err := applyInPlaceCompaction(base, db.InPlaceCompaction{Trims: []db.CompactionTrim{{SequenceID: 4, ToolUseID: "t1"}}}, "c1", 7)
	if err != nil {
		t.Fatal(err)
	}
	if r := trimmed[3].message.Content[0]; r.ToolUseID != "t1" || len(r.ToolResult) != 1 || r.ToolResult[0].Text != "[Tool output compacted: conversation_id c1 sequence_id 4]" {
		t.Fatalf("trimmed result = %+v", r)
	}
	if base[3].message.Content[0].ToolResult[0].Text != "lots of output" {
		t.Fatal("trim modified input")
	}
}

// TestCompactDebugEndToEnd drives /compact-debug through the chat endpoint and
// checks the next LLM request sees the compacted history while the original
// rows stay intact.
func TestCompactDebugEndToEnd(t *testing.T) {
	t.Parallel()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ps := predictable.NewService()
	srv := NewServer(database, &twoModelLLMManager{service: ps},
		claudetool.ToolSetConfig{EnableBrowser: false},
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})),
		false, "model-a", "")
	srv.hooksDir = t.TempDir()
	// The listing leaves out the recent part; keep it to the last message.
	srv.keepRecentTokensOverride = 1
	ctx := t.Context()

	modelA := "model-a"
	conv, err := database.CreateConversation(ctx, nil, true, nil, &modelA, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conv.ConversationID

	endTurns := func() int {
		n := 0
		for _, m := range listMessages(t, database, id) {
			if m.Type == string(db.MessageTypeAgent) && m.LlmData != nil && strings.Contains(*m.LlmData, `"EndOfTurn":true`) {
				n++
			}
		}
		return n
	}
	chat := func(msg string, wantCode int) {
		t.Helper()
		if w := postChat(t, srv, id, msg); w.Code != wantCode {
			t.Fatalf("%q: got %d, want %d: %s", msg, w.Code, wantCode, w.Body.String())
		}
	}
	turn := func(msg string) {
		t.Helper()
		before := endTurns()
		chat(msg, http.StatusAccepted)
		waitFor(t, 10*time.Second, func() bool { return endTurns() > before })
	}

	turn("hello")
	turn("bash: echo compact-me-please")

	seqOf := func(typ db.MessageType, contains string) int64 {
		t.Helper()
		for _, m := range listMessages(t, database, id) {
			if m.Type == string(typ) && m.LlmData != nil && strings.Contains(*m.LlmData, contains) {
				return m.SequenceID
			}
		}
		t.Fatalf("no %s message containing %q", typ, contains)
		return 0
	}
	helloSeq := seqOf(db.MessageTypeUser, `"hello"`)
	hiSeq := seqOf(db.MessageTypeAgent, "Well, hi there!")
	toolUseSeq := seqOf(db.MessageTypeAgent, `"ToolName":"bash"`)
	toolResultSeq := seqOf(db.MessageTypeUser, "compact-me-please\\n")

	// Invalid requests are rejected and record nothing.
	chat("/compact-debug squish "+strconv.FormatInt(toolUseSeq, 10)+"-"+strconv.FormatInt(toolUseSeq, 10)+" x", http.StatusBadRequest)
	chat("/compact-debug trim nope", http.StatusBadRequest)
	chat("/compact-debug trim "+strconv.FormatInt(helloSeq, 10), http.StatusBadRequest) // no tool output
	chat("/compact-debug squish nope", http.StatusBadRequest)

	// Trim takes an id from the index: the call's.
	chat("/compact-debug trim "+strconv.FormatInt(toolUseSeq, 10), http.StatusAccepted)
	chat("/compact-debug squish "+strconv.FormatInt(helloSeq, 10)+"-"+strconv.FormatInt(hiSeq, 10)+" The user  said hello.", http.StatusAccepted)

	var records []db.InPlaceCompaction
	for _, m := range listMessages(t, database, id) {
		if m.Type == string(db.MessageTypeInPlaceCompaction) {
			var c db.InPlaceCompaction
			if err := json.Unmarshal([]byte(*m.UserData), &c); err != nil {
				t.Fatal(err)
			}
			records = append(records, c)
		}
	}
	if len(records) != 2 || records[1].Squishes[0].Summary != "The user  said hello." {
		t.Fatalf("records = %+v", records)
	}

	// The listing reflects the compacted view.
	chat("/compact-debug", http.StatusAccepted)
	warnings, err := database.ListMessagesByType(ctx, id, db.MessageTypeWarning)
	if err != nil || len(warnings) == 0 {
		t.Fatalf("no listing warning: %v", err)
	}
	var listingData struct{ Text string }
	if err := json.Unmarshal([]byte(*warnings[len(warnings)-1].UserData), &listingData); err != nil {
		t.Fatal(err)
	}
	listing := listingData.Text
	if want := `note "The user said hello."`; !strings.Contains(listing, want) || !strings.Contains(listing, `bash "echo compact-me-please" → trimmed`) {
		t.Fatalf("listing %s missing %q", listing, want)
	}

	turn("hello")
	req := ps.GetLastRequest()
	var dump strings.Builder
	for _, m := range req.Messages {
		b, _ := json.Marshal(m)
		dump.Write(b)
		dump.WriteString("\n")
	}
	got := dump.String()
	first := req.Messages[0]
	if first.Role != llm.MessageRoleUser || first.Content[0].Text != compactionNoteText(id, helloSeq, hiSeq, "The user  said hello.") {
		t.Fatalf("first message is not the summary:\n%s", got)
	}
	if strings.Contains(got, "Well, hi there!") || strings.Contains(got, "compact-me-please\\n") {
		t.Fatalf("compacted content leaked into request:\n%s", got)
	}
	if !strings.Contains(got, trimmedToolOutputText(id, toolResultSeq)) || !strings.Contains(got, `"ToolName":"bash"`) {
		t.Fatalf("expected trimmed tool result and intact tool use:\n%s", got)
	}

	// The original rows are untouched.
	var original generated.Message
	for _, m := range listMessages(t, database, id) {
		if m.SequenceID == toolResultSeq {
			original = m
		}
	}
	if !strings.Contains(*original.LlmData, "compact-me-please\\n") {
		t.Fatal("tool result row was modified")
	}
}

// TestBtwFrozenReferenceUsesInPlaceCompactedView: a /btw reader sees the
// parent's compacted view, including only compaction records up to the
// frozen pointer.
func TestBtwFrozenReferenceUsesInPlaceCompactedView(t *testing.T) {
	_, database, _ := newTestServer(t)
	ctx := t.Context()
	parent, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	create := func(params db.CreateMessageParams) int64 {
		t.Helper()
		params.ConversationID = parent.ConversationID
		row, err := database.CreateMessage(ctx, params)
		if err != nil {
			t.Fatal(err)
		}
		return row.SequenceID
	}
	squish := func(from, to int64, summary string) db.CreateMessageParams {
		return db.CreateMessageParams{Type: db.MessageTypeInPlaceCompaction, UserData: db.InPlaceCompaction{
			Squishes: []db.CompactionSquish{{FromSequenceID: from, ToSequenceID: to, Summary: summary}},
		}}
	}
	create(db.CreateMessageParams{Type: db.MessageTypeSystem, LLMData: llm.UserStringMessage("PARENT_SYSTEM")})
	old := create(db.CreateMessageParams{Type: db.MessageTypeUser, LLMData: llm.UserStringMessage("OLD_QUESTION")})
	reply := create(db.CreateMessageParams{Type: db.MessageTypeAgent, LLMData: llm.Message{Role: llm.MessageRoleAssistant, Content: llm.TextContent("OLD_REPLY")}})
	create(squish(old, reply, "SQUISHED_OLD"))
	kept := create(db.CreateMessageParams{Type: db.MessageTypeUser, LLMData: llm.UserStringMessage("KEPT_QUESTION")})
	pointer := db.BtwParentPointer{Generation: parent.CurrentGeneration, SequenceID: kept}
	// After the pointer: not part of the frozen view.
	create(squish(old, kept, "TOO_LATE"))

	rows, err := database.ListFrozenParentMessages(ctx, parent.ConversationID, pointer)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := formatBtwFrozenReference(slog.Default(), rows, btwReaderParentHistoryLimit)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PARENT_SYSTEM", "SQUISHED_OLD", "KEPT_QUESTION"} {
		if !strings.Contains(frozen, want) {
			t.Errorf("frozen reference missing %q:\n%s", want, frozen)
		}
	}
	for _, unwanted := range []string{"OLD_QUESTION", "OLD_REPLY", "TOO_LATE"} {
		if strings.Contains(frozen, unwanted) {
			t.Errorf("frozen reference contains %q:\n%s", unwanted, frozen)
		}
	}
}
