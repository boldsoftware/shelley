package server

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

func TestCompactionToolPairs(t *testing.T) {
	collapse := func(id claudetool.IndexID) claudetool.CompactInPlaceInput {
		return claudetool.CompactInPlaceInput{
			Collapse: []claudetool.CompactCollapse{{From: id, To: id, Note: "Keep the rule; command completed."}},
		}
	}
	t.Run("intervening task stays in the unit", func(t *testing.T) {
		items := []contextItem{
			textItem(1, llm.MessageRoleUser, "go"),
			callItem(2, "a"),
			textItem(3, llm.MessageRoleUser, "KEEP_THIS_RULE"),
			outputItem(4, "a", strings.Repeat("output ", 20)),
			textItem(5, llm.MessageRoleAssistant, "recent"),
		}
		rows, recent := splitRows(items, recentStart(items, 1, piTokenCounter))
		var ids []string
		for _, row := range rows {
			ids = append(ids, rowID(row))
		}
		if !reflect.DeepEqual(ids, []string{"1", "2..4", "5"}) || recent != 2 {
			t.Fatalf("rows %v, recent %d", ids, recent)
		}
		index, err := compactIndex(items, 0, 1, piTokenCounter)
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`(?m)^2\.\.4 +assistant\* +\d+ +start,end,trim +`).MatchString(index) ||
			!strings.Contains(index, "KEEP_THIS_RULE") || regexp.MustCompile(`(?m)^[234] +`).MatchString(index) {
			t.Fatalf("index exposes halves or loses the task:\n%s", index)
		}
		for _, stale := range []claudetool.IndexID{"2", "3", "4"} {
			if _, err := buildCompaction(items, 1, piTokenCounter, collapse(stale)); err == nil || !strings.Contains(err.Error(), "unknown id") {
				t.Fatalf("old ID %s silently changed scope: %v", stale, err)
			}
		}
		c, err := buildCompaction(items, 1, piTokenCounter, collapse("2..4"))
		if err != nil || len(c.Squishes) != 1 || c.Squishes[0].FromSequenceID != 2 || c.Squishes[0].ToSequenceID != 4 ||
			c.Squishes[0].Summary != collapse("2..4").Collapse[0].Note {
			t.Fatalf("whole unit: %+v, %v", c, err)
		}
		if after, err := applyInPlaceCompaction(items, c, "c1", 6); err != nil || len(after) != 3 {
			t.Fatalf("apply whole unit: %+v, %v", after, err)
		}
		trim, err := buildCompaction(items, 1, piTokenCounter, claudetool.CompactInPlaceInput{Trim: []claudetool.IndexID{"2..4"}})
		if err != nil || len(trim.Trims) != 1 || trim.Trims[0].SequenceID != 4 {
			t.Fatalf("trim whole unit: %+v, %v", trim, err)
		}
	})
	t.Run("all parallel results stay together", func(t *testing.T) {
		call := callItem(1, "a")
		call.message.Content = append(call.message.Content, callItem(1, "b").message.Content...)
		items := []contextItem{
			call, outputItem(2, "a", strings.Repeat("output ", 20)),
			textItem(3, llm.MessageRoleAssistant, "between"),
			outputItem(4, "b", strings.Repeat("output ", 20)),
			textItem(5, llm.MessageRoleAssistant, "recent"),
		}
		rows, _ := splitRows(items, recentStart(items, 1, piTokenCounter))
		if rowID(rows[0]) != "1..4" {
			t.Fatalf("parallel outputs split: %+v", rows)
		}
		c, err := buildCompaction(items, 1, piTokenCounter, collapse("1..4"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := applyInPlaceCompaction(items, c, "c1", 6); err != nil {
			t.Fatal(err)
		}
		for _, span := range [][2]int64{{1, 3}, {3, 4}} {
			_, err := applyInPlaceCompaction(items, db.InPlaceCompaction{Squishes: []db.CompactionSquish{{
				FromSequenceID: span[0], ToSequenceID: span[1], Summary: "Done.",
			}}}, "c1", 6)
			if err == nil || !strings.Contains(err.Error(), "splits a tool call") {
				t.Fatalf("text boundary split %v: %v", span, err)
			}
		}
	})
	t.Run("mixed result gets a scoped ID", func(t *testing.T) {
		output := outputItem(2, "a", strings.Repeat("output ", 20))
		output.message.Content = append(output.message.Content, llm.Content{Type: llm.ContentTypeThinking, Thinking: "metadata"})
		items := []contextItem{callItem(1, "a"), output, textItem(3, llm.MessageRoleAssistant, "recent")}
		rows, _ := splitRows(items, recentStart(items, 1, piTokenCounter))
		if rowID(rows[0]) != "1..2" {
			t.Fatalf("new group reused the old scalar ID: %s", rowID(rows[0]))
		}
		if _, err := buildCompaction(items, 1, piTokenCounter, collapse("1")); err == nil || !strings.Contains(err.Error(), "unknown id") {
			t.Fatalf("old ID silently expanded: %v", err)
		}
	})
	t.Run("text-only recorded squish inside a pair stays valid", func(t *testing.T) {
		items := []contextItem{
			callItem(1, "a"),
			textItem(2, llm.MessageRoleAssistant, "first"),
			textItem(3, llm.MessageRoleAssistant, "second"),
			outputItem(4, "a", "result"),
		}
		after, err := applyInPlaceCompaction(items, db.InPlaceCompaction{Squishes: []db.CompactionSquish{{
			FromSequenceID: 2, ToSequenceID: 3, Summary: "Intermediate text.",
		}}}, "c1", 5)
		if err != nil || len(after) != 3 || after[0].from != 1 || after[2].from != 4 {
			t.Fatalf("pair-preserving record rejected: %+v, %v", after, err)
		}
	})
	t.Run("recent boundary keeps the whole exchange", func(t *testing.T) {
		items := []contextItem{
			textItem(1, llm.MessageRoleUser, "older"),
			callItem(2, "a"),
			textItem(3, llm.MessageRoleUser, strings.Repeat("x", 4*25_000)),
			outputItem(4, "a", "result"),
			textItem(5, llm.MessageRoleAssistant, "recent"),
		}
		if cut := recentStart(items, 20_000, piTokenCounter); cut != 1 {
			t.Fatalf("recent cut %d splits the exchange", cut)
		}
		if _, err := buildCompaction(items, 20_000, piTokenCounter, collapse("2..4")); err == nil || !strings.Contains(err.Error(), "recent part") {
			t.Fatalf("protected exchange: %v", err)
		}
	})
	t.Run("large exchange stays whole without a hard cap", func(t *testing.T) {
		items := []contextItem{
			callItem(1, "a"),
			textItem(2, llm.MessageRoleUser, "keep this rule"),
			outputItem(3, "a", strings.Repeat("x", 4*31_000)),
			textItem(4, llm.MessageRoleAssistant, "recent"),
		}
		c, err := buildCompaction(items, 1, piTokenCounter, collapse("1..3"))
		if err != nil || len(c.Squishes) != 1 || c.Squishes[0].FromSequenceID != 1 || c.Squishes[0].ToSequenceID != 3 {
			t.Fatalf("large exchange split or rejected: %+v, %v", c, err)
		}
	})
	t.Run("hidden evidence still constrains raw boundaries", func(t *testing.T) {
		call := callItem(1, "a")
		call.message.Content = append(call.message.Content, compactCallItem(1, "index").message.Content...)
		items := []contextItem{
			call, outputItem(2, "a", strings.Repeat("output ", 20)),
			outputItem(3, "index", "index"),
			textItem(4, llm.MessageRoleAssistant, "recent"),
		}
		index, err := compactIndex(items, 0, 1, piTokenCounter)
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`(?m)^1 +assistant +\d+ +trim +`).MatchString(index) {
			t.Fatalf("index offers an unsafe end:\n%s", index)
		}
		if _, err := buildCompaction(items, 1, piTokenCounter, collapse("1")); err == nil || !strings.Contains(err.Error(), `keep {"from":"1","to":"1"}`) {
			t.Fatalf("hidden evidence split accepted: %v", err)
		}
	})
}
