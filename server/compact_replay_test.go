package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// TestHistoricalCompactionReplay is an opt-in, read-only replay of audited
// compact_in_place calls. It deliberately uses the ordinary history
// reconstruction and validation functions so the same harness can be copied
// to a baseline checkout and run against the same inputs.
func TestHistoricalCompactionReplay(t *testing.T) {
	dbPath, callsPath, outPath := os.Getenv("SHELLEY_COMPACTION_REPLAY_DB"),
		os.Getenv("SHELLEY_COMPACTION_REPLAY_CALLS"),
		os.Getenv("SHELLEY_COMPACTION_REPLAY_OUT")
	if dbPath == "" && callsPath == "" && outPath == "" {
		t.Skip("set SHELLEY_COMPACTION_REPLAY_DB, SHELLEY_COMPACTION_REPLAY_CALLS, and SHELLEY_COMPACTION_REPLAY_OUT to run historical replay")
	}
	if dbPath == "" || callsPath == "" || outPath == "" {
		t.Fatal("all three replay environment variables must be set")
	}

	callsBytes, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatalf("read audit calls: %v", err)
	}
	var audited []struct {
		Conversation string `json:"conv"`
		Sequence     int64  `json:"seq"`
		Date         string `json:"date"`
		Generation   int64  `json:"generation"`
		Model        string `json:"model"`
		Call         struct {
			ToolName  string          `json:"ToolName"`
			ToolInput json.RawMessage `json:"ToolInput"`
		} `json:"call"`
	}
	if err := json.Unmarshal(callsBytes, &audited); err != nil {
		t.Fatalf("decode audit calls: %v", err)
	}

	uri := "file:" + (&url.URL{Path: dbPath}).EscapedPath() + "?mode=ro"
	rawDB, err := sql.Open("sqlite", uri)
	if err != nil {
		t.Fatalf("open replay database read-only: %v", err)
	}
	defer rawDB.Close()
	rawDB.SetMaxOpenConns(1)
	queries := generated.New(rawDB)

	type attempt struct {
		Conversation string `json:"conversation"`
		Sequence     int64  `json:"sequence"`
		Date         string `json:"date"`
		Status       string `json:"status"`
		HistoryItems int    `json:"history_items,omitempty"`
		Squishes     int    `json:"squishes,omitempty"`
		Trims        int    `json:"trims,omitempty"`
		Error        string `json:"error,omitempty"`
	}
	type report struct {
		Caveats           []string       `json:"caveats"`
		Counts            map[string]int `json:"counts"`
		IndexCallsSkipped int            `json:"index_calls_skipped"`
		Attempts          []attempt      `json:"attempts"`
	}
	out := report{
		Caveats: []string{
			"Only compact_in_place calls whose input is represented by the current schema are replayed; missing collapse notes are reported as unsupported, never synthesized.",
			"Token boundaries use the current tokenCounterFor(model) heuristic, not historical provider token counts or historical system/tool definitions.",
			"Context eligibility uses the snapshot's current excluded_from_context flags; these may differ from flags at the original call time.",
			"User presentation uses the snapshot's conversation role/options through contextItems, including distillation and provenance/sequence wrappers; those options may differ from their historical values.",
			"Replay reconstructs stored history before each call and does not recreate historical index nudges or the exact transient prompt.",
		},
		Counts: map[string]int{},
	}
	historyByConversation := make(map[string][]generated.Message)
	managers := make(map[string]*ConversationManager)
	for _, call := range audited {
		if call.Call.ToolName != "compact_in_place" {
			continue
		}
		a := attempt{Conversation: call.Conversation, Sequence: call.Sequence, Date: call.Date}
		var rawInput struct {
			Action   string               `json:"action"`
			Trim     []claudetool.IndexID `json:"trim"`
			Collapse []struct {
				From claudetool.IndexID `json:"from"`
				To   claudetool.IndexID `json:"to"`
				Note *string            `json:"note"`
			} `json:"collapse"`
		}
		if len(call.Call.ToolInput) == 0 || string(call.Call.ToolInput) == "null" {
			t.Fatalf("compact call %s/%d has no tool_input", call.Conversation, call.Sequence)
		}
		if err := json.Unmarshal(call.Call.ToolInput, &rawInput); err != nil {
			t.Fatalf("decode compact input %s/%d: %v", call.Conversation, call.Sequence, err)
		}
		if rawInput.Action == "index" {
			out.IndexCallsSkipped++
			continue
		}
		var input claudetool.CompactInPlaceInput
		if err := json.Unmarshal(call.Call.ToolInput, &input); err != nil {
			t.Fatalf("decode supported compact input %s/%d: %v", call.Conversation, call.Sequence, err)
		}
		missingNote := false
		for _, c := range rawInput.Collapse {
			if c.Note == nil {
				missingNote = true
				break
			}
		}
		if missingNote {
			a.Status = "unsupported_missing_note"
			out.Attempts = append(out.Attempts, a)
			out.Counts[a.Status]++
			continue
		}
		rows, ok := historyByConversation[call.Conversation]
		if !ok {
			rows, err = queries.ListMessages(context.Background(), call.Conversation)
			if err != nil {
				t.Fatalf("list messages for %s: %v", call.Conversation, err)
			}
			historyByConversation[call.Conversation] = rows
			conv, err := queries.GetConversation(context.Background(), call.Conversation)
			if err != nil {
				t.Fatalf("get conversation %s: %v", call.Conversation, err)
			}
			managers[call.Conversation] = &ConversationManager{
				logger: slog.Default(), conversationID: call.Conversation,
				role: conversationRoleOf(conv), conversationOptions: db.ParseConversationOptions(conv.ConversationOptions),
			}
		}
		prior := make([]generated.Message, 0, len(rows))
		for _, row := range rows {
			if row.SequenceID < call.Sequence && row.Generation == call.Generation && !row.ExcludedFromContext {
				prior = append(prior, row)
			}
		}
		history, _, err := managers[call.Conversation].contextItems(prior)
		if err != nil {
			a.Status, a.Error = "reconstruction_error", err.Error()
			out.Attempts = append(out.Attempts, a)
			out.Counts[a.Status]++
			continue
		}
		a.HistoryItems = len(history)
		historyBytes := func() []byte {
			var messages []llm.Message
			for _, item := range history {
				messages = append(messages, item.message)
			}
			data, err := json.Marshal(messages)
			if err != nil {
				t.Fatal(err)
			}
			return data
		}
		before := string(historyBytes())
		compaction, err := buildCompaction(history, defaultKeepRecentTokens, tokenCounterFor(call.Model), input)
		if err != nil {
			a.Status, a.Error = "rejected", err.Error()
		} else {
			view, err := compactionView(history)
			if err != nil {
				t.Fatal(err)
			}
			rows, _ := splitRows(view, recentStart(view, defaultKeepRecentTokens, tokenCounterFor(call.Model)))
			var expected []db.CompactionSquish
			for _, col := range input.Collapse {
				i, j := indexOfRow(rows, col.From), indexOfRow(rows, col.To)
				if i < 0 || j < i {
					t.Fatalf("accepted unknown/reversed range at %s/%d", call.Conversation, call.Sequence)
				}
				last := rows[j][len(rows[j])-1]
				expected = append(expected, db.CompactionSquish{
					FromSequenceID: rows[i][0].from, ToSequenceID: last.to, Summary: col.Note,
				})
			}
			sort.Slice(expected, func(i, j int) bool { return expected[i].FromSequenceID < expected[j].FromSequenceID })
			if len(expected) != len(compaction.Squishes) {
				t.Fatalf("changed collapse count at %s/%d", call.Conversation, call.Sequence)
			}
			for i := range expected {
				if expected[i] != compaction.Squishes[i] {
					t.Fatalf("changed collapse scope or note at %s/%d", call.Conversation, call.Sequence)
				}
			}
			a.Squishes, a.Trims = len(compaction.Squishes), len(compaction.Trims)
			if input.Action == "skip" || (len(compaction.Squishes) == 0 && len(compaction.Trims) == 0) {
				a.Status = "noop"
			} else if _, err := applyInPlaceCompaction(history, compaction, call.Conversation, call.Sequence); err != nil {
				t.Fatalf("validated record failed application at %s/%d: %v", call.Conversation, call.Sequence, err)
			} else {
				a.Status = "accepted"
			}
		}
		if string(historyBytes()) != before {
			t.Fatalf("replay mutated source history at %s/%d", call.Conversation, call.Sequence)
		}
		out.Attempts = append(out.Attempts, a)
		out.Counts[a.Status]++
	}
	sort.Slice(out.Attempts, func(i, j int) bool {
		if out.Attempts[i].Conversation != out.Attempts[j].Conversation {
			return out.Attempts[i].Conversation < out.Attempts[j].Conversation
		}
		return out.Attempts[i].Sequence < out.Attempts[j].Sequence
	})
	encoded, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatalf("encode replay report: %v", err)
	}
	if err := os.WriteFile(outPath, append(encoded, '\n'), 0o600); err != nil {
		t.Fatalf("write replay report: %v", err)
	}
	t.Logf("historical compaction replay: %s", strings.TrimSpace(fmt.Sprint(out.Counts)))
}
