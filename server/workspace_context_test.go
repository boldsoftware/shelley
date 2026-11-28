package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
)

func TestWorkspaceHistorySurvivesLoopRebuild(t *testing.T) {
	t.Parallel()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	rec := &recordingService{Service: predictable.NewService()}
	models := &twoModelLLMManager{service: rec}
	newServer := func() *Server {
		s := NewServer(database, models, claudetool.ToolSetConfig{}, nil, false, "model-a", "")
		s.hooksDir = t.TempDir()
		t.Cleanup(func() { stopActiveConversationLoops(s) })
		return s
	}
	s := newServer()
	start, excursion, dest := t.TempDir(), t.TempDir(), t.TempDir()
	conv, err := database.CreateConversation(t.Context(), nil, true, &start, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conv.ConversationID
	send := func(text string) {
		t.Helper()
		if w := postChat(t, s, id, text); w.Code != http.StatusAccepted {
			t.Fatalf("chat %q: %d %s", text, w.Code, w.Body.String())
		}
		waitForIdle(t, s, id)
	}

	send("hello")
	excursionTurn := "bash in " + excursion + ": pwd"
	send(excursionTurn)
	manager, err := s.getOrCreateConversationManager(t.Context(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	if manager.Cwd() != start {
		t.Fatalf("one-off command moved the workspace to %q", manager.Cwd())
	}
	if w := postSetCwd(t, s, id, dest); w.Code != http.StatusOK {
		t.Fatalf("move: %d %s", w.Code, w.Body.String())
	}

	assertHistory := func(turn string) {
		t.Helper()
		rec.mu.Lock()
		defer rec.mu.Unlock()
		i := rec.turnIndex(t, turn)
		if system := rec.systems[i]; !strings.Contains(system, "Initial pwd: "+start+"\n") || strings.Contains(system, dest) {
			t.Fatalf("system prompt lost initial directory %q:\n%s", start, system)
		}
		history := rec.requests[i]
		if messageText(history[0]) != "hello" {
			t.Fatalf("first historical message = %q, want hello", messageText(history[0]))
		}
		excursionAt := slices.IndexFunc(history, func(m llm.Message) bool {
			return slices.ContainsFunc(m.Content, func(c llm.Content) bool {
				return c.Type == llm.ContentTypeToolResult && slices.ContainsFunc(c.ToolResult, func(result llm.Content) bool {
					return strings.Contains(result.Text, excursion)
				})
			})
		})
		moveAt := slices.IndexFunc(history, func(m llm.Message) bool {
			return strings.Contains(messageText(m), "[The user changed the working directory from "+start+" to "+dest+".")
		})
		if excursionAt < 0 || moveAt <= excursionAt {
			t.Fatalf("excursion and workspace move out of order: excursion at %d, move at %d", excursionAt, moveAt)
		}
	}
	send("echo: live")
	assertHistory("echo: live")
	manager.ResetLoop()
	send("echo: rebuilt")
	assertHistory("echo: rebuilt")
	if w := postChat(t, s, id, "/model model-b"); w.Code != http.StatusAccepted {
		t.Fatalf("model switch: %d %s", w.Code, w.Body.String())
	}
	send("echo: switched")
	assertHistory("echo: switched")
	if manager.GetModel() != "model-b" {
		t.Fatalf("loop model = %q, want model-b", manager.GetModel())
	}
	stopActiveConversationLoops(s)
	s = newServer()
	send("bash: pwd")
	assertHistory("bash: pwd")
	if !rec.turnMentioning(t, "bash: pwd", dest+"\n") {
		t.Fatalf("relative command after restart did not run in %q", dest)
	}
}

func TestWorkspaceMoveSurvivesInPlaceCompaction(t *testing.T) {
	t.Parallel()
	c := newCompactTestConversation(t, db.ConversationOptions{
		ToolOverrides: map[string]string{claudetool.CompactInPlaceName: "on"},
	})
	start, excursion, dest, userDest := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	if err := c.database.UpdateConversationCwd(t.Context(), c.id, start); err != nil {
		t.Fatal(err)
	}
	c.turn("hello")
	c.turn("bash in " + excursion + ": pwd")
	c.turn("change_dir: " + dest)
	waitForIdle(t, c.srv, c.id)
	if w := postSetCwd(t, c.srv, c.id, userDest); w.Code != http.StatusOK {
		t.Fatalf("move: %d %s", w.Code, w.Body.String())
	}
	manager, err := c.srv.getOrCreateConversationManager(t.Context(), c.id, "")
	if err != nil {
		t.Fatal(err)
	}
	manager.ResetLoop()
	c.turn("echo: AFTER_MOVES")
	waitForIdle(t, c.srv, c.id)
	before := c.seqWith("Well, hi there!")
	agentMove := c.seqWith("Changed working directory to: " + dest)
	userMove := c.seqWith("The user changed the working directory from " + dest + " to " + userDest)
	for _, squish := range []db.CompactionSquish{
		{FromSequenceID: before, ToSequenceID: agentMove, Summary: "first part"},
		{FromSequenceID: userMove, ToSequenceID: userMove, Summary: "user move"},
	} {
		if err := manager.RecordInPlaceCompaction(t.Context(), db.InPlaceCompaction{Squishes: []db.CompactionSquish{squish}}); err == nil || !strings.Contains(err.Error(), "workspace move") {
			t.Fatalf("collapse %+v should preserve the workspace move, got %v", squish, err)
		}
	}
	if err := os.WriteFile(filepath.Join(userDest, "AGENTS.md"), []byte("REFRESH_CURRENT_GUIDANCE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if w := postChat(t, c.srv, c.id, fmt.Sprintf("/compact-debug squish %d-%d greeting", before, before)); w.Code != http.StatusAccepted {
		t.Fatalf("collapse before moves: %d %s", w.Code, w.Body.String())
	}
	c.turn("bash: pwd")
	req := c.ps.GetLastRequest()
	if system := systemText(req); !strings.Contains(system, "Initial pwd: "+start+"\n") ||
		!strings.Contains(system, "REFRESH_CURRENT_GUIDANCE") || strings.Contains(system, "Initial pwd: "+userDest) {
		t.Fatalf("refreshed system prompt lost initial directory %q:\n%s", start, system)
	}
	if first := messageText(req.Messages[0]); first != "hello" {
		t.Fatalf("first historical message = %q, want hello", first)
	}
	position := func(text string) int {
		return slices.IndexFunc(req.Messages, func(m llm.Message) bool { return messagesMention([]llm.Message{m}, text) })
	}
	noteAt, excursionAt := position("greeting"), position(excursion)
	agentMoveAt, userMoveAt := position("Changed working directory to: "+dest), position("to "+userDest+".")
	if noteAt < 0 || excursionAt <= noteAt || agentMoveAt <= excursionAt || userMoveAt <= agentMoveAt {
		t.Fatalf("history out of order: note at %d, excursion at %d, agent move at %d, user move at %d",
			noteAt, excursionAt, agentMoveAt, userMoveAt)
	}
	if position(userDest+"\n") <= userMoveAt {
		t.Fatalf("relative command after compaction did not run in %q", userDest)
	}
}

func TestWorkspaceRefreshKeepsUnknownInitialDirectory(t *testing.T) {
	t.Parallel()
	c := newCompactTestConversation(t, db.ConversationOptions{})
	dir := t.TempDir()
	if err := c.database.UpdateConversationCwd(t.Context(), c.id, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := c.database.CreateMessage(t.Context(), db.CreateMessageParams{
		ConversationID: c.id,
		Type:           db.MessageTypeSystem,
		LLMData: llm.UserStringMessage("You are Shelley, a coding agent. Communicate with brevity. Be persistent and creative.\n\n" +
			"<guidance>\nInitial pwd: /example/path\n</guidance>"),
	}); err != nil {
		t.Fatal(err)
	}
	c.turn("hello")
	waitForIdle(t, c.srv, c.id)
	seq := c.seqWith("Well, hi there!")
	if w := postChat(t, c.srv, c.id, fmt.Sprintf("/compact-debug squish %d-%d greeting", seq, seq)); w.Code != http.StatusAccepted {
		t.Fatalf("collapse: %d %s", w.Code, w.Body.String())
	}
	c.turn("echo: refreshed")
	if system := systemText(c.ps.GetLastRequest()); strings.Contains(system, "Initial pwd: ") {
		t.Fatalf("refresh assigned an initial directory to a conversation with no record of one:\n%s", system)
	}
}
