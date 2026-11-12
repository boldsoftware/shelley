package server

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

func TestCompactionSplitRanges(t *testing.T) {
	items := []contextItem{
		textItem(1, llm.MessageRoleUser, strings.Repeat("before ", 20)),
		textItem(2, llm.MessageRoleAssistant, strings.Repeat("before ", 20)),
		callItem(3, "pending"),
		textItem(4, llm.MessageRoleAssistant, strings.Repeat("after ", 20)),
		textItem(5, llm.MessageRoleAssistant, strings.Repeat("after ", 20)),
		textItem(6, llm.MessageRoleAssistant, "recent"),
	}
	original, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	rows, recent := splitRows(items, recentStart(items, 1, piTokenCounter))
	starts, ends := toolPairBoundaries(items)
	for _, tc := range []struct {
		name     string
		from, to int
		want     []collapsePart
	}{
		{"both sides", 0, 4, []collapsePart{{0, 1, false}, {2, 2, true}, {3, 4, false}}},
		{"exact requested scope", 1, 3, []collapsePart{{1, 1, false}, {2, 2, true}, {3, 3, false}}},
		{"protected suffix", 0, 5, []collapsePart{{0, 1, false}, {2, 2, true}, {3, 4, false}, {5, 5, true}}},
		{"only blocked", 2, 2, []collapsePart{{2, 2, true}}},
		{"no split needed", 3, 4, []collapsePart{{3, 4, false}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := splitCollapseRange(rows, recent, tc.from, tc.to, starts, ends)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
	index, err := compactIndex(items, 0, 1, piTokenCounter)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{`compact {"from":"1","to":"2"}`, `keep {"from":"3","to":"3"}`, `compact {"from":"4","to":"5"}`} {
		if !strings.Contains(index, line) {
			t.Fatalf("index lacks %s:\n%s", line, index)
		}
	}
	in := claudetool.CompactInPlaceInput{Collapse: []claudetool.CompactCollapse{{From: "1", To: "5", Note: "Old whole-range note."}}}
	before, _ := json.Marshal(in)
	c, err := buildCompaction(items, 1, piTokenCounter, in)
	if err == nil || !strings.Contains(err.Error(), "write a fresh note") || len(c.Squishes) != 0 {
		t.Fatalf("old note was not rejected with a split plan: %+v, %v", c, err)
	}
	afterInput, _ := json.Marshal(in)
	if string(before) != string(afterInput) {
		t.Fatal("splitter changed submitted notes or endpoints")
	}
	in.Collapse = []claudetool.CompactCollapse{
		{From: "1", To: "2", Note: "Before."},
		{From: "4", To: "5", Note: "After."},
	}
	c, err = buildCompaction(items, 1, piTokenCounter, in)
	if err != nil || len(c.Squishes) != 2 {
		t.Fatalf("fresh split notes: %+v, %v", c, err)
	}
	after, err := applyInPlaceCompaction(items, c, "c1", 7)
	if err != nil || len(after) != 4 {
		t.Fatalf("apply split notes: %+v, %v", after, err)
	}
	if !reflect.DeepEqual(after[1], items[2]) || !reflect.DeepEqual(after[3], items[5]) {
		t.Fatal("blocked or recent message changed")
	}
	if c.Squishes[0].Summary != "Before." || c.Squishes[1].Summary != "After." {
		t.Fatal("fresh notes changed")
	}
	afterSource, _ := json.Marshal(items)
	if string(original) != string(afterSource) {
		t.Fatal("planning or applying split ranges mutated source history")
	}
}

func TestCompactionSplitAllBlocked(t *testing.T) {
	items := []contextItem{callItem(1, "pending"), callItem(2, "also-pending"), textItem(3, llm.MessageRoleAssistant, "recent")}
	rows, recent := splitRows(items, recentStart(items, 1, piTokenCounter))
	starts, ends := toolPairBoundaries(items)
	parts := splitCollapseRange(rows, recent, 0, len(rows)-1, starts, ends)
	if !reflect.DeepEqual(parts, []collapsePart{{0, 2, true}}) {
		t.Fatalf("adjacent blocked rows were not merged: %+v", parts)
	}
	index, err := compactIndex(items, 0, 1, piTokenCounter)
	if err != nil || !strings.Contains(index, "No older rows can be collapsed") || !strings.Contains(index, "No compactable ranges remain") ||
		strings.Contains(index, "last collapsible row") {
		t.Fatalf("all-blocked index: %s, %v", index, err)
	}
}

func TestCompactionSplitNoteIDs(t *testing.T) {
	items := []contextItem{
		textItem(1, llm.MessageRoleUser, "earlier summary"),
		callItem(2, "pending"),
		textItem(3, llm.MessageRoleAssistant, "later text"),
		textItem(4, llm.MessageRoleAssistant, "recent"),
	}
	items[0].source, items[0].noteID = nil, "s9.0"
	index, err := compactIndex(items, 0, 1, piTokenCounter)
	if err != nil || !strings.Contains(index, `compact {"from":"s9.0","to":"s9.0"}`) || !strings.Contains(index, `keep {"from":"2","to":"2"}`) {
		t.Fatalf("plan lost exact note IDs: %s, %v", index, err)
	}
}

func TestCompactIndexSplitUpdate(t *testing.T) {
	nudge := nudgeItem(6)
	nudge.message.Content[0].Text = "\n" + fmt.Sprintf(recentPartLine, "3", 3, 100) + "\n"
	items := []contextItem{
		textItem(1, llm.MessageRoleUser, "before"),
		textItem(2, llm.MessageRoleAssistant, "before"),
		callItem(3, "pending"),
		textItem(4, llm.MessageRoleAssistant, "after"),
		textItem(5, llm.MessageRoleAssistant, "after"),
		nudge, textItem(7, llm.MessageRoleAssistant, "recent"),
	}
	index, err := compactIndex(items, 0, 1, piTokenCounter)
	if err != nil || !strings.Contains(index, "## Index update") ||
		!strings.Contains(index, `keep {"from":"3","to":"3"}`) ||
		!strings.Contains(index, `compact {"from":"4","to":"5"}`) ||
		strings.Contains(index, `compact {"from":"1"`) {
		t.Fatalf("incremental plan repeats or widens earlier scope: %s, %v", index, err)
	}
}

func TestCompactSelectionSplitRecovery(t *testing.T) {
	c := newCompactTestConversation(t, db.ConversationOptions{})
	items := []contextItem{
		textItem(1, llm.MessageRoleUser, strings.Repeat("before ", 20)),
		callItem(2, "completed"), outputItem(3, "completed", strings.Repeat("KEEP_OUTPUT ", 20)),
		callItem(4, "pending"),
		textItem(5, llm.MessageRoleAssistant, strings.Repeat("after ", 20)),
	}
	params := make([]db.CreateMessageParams, len(items))
	for i, it := range items {
		params[i] = db.CreateMessageParams{ConversationID: c.id, Type: db.MessageTypeAgent, LLMData: it.message}
		if it.message.Role == llm.MessageRoleUser && !isToolResultMessage(it.message) {
			params[i].Type = db.MessageTypeUser
		}
	}
	created, err := c.database.CreateMessages(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	first, completed, blocked, last := created[0].SequenceID, created[1].SequenceID, created[3].SequenceID, created[4].SequenceID
	c.turn(`compact_in_place: {"action":"index"}`)
	out, isErr := c.lastToolOutput()
	if isErr || !strings.Contains(out, fmt.Sprintf(`keep {"from":"%d","to":"%d"}`, blocked, blocked)) {
		t.Fatalf("index lacks split plan: %s", out)
	}
	c.turn(fmt.Sprintf(`compact_in_place: {"action":"compact","trim":["%d"],"collapse":[{"from":"%d","to":"%d","note":"Old whole-range note."}]}`, completed, first, last))
	out, isErr = c.lastToolOutput()
	if !isErr || !strings.Contains(out, "Auto-split plan") || c.records() != 0 {
		t.Fatalf("blocked request partially recorded work: %s", out)
	}
	cm, err := c.srv.getOrCreateConversationManager(t.Context(), c.id, "")
	if err != nil {
		t.Fatal(err)
	}
	history, _, err := cm.loadContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	foundOutput, foundBlocked := false, false
	var beforeBlocked llm.Message
	for _, it := range history {
		if it.from == created[2].SequenceID {
			foundOutput = true
			if !strings.Contains(fmt.Sprint(it.message), "KEEP_OUTPUT") {
				t.Fatal("rejected batch applied its trim")
			}
		}
		if it.from == blocked {
			foundBlocked, beforeBlocked = true, it.message
		}
	}
	if !foundOutput || !foundBlocked {
		t.Fatal("rejected batch lost an output or blocked message")
	}
	c.turn(fmt.Sprintf(`compact_in_place: {"action":"compact","collapse":[{"from":"%d","to":"%d","note":"Before."},{"from":"%d","to":"%d","note":"After."}]}`, first, completed, last, last))
	if out, isErr := c.lastToolOutput(); isErr || c.records() != 1 {
		t.Fatalf("fresh split notes failed: %s", out)
	}
	history, _, err = cm.loadContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range history {
		if it.from == blocked {
			found = true
			if !reflect.DeepEqual(it.message, beforeBlocked) {
				t.Fatal("blocked message changed")
			}
		}
	}
	if !found {
		t.Fatal("blocked message disappeared")
	}
}
