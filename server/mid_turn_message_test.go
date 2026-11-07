package server

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
)

// A message sent while a tool runs reaches the model after the tool's result
// and is recorded there too, so history rebuilt from the database is what the
// model saw: no "not executed" result in place of the real one.
func TestMessageDuringToolFollowsItsResult(t *testing.T) {
	s, database, _ := newTestServer(t)
	rec := &recordingService{Service: predictable.NewService()}
	s.llmManager = &testLLMManager{service: rec}
	conv, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cm, err := s.getOrCreateConversationManager(t.Context(), conv.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	started, release := filepath.Join(dir, "started"), filepath.Join(dir, "release")
	command := fmt.Sprintf("bash: touch %s; while [ ! -e %s ]; do sleep 0.01; done; echo TOOL_DONE", started, release)
	if _, err := cm.AcceptUserMessage(t.Context(), rec, "predictable", llm.UserStringMessage(command)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool { _, err := os.Stat(started); return err == nil })
	if _, err := cm.AcceptUserMessage(t.Context(), rec, "predictable", llm.UserStringMessage("echo: mid")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waitForIdle(t, s, conv.ConversationID)

	// What each place holds after the tool call, in order.
	tail := func(messages []llm.Message) []string {
		var got []string
		for _, m := range messages {
			for _, c := range m.Content {
				switch {
				case c.Type == llm.ContentTypeToolUse:
					got = append(got, "call")
				case c.Type == llm.ContentTypeToolResult:
					got = append(got, "result: "+c.ToolResult[0].Text)
				case len(got) > 0 && c.Type == llm.ContentTypeText:
					got = append(got, "text: "+c.Text)
				}
			}
		}
		return got
	}
	want := fmt.Sprint([]string{"call", "result: TOOL_DONE\n", "text: echo: mid"})
	first3 := func(s []string) string { return fmt.Sprint(s[:min(3, len(s))]) }
	rec.mu.Lock()
	request := rec.requests[1]
	rec.mu.Unlock()
	if got := first3(tail(request)); got != want {
		t.Errorf("request after the tool: %s, want %s", got, want)
	}
	var stored []llm.Message
	for _, row := range listMessages(t, database, conv.ConversationID) {
		if row.Type == string(db.MessageTypeUser) || row.Type == string(db.MessageTypeAgent) {
			stored = append(stored, storedLLMMessage(t, &row))
		}
	}
	if got := first3(tail(stored)); got != want {
		t.Errorf("recorded after the tool: %s, want %s", got, want)
	}
}
