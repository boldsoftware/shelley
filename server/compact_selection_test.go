package server

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

func TestBuildCompactionTrimValidation(t *testing.T) {
	t.Run("mixed outputs", func(t *testing.T) {
		call := callItem(1, "old")
		call.message.Content = append(call.message.Content, llm.Content{Type: llm.ContentTypeToolUse, ID: "new", ToolName: "bash"})
		output := outputItem(2, "old", trimmedToolOutputText("c1", 2))
		output.message.Content = append(output.message.Content, llm.Content{
			Type: llm.ContentTypeToolResult, ToolUseID: "new",
			ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: "new output"}},
		})
		items := []contextItem{call, output, textItem(3, llm.MessageRoleAssistant, "recent")}
		c, err := buildCompaction(items, 1, piTokenCounter, claudetool.CompactInPlaceInput{Trim: []claudetool.IndexID{"1"}})
		if err != nil || len(c.Trims) != 1 || c.Trims[0].ToolUseID != "new" {
			t.Fatalf("mixed trim: %+v, %v", c, err)
		}
		after, err := applyInPlaceCompaction(items, c, "c1", 4)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(after[1].message.Content[0], output.message.Content[0]) ||
			!isTrimmedOutput(after[1].message.Content[1]) {
			t.Fatal("mixed trim did not preserve old output and trim remaining output")
		}
	})
	items := []contextItem{
		callItem(1, "old"), outputItem(2, "old", trimmedToolOutputText("c1", 2)),
		callItem(3, "new"), outputItem(4, "new", "retain this long output until trimmed"),
		textItem(5, llm.MessageRoleAssistant, "a useful conclusion to summarize"),
		callItem(6, "recent"), outputItem(7, "recent", trimmedToolOutputText("c1", 7)),
	}
	for _, tc := range []struct {
		name  string
		trim  []claudetool.IndexID
		count int
		err   string
	}{
		{"already trimmed", []claudetool.IndexID{"1"}, 0, "already trimmed"},
		{"duplicate untrimmed", []claudetool.IndexID{"3", "3"}, 0, "listed twice"},
		{"untrimmed", []claudetool.IndexID{"3"}, 1, ""},
		{"unknown", []claudetool.IndexID{"1", "missing"}, 0, "unknown id"},
		{"no output", []claudetool.IndexID{"5"}, 0, "has no tool output"},
		{"new and no output", []claudetool.IndexID{"3", "5"}, 0, "has no tool output"},
		{"no output and unknown", []claudetool.IndexID{"5", "missing"}, 0, "unknown id"},
		{"protected already trimmed", []claudetool.IndexID{"6", "6"}, 0, "recent part"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := func() []byte {
				var messages []llm.Message
				for _, item := range items {
					messages = append(messages, item.message)
				}
				data, err := json.Marshal(messages)
				if err != nil {
					t.Fatal(err)
				}
				return data
			}
			before := messages()
			c, err := buildCompaction(items, 1, piTokenCounter, claudetool.CompactInPlaceInput{Trim: tc.trim})
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("want %q, got %v", tc.err, err)
				}
			} else {
				if err != nil || len(c.Trims) != tc.count {
					t.Fatalf("got %+v, %v", c, err)
				}
				after, err := applyInPlaceCompaction(items, c, "c1", 8)
				if err != nil {
					t.Fatal(err)
				}
				if !isTrimmedOutput(after[3].message.Content[0]) {
					t.Fatal("output was not trimmed")
				}
			}
			after := messages()
			if string(before) != string(after) {
				t.Fatal("validation mutated input")
			}
		})
	}
	for _, role := range []llm.MessageRole{llm.MessageRoleUser, llm.MessageRoleAssistant} {
		t.Run(fmt.Sprintf("protected text %v", role), func(t *testing.T) {
			items := []contextItem{
				textItem(1, llm.MessageRoleAssistant, "older"),
				textItem(2, role, "recent without tool output"),
			}
			_, err := buildCompaction(items, 1, piTokenCounter, claudetool.CompactInPlaceInput{
				Trim: []claudetool.IndexID{"2", "2"},
			})
			if err == nil || !strings.Contains(err.Error(), "recent part") {
				t.Fatalf("protected output-free row: %v", err)
			}
		})
	}
}

func TestCompactIndexSelectionFlags(t *testing.T) {
	items := []contextItem{
		callItem(1, "lost"),
		textItem(2, llm.MessageRoleAssistant, "middle"),
		outputItem(3, "late", "orphan output"),
		callItem(4, "old"), outputItem(5, "old", trimmedToolOutputText("c1", 5)),
		callItem(6, "new"), outputItem(7, "new", "output"),
		textItem(8, llm.MessageRoleAssistant, "recent"),
	}
	index, err := compactIndex(items, 0, 1, piTokenCounter)
	if err != nil {
		t.Fatal(err)
	}
	for id, flags := range map[int]string{
		1: "-", 2: "start,end", 3: "trim", 4: "start,end", 6: "start,end,trim",
	} {
		pattern := fmt.Sprintf(`(?m)^%d +\S+ +\d+ +%s +`, id, flags)
		if !regexp.MustCompile(pattern).MatchString(index) {
			t.Errorf("missing eligibility %s:\n%s", pattern, index)
		}
	}
	if strings.Contains(index, "\n8 ") || !strings.Contains(index, "last collapsible row is 6") {
		t.Fatalf("index does not protect recent rows:\n%s", index)
	}
}

func TestBuildCompactionSplitPlans(t *testing.T) {
	items := []contextItem{
		textItem(1, llm.MessageRoleAssistant, strings.Repeat("history ", 20)),
		callItem(2, "lost"),
		textItem(3, llm.MessageRoleAssistant, "intervening message"),
		outputItem(4, "late", "orphan output"),
		textItem(5, llm.MessageRoleAssistant, "a useful conclusion"),
		textItem(6, llm.MessageRoleAssistant, "recent"),
	}
	for _, tc := range []struct {
		from, to, hint string
	}{
		{"1", "2", `compact {"from":"1","to":"1"}`},
		{"4", "5", `compact {"from":"5","to":"5"}`},
		{"2", "2", "No compactable ranges remain"},
		{"4", "4", "No compactable ranges remain"},
		{"1", "6", `keep {"from":"6","to":"6"}`},
		{"6", "6", "No compactable ranges remain"},
	} {
		t.Run(tc.from+"-"+tc.to, func(t *testing.T) {
			_, err := buildCompaction(items, 1, piTokenCounter, claudetool.CompactInPlaceInput{
				Collapse: []claudetool.CompactCollapse{{From: claudetool.IndexID(tc.from), To: claudetool.IndexID(tc.to), Note: "Note."}},
			})
			if err == nil || !strings.Contains(err.Error(), tc.hint) {
				t.Fatalf("want %q, got %v", tc.hint, err)
			}
		})
	}
	_, err := buildCompaction(items, 1, piTokenCounter, claudetool.CompactInPlaceInput{
		Collapse: []claudetool.CompactCollapse{{From: "missing", To: "4", Note: "Note."}},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown id") {
		t.Fatalf("unknown ID not rejected: %v", err)
	}
}

func TestCompactSelectionValidation(t *testing.T) {
	c := newCompactTestConversation(t, db.ConversationOptions{})
	c.turn("hello")
	hello := c.seqWith(`"Text":"hello"`)
	c.turn("bash: echo KEEP_THIS_OUTPUT")
	call := c.callSeq(c.rows()[c.seqWith(`KEEP_THIS_OUTPUT\n`)].Content[0].ToolUseID)
	c.turn(`compact_in_place: {"action":"index"}`)
	c.turn(`compact_in_place: {"action":"index"}`)
	if out, _ := c.lastToolOutput(); !strings.Contains(out, "## Index update") {
		t.Fatalf("expected incremental index, got %s", out)
	}
	c.turn(fmt.Sprintf(`compact_in_place: {"action":"compact","trim":["%d"],"collapse":[{"from":"missing","to":"s999.0","note":"Keep the task."}]}`, call))
	out, isErr := c.lastToolOutput()
	if !isErr || !strings.Contains(out, "unknown id") || strings.Contains(out, "## Index") ||
		strings.Contains(out, "Replacement index") {
		t.Fatalf("unknown IDs did not return an ordinary validation error: %s", out)
	}
	if c.records() != 0 {
		t.Fatal("invalid request persisted partial work")
	}
	c.turn(fmt.Sprintf(`compact_in_place: {"action":"compact","trim":["%d","%d","%d"]}`, hello, call, call))
	if out, isErr := c.lastToolOutput(); !isErr || !strings.Contains(out, "has no tool output") ||
		!strings.Contains(out, "listed twice") {
		t.Fatalf("invalid trims not rejected: %s", out)
	}
	if c.records() != 0 {
		t.Fatal("invalid trims persisted partial work")
	}
	c.turn(fmt.Sprintf(`compact_in_place: {"action":"compact","trim":["%d"]}`, call))
	if out, isErr := c.lastToolOutput(); isErr {
		t.Fatalf("valid trim failed: %s", out)
	}
	if c.records() != 1 {
		t.Fatal("valid trim was not recorded")
	}
	c.turn(fmt.Sprintf(`compact_in_place: {"action":"compact","trim":["%d"]}`, call))
	if out, isErr := c.lastToolOutput(); !isErr || !strings.Contains(out, "already trimmed") {
		t.Fatalf("repeated trim not rejected: %s", out)
	}
	if c.records() != 1 {
		t.Fatal("repeated trim was recorded")
	}
}
