package server

import (
	"strings"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

// Dialogue is found however many tool rows bury it: a long agentic turn can
// have hundreds of tool calls between the request and the reply.
func TestLiveContextDigsPastToolRows(t *testing.T) {
	t.Parallel()
	e := newLiveMessageEnv(t)
	conv, err := e.database.CreateConversation(t.Context(), nil, false, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conv.ConversationID
	// Collected and written in one transaction: a commit per row made
	// inserting the ~2000 rows most of this test's time.
	var rows []db.CreateMessageParams
	add := func(typ db.MessageType, content llm.Content, final bool) {
		role := llm.MessageRoleUser
		if typ == db.MessageTypeAgent {
			role = llm.MessageRoleAssistant
		}
		rows = append(rows, db.CreateMessageParams{ConversationID: id, Type: typ, LLMData: llm.Message{Role: role, Content: []llm.Content{content}, EndOfTurn: final}})
	}
	for i := range 8 {
		add(db.MessageTypeUser, llm.StringContent("request "+string(rune('a'+i))), false)
		for range 120 {
			add(db.MessageTypeAgent, llm.Content{Type: llm.ContentTypeToolUse, ID: "t", ToolName: "bash"}, false)
			add(db.MessageTypeUser, llm.Content{Type: llm.ContentTypeToolResult, ToolUseID: "t", ToolResult: []llm.Content{llm.StringContent("noise")}}, false)
		}
		add(db.MessageTypeAgent, llm.StringContent("reply "+string(rune('a'+i))), true)
	}
	if _, err := e.database.CreateMessages(t.Context(), rows); err != nil {
		t.Fatal(err)
	}
	items, lastSeq, err := e.s.liveContext(t.Context(), id, false)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, item := range items {
		got = append(got, item.Content[0].Text)
	}
	if want := "request f|reply f|request g|reply g|request h|reply h"; strings.Join(got, "|") != want {
		t.Errorf("context = %q, want %q", strings.Join(got, "|"), want)
	}
	if lastSeq != 8*242 {
		t.Errorf("lastSeq = %d, want the last row", lastSeq)
	}

	rows = nil
	add(db.MessageTypeUser, llm.StringContent("Compare the builds."), false)
	add(db.MessageTypeAgent, llm.StringContent("The control passed too."), false)
	add(db.MessageTypeAgent, llm.Content{Type: llm.ContentTypeThinking, Thinking: "PRIVATE"}, false)
	if _, err := e.database.CreateMessages(t.Context(), rows); err != nil {
		t.Fatal(err)
	}
	items, _, err = e.s.liveContext(t.Context(), id, true)
	if err != nil {
		t.Fatal(err)
	}
	expectEqual(t, "latest public note on joining", items[len(items)-1].Content[0].Text, "The control passed too.")
	rows = nil
	add(db.MessageTypeUser, llm.StringContent("Try the union case."), false)
	if _, err := e.database.CreateMessages(t.Context(), rows); err != nil {
		t.Fatal(err)
	}
	items, _, err = e.s.liveContext(t.Context(), id, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Content[0].Text == "The control passed too." {
			t.Fatal("replayed progress from before the new request")
		}
	}
}
