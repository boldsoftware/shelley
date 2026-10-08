package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// The compact_in_place tool lets the agent compact its own context: "index"
// lists the older part of the LLM's current view, "compact" records an
// in-place compaction (collapsing message ranges into notes, trimming tool
// outputs) and the turn continues on the compacted history. While the tool is
// enabled the agent is also nudged with the context size and the index (see
// contextNudger), unless it was enabled mid-conversation (see
// EnableCompactInPlace).

const (
	// defaultCompactNudgeTokens is where the first context nudge fires when
	// ConversationOptions.CompactNudgeTokens is unset.
	defaultCompactNudgeTokens = 250_000
	// compactNudgeStep is the distance between later nudges.
	compactNudgeStep = 50_000
	// maxCollapseTokens bounds the history one collapse may cover, so that a
	// single note never stands for too much. A single message, or a single
	// tool call and its output, may exceed it.
	maxCollapseTokens = 20_000
)

// inPlaceCompactor is the claudetool.InPlaceCompactor of the loop of one
// generation.
type inPlaceCompactor struct {
	cm         *ConversationManager
	generation uint64
}

func (c inPlaceCompactor) Index(ctx context.Context) (string, error) {
	return c.cm.contextIndex(ctx)
}

// contextIndex renders the index of the current context.
func (cm *ConversationManager) contextIndex(ctx context.Context) (string, error) {
	items, err := cm.loadContextItems(ctx)
	if err != nil {
		return "", err
	}
	return compactIndex(items, cm.keepRecentTokens)
}

func (c inPlaceCompactor) Compact(ctx context.Context, in claudetool.CompactInPlaceInput) (string, error) {
	return c.cm.compactMidTurn(ctx, c.generation, in)
}

// compactMidTurn validates in against the current context and records it.
// The running loop picks up the compacted history before its next request.
func (cm *ConversationManager) compactMidTurn(ctx context.Context, generation uint64, in claudetool.CompactInPlaceInput) (string, error) {
	// Like takeInjectable, this runs in the loop goroutine: hold the lifecycle
	// lock only to validate and record, so a concurrent teardown either sees
	// the record or the record is never made.
	cm.loopLifecycleMu.Lock()
	cm.mu.Lock()
	stale := cm.loopTearingDown || cm.loop == nil || cm.loopGeneration != generation || cm.distilling || cm.cancelling
	cm.mu.Unlock()
	if stale {
		cm.loopLifecycleMu.Unlock()
		return "", fmt.Errorf("the conversation is being stopped or rewritten; not compacted")
	}
	items, err := cm.loadContextItems(ctx)
	var c db.InPlaceCompaction
	if err == nil {
		c, err = buildCompaction(items, cm.keepRecentTokens, in)
	}
	var after []contextItem
	if err == nil {
		after, err = applyInPlaceCompaction(items, c, cm.conversationID, 0)
	}
	if err == nil {
		// Compaction hides the evidence of compacting: the nudges, and the
		// earlier calls of this tool (this call stays, as a receipt).
		c.HiddenSequenceIDs, c.HiddenToolUseIDs = compactionEvidence(after)
		after, err = applyInPlaceCompaction(items, c, cm.conversationID, 0)
	}
	var created *generated.Message
	if err == nil {
		created, err = cm.createInPlaceCompaction(context.WithoutCancel(ctx), c)
	}
	if err == nil {
		cm.mu.Lock()
		cm.compactedGeneration = generation
		cm.mu.Unlock()
	}
	cm.loopLifecycleMu.Unlock()
	if err != nil {
		return "", err
	}
	if err := cm.publishCreated(ctx, created); err != nil {
		return "", err
	}
	return fmt.Sprintf("Compacted: %d collapsed, %d trimmed; history ~%d -> ~%d tokens.",
		len(c.Squishes), len(c.Trims), itemTokens(items), itemTokens(after)), nil
}

var errCompactInPlaceUnavailable = errors.New("compact_in_place is not available in this conversation")

// errCompactInPlaceAlreadyOn ends EnableCompactInPlace's mutation early,
// leaving the loop alone.
var errCompactInPlaceAlreadyOn = errors.New("compact_in_place is already on")

// EnableCompactInPlace turns compact_in_place on in a conversation started
// without it, for the Compact in Place button; the agent is not nudged (see
// DisableCompactNudges). Tool definitions are not in the log: every loop
// builds them from the conversation options. So this updates the options,
// drops the loop so the next turn is built with the tool, and appends a
// marker that says where it appeared. Enabling it twice is a no-op.
//
// It returns errAgentWorking mid-turn: dropping the loop would cut the turn
// short.
func (cm *ConversationManager) EnableCompactInPlace(ctx context.Context) error {
	if cm.role == roleBtwReader {
		// Its loop has a fixed toolset; see ensureLoop.
		return errCompactInPlaceUnavailable
	}
	// Serializes with ApplySettings, which writes back the in-memory
	// options it read.
	cm.modelSettingsMu.Lock()
	defer cm.modelSettingsMu.Unlock()
	var previous ConversationSettings
	err := cm.resetLoopAfter(true, func() error {
		if cm.IsAgentWorking() {
			return errAgentWorking
		}
		conv, err := cm.db.GetConversationByID(ctx, cm.conversationID)
		if err != nil {
			return err
		}
		previous = conversationSettings(*conv)
		opts, changed, err := cm.db.ModifyConversationOptions(ctx, cm.conversationID, func(o *db.ConversationOptions) bool {
			if claudetool.IsToolEnabled(claudetool.CompactInPlaceName, o.ToolOverrides, o.DisableAllTools) {
				return false
			}
			o.ToolOverrides = maps.Clone(orEmpty(o.ToolOverrides))
			o.ToolOverrides[claudetool.CompactInPlaceName] = "on"
			o.DisableCompactNudges = true
			return true
		})
		if err != nil {
			return err
		}
		if !changed {
			return errCompactInPlaceAlreadyOn
		}
		cm.mu.Lock()
		cm.conversationOptions = opts
		cm.mu.Unlock()
		return nil
	})
	if errors.Is(err, errCompactInPlaceAlreadyOn) {
		return nil
	}
	if err != nil {
		return err
	}
	// The tool is on whether or not the marker lands, and a retry would find
	// nothing to do. So don't fail the caller: the cost is a log that doesn't
	// show where the tool appeared.
	if err := cm.recordModelChangeMarker(ctx, ModelChangeUserData{
		ToolsOn:  []string{claudetool.CompactInPlaceName},
		Previous: &previous,
		Text:     "Enabled the compact_in_place tool.",
	}); err != nil {
		cm.logger.Error("compact_in_place enabled, but its marker was not recorded",
			"conversationID", cm.conversationID, "error", err)
	}
	return nil
}

// handleEnableCompactInPlace handles POST
// /conversation/<id>/enable-compact-in-place: in a conversation started
// without compact_in_place, the Compact in Place button enables the tool
// before asking the agent to use it.
func (s *Server) handleEnableCompactInPlace(w http.ResponseWriter, r *http.Request, conversationID string) {
	ctx := r.Context()
	conversation, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}
	if conversation.Archived {
		http.Error(w, "conversation is archived", http.StatusConflict)
		return
	}
	// A draft's options still travel with its first send.
	if conversation.IsDraft {
		http.Error(w, "conversation is still a draft", http.StatusConflict)
		return
	}
	userEmail := r.Header.Get("X-ExeDev-Email")
	ctx = contextWithUserEmail(ctx, userEmail)
	manager, err := s.getOrCreateConversationManager(ctx, conversationID, userEmail)
	if err != nil {
		s.internalError(w, "Failed to get conversation manager", err, "conversationID", conversationID)
		return
	}
	if err := manager.EnableCompactInPlace(ctx); err != nil {
		switch {
		case errors.Is(err, errAgentWorking):
			http.Error(w, "Finish or stop the current turn to compact in place", http.StatusConflict)
		case errors.Is(err, errCompactInPlaceUnavailable):
			http.Error(w, err.Error(), http.StatusConflict)
		default:
			s.internalError(w, "Failed to enable compact_in_place", err, "conversationID", conversationID)
		}
		return
	}
	updated, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		s.internalError(w, "Failed to reload conversation", err, "conversationID", conversationID)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updated)
}

// compactionEvidence returns the context nudges in items, and the
// compact_in_place calls whose results are in items.
func compactionEvidence(items []contextItem) (seqs []int64, toolUseIDs []string) {
	results := map[string]bool{}
	for _, it := range items {
		if it.source == nil {
			continue
		}
		if it.source.UserData != nil {
			var ud struct {
				ContextNudge bool `json:"context_nudge"`
			}
			if json.Unmarshal([]byte(*it.source.UserData), &ud) == nil && ud.ContextNudge {
				seqs = append(seqs, it.from)
			}
		}
		for _, c := range it.message.Content {
			if c.Type == llm.ContentTypeToolResult {
				results[c.ToolUseID] = true
			}
		}
	}
	for _, it := range items {
		for _, c := range it.message.Content {
			if c.Type == llm.ContentTypeToolUse && c.ToolName == claudetool.CompactInPlaceName && results[c.ID] {
				toolUseIDs = append(toolUseIDs, c.ID)
			}
		}
	}
	return seqs, toolUseIDs
}

// buildCompaction turns the agent's request into a record, validating it
// against the compaction view of history and its recent part. It reports
// every problem at once.
func buildCompaction(history []contextItem, keepRecentTokens int, in claudetool.CompactInPlaceInput) (db.InPlaceCompaction, error) {
	var c db.InPlaceCompaction
	if len(in.Trim) == 0 && len(in.Collapse) == 0 {
		return c, fmt.Errorf("nothing to compact: give trim and/or collapse")
	}
	items, err := compactionView(history)
	if err != nil {
		return c, err
	}
	rows, recent := splitRows(items, recentStart(items, keepRecentTokens))
	alone, err := aloneRows(rows)
	if err != nil {
		return c, err
	}
	var problems []string
	var trimmed []int
	for _, id := range in.Trim {
		r := indexOfRow(rows, id)
		problem := func(format string, args ...any) {
			problems = append(problems, fmt.Sprintf("trim %s: ", id)+fmt.Sprintf(format, args...))
		}
		switch {
		case r < 0:
			problem("unknown id; use ids from the index")
		case r >= recent:
			problem("in the recent part, which is kept as is")
		case slices.Contains(trimmed, r):
			problem("listed twice")
		case !slices.ContainsFunc(rows[r], func(it contextItem) bool { return hasContent(it.message, llm.ContentTypeToolResult) }):
			problem("has no tool output")
		case len(rowTrims(rows[r])) == 0:
			problem("already trimmed")
		default:
			trimmed = append(trimmed, r)
			c.Trims = append(c.Trims, rowTrims(rows[r])...)
		}
	}

	type span struct{ i, j int }
	var spans []span
	for _, col := range in.Collapse {
		name := fmt.Sprintf("collapse %s-%s", col.From, col.To)
		problem := func(format string, args ...any) {
			problems = append(problems, name+": "+fmt.Sprintf(format, args...))
		}
		i, j := indexOfRow(rows, col.From), indexOfRow(rows, col.To)
		switch {
		case i < 0 || j < 0:
			problem("unknown id; use ids from the index")
			continue
		case j < i:
			problem("from comes after to")
			continue
		case j >= recent:
			problem("reaches into the recent part (from %s on), which is kept as is", rowID(rows[recent]))
			continue
		case orphanOutput(rows[i]):
			problem("%s is a tool output without its call; start the range after it", rowID(rows[i]))
			continue
		case orphanCall(rows[j]):
			problem("%s is a tool call without its output; end the range before it", rowID(rows[j]))
			continue
		case slices.ContainsFunc(spans, func(s span) bool { return i <= s.j && s.i <= j }):
			problem("overlaps another collapse")
			continue
		}
		// A row that can only be collapsed on its own may not share a range.
		if k := slices.Index(alone[i:j+1], true); k >= 0 && j > i {
			instead := "collapse each of those rows on its own"
			if r := suggestRanges(rows, alone, i, j); r != "" {
				instead = "collapse e.g. " + r + " instead"
			}
			problem("covers %s, which can only be collapsed on its own; %s", rowID(rows[i+k]), instead)
			continue
		}
		// The range is sound; whatever else is wrong with it, it is taken.
		spans = append(spans, span{i, j})
		tokens := 0
		for _, row := range rows[i : j+1] {
			tokens += itemTokens(row)
		}
		switch {
		case strings.TrimSpace(col.Note) == "":
			problem("note is empty")
			continue
		case tokens > maxCollapseTokens && j > i:
			problem("covers ~%d tokens; at most ~%d per collapse unless it is a single row, so split it", tokens, maxCollapseTokens)
			continue
		case (len(col.Note)+3)/4 >= tokens:
			problem("note is not shorter than the ~%d tokens it replaces", tokens)
			continue
		}
		last := rows[j][len(rows[j])-1]
		c.Squishes = append(c.Squishes, db.CompactionSquish{FromSequenceID: rows[i][0].from, ToSequenceID: last.to, Summary: col.Note})
	}
	switch len(problems) {
	case 0:
	case 1:
		return c, fmt.Errorf("%s. Nothing was compacted; fix this and send the whole request again", problems[0])
	default:
		return c, fmt.Errorf("%d problems. Nothing was compacted; fix them and send the whole request again:\n- %s", len(problems), strings.Join(problems, "\n- "))
	}
	// Squishes apply in order, each to the view the previous ones left; they
	// are disjoint, so order them as they appear.
	slices.SortFunc(c.Squishes, func(a, b db.CompactionSquish) int { return int(a.FromSequenceID - b.FromSequenceID) })
	return c, nil
}

// collapsesAlone reports whether a collapse may cover it only on its own: a
// message from the user (or the parent agent), or a note or summary from an
// earlier compaction. They carry the task and its rules, and the agent
// tends to fold them into notes that keep little of them.
func collapsesAlone(it contextItem) (bool, error) {
	switch {
	case it.source == nil:
		return true, nil
	case it.source.Type != string(db.MessageTypeUser) || isToolResultMessage(it.message):
		return false, nil
	case it.source.UserData == nil:
		return true, nil
	}
	tag, _, err := provenanceTag([]byte(*it.source.UserData))
	if err != nil {
		return false, fmt.Errorf("message %s: %w", itemID(it), err)
	}
	return tag == "" || tag == "parent_message", nil
}

// splitRows groups items into the index's rows, and returns the index of the
// first row of the recent part, which starts at items[recent]. A message with
// tool calls shares a row with the message carrying their outputs, which
// follows it; so a range of rows cannot split a call from its output. The
// recent part never starts at an output, so no row straddles it.
func splitRows(items []contextItem, recent int) (rows [][]contextItem, recentRow int) {
	group := func(items []contextItem) {
		for k := 0; k < len(items); k++ {
			if k+1 < len(items) && hasContent(items[k].message, llm.ContentTypeToolUse) && isToolResultMessage(items[k+1].message) {
				rows = append(rows, items[k:k+2])
				k++
				continue
			}
			rows = append(rows, items[k:k+1])
		}
	}
	group(items[:recent])
	recentRow = len(rows)
	group(items[recent:])
	return rows, recentRow
}

// rowID is the id of a row in the index: that of its first item.
func rowID(row []contextItem) string {
	return itemID(row[0])
}

func indexOfRow(rows [][]contextItem, id claudetool.IndexID) int {
	return slices.IndexFunc(rows, func(row []contextItem) bool { return rowID(row) == string(id) })
}

// aloneRows reports for each row whether a collapse may cover it only on its
// own (see collapsesAlone). Rows of a call and its output never are.
func aloneRows(rows [][]contextItem) ([]bool, error) {
	alone := make([]bool, len(rows))
	for i, row := range rows {
		if len(row) > 1 {
			continue
		}
		var err error
		if alone[i], err = collapsesAlone(row[0]); err != nil {
			return nil, err
		}
	}
	return alone, nil
}

// Rows keep calls with their outputs, but a call can lack its output (the
// server stopped while it ran), or an output its call. A range may not start
// at such an output or end at such a call: it would leave the call, or the
// output, behind.
func orphanOutput(row []contextItem) bool {
	return hasContent(row[0].message, llm.ContentTypeToolResult)
}

func orphanCall(row []contextItem) bool {
	return hasContent(row[len(row)-1].message, llm.ContentTypeToolUse)
}

// suggestRanges lists the ranges of rows[i..j] between the rows that can only
// be collapsed on their own.
func suggestRanges(rows [][]contextItem, alone []bool, i, j int) string {
	var ranges []string
	start := -1
	for k := i; k <= j+1; k++ {
		switch {
		case k <= j && !alone[k]:
			if start < 0 {
				start = k
			}
		case start >= 0:
			end := k - 1
			for start <= end && orphanOutput(rows[start]) {
				start++
			}
			for start <= end && orphanCall(rows[end]) {
				end--
			}
			if start <= end {
				r := rowID(rows[start])
				if start < end {
					r += "-" + rowID(rows[end])
				}
				ranges = append(ranges, r)
			}
			start = -1
		}
	}
	return strings.Join(ranges, ", ")
}

// rowTrims lists the trims of a row's tool outputs not trimmed yet.
func rowTrims(row []contextItem) []db.CompactionTrim {
	var trims []db.CompactionTrim
	for _, it := range row {
		for _, c := range it.message.Content {
			if c.Type == llm.ContentTypeToolResult && !isTrimmedOutput(c) {
				trims = append(trims, db.CompactionTrim{SequenceID: it.from, ToolUseID: c.ToolUseID})
			}
		}
	}
	return trims
}

// isTrimmedOutput reports whether c is a tool output trimmed by an in-place
// compaction: its text is all a placeholder trimmedToolOutputText made.
func isTrimmedOutput(c llm.Content) bool {
	if len(c.ToolResult) != 1 {
		return false
	}
	text := c.ToolResult[0].Text
	var conversationID string
	var seq int64
	_, err := fmt.Sscanf(text, trimmedToolOutputPrefix+": conversation_id %s sequence_id %d]", &conversationID, &seq)
	return err == nil && text == trimmedToolOutputText(conversationID, seq)
}

// compactionView returns history as the agent can compact it: without the
// evidence of compacting (see compactionEvidence), which the compaction
// hides anyway, and without the call now running, which has no output yet.
// Neither moves the recent part while the agent works.
func compactionView(history []contextItem) ([]contextItem, error) {
	seqs, toolUseIDs := compactionEvidence(history)
	view, err := hideItems(slices.Clone(history), seqs, toolUseIDs)
	if err != nil {
		return nil, err
	}
	// Like hideItems, remove only the call: other calls in its message stay,
	// and a message left without a call goes.
	for i := range view {
		content := slices.DeleteFunc(slices.Clone(view[i].message.Content), func(c llm.Content) bool {
			return c.Type == llm.ContentTypeToolUse && c.ToolName == claudetool.CompactInPlaceName
		})
		if len(content) == len(view[i].message.Content) {
			continue
		}
		view[i].message.Content = content
		if !hasContent(view[i].message, llm.ContentTypeToolUse) {
			view[i].message.Content = nil
		}
	}
	return slices.DeleteFunc(view, func(it contextItem) bool { return len(it.message.Content) == 0 }), nil
}

// recentStart returns the index of the first item of the recent part, which
// is kept as is: about keepRecentTokens of the newest history, starting on a
// message that is not a tool result.
func recentStart(items []contextItem, keepRecentTokens int) int {
	msgs := make([]llm.Message, len(items))
	for i, it := range items {
		msgs[i] = it.message
	}
	return findPiCutPoint(msgs, keepRecentTokens)
}

func itemID(it contextItem) string {
	if it.source == nil {
		return it.noteID
	}
	return strconv.FormatInt(it.from, 10)
}

func itemTokens(items []contextItem) int {
	n := 0
	for _, it := range items {
		n += estimatePiMessageTokens(it.message)
	}
	return n
}

const compactIndexGuidance = `Look for dead ends, out-of-date tool outputs (superseded file reads, old
build and test logs), side quests and finished subprojects. Collapse each into
a short note that keeps what still matters: decisions, findings, paths,
commands, ids. Trim tool outputs you no longer need. Big tool inputs and
outputs are most of the context, so focus there. Compacting only a little is
fine; when unsure, leave it. Originals stay retrievable by conversation_id and
sequence_id (see the previous-conversations skill).

Then call compact_in_place with action "compact": trim lists ids of rows whose
tool outputs you no longer need; collapse lists {from, to, note} ranges of ids
from the table. A range may not cover more than ~%dk tokens unless it is a
single row. Rows marked * are messages from the user or the parent and notes
and summaries from earlier compactions: they carry the task and its rules, so a
range may not cover one unless it is just that row.`

// compactIndex renders the index the agent compacts from.
func compactIndex(history []contextItem, keepRecentTokens int) (string, error) {
	items, err := compactionView(history)
	if err != nil {
		return "", err
	}
	recent := recentStart(items, keepRecentTokens)
	var b strings.Builder
	fmt.Fprintf(&b, "## Index\n\n%d messages, ~%d tokens.\n", len(items), itemTokens(items))
	if recent == 0 {
		b.WriteString("All of it is recent, so nothing can be compacted yet.\n")
		return b.String(), nil
	}
	fmt.Fprintf(&b, "The recent part, from %s on (%d messages, ~%d tokens), is kept as is and not listed.\n",
		itemID(items[recent]), len(items)-recent, itemTokens(items[recent:]))
	b.WriteString("\n")
	fmt.Fprintf(&b, compactIndexGuidance, maxCollapseTokens/1000)
	b.WriteString("\n\nOne row per message, oldest first, except that a tool call and its output\n" +
		"share a row. id is the sequence_id (of the call, for a shared row), or s<n>.<i>\n" +
		"for a note from an earlier compaction. tokens are estimated; `→ N` gives the\n" +
		"tokens of a call's output, or says it is trimmed or missing.\n\n```\n")

	rows, recentRow := splitRows(items, recent)
	alone, err := aloneRows(rows)
	if err != nil {
		return "", err
	}
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "id\trole\ttokens\tcontent")
	toolNames := map[string]string{}
	for i, row := range rows[:recentRow] {
		role := "note"
		switch {
		case row[0].source == nil:
		case row[0].message.Role == llm.MessageRoleAssistant:
			role = "assistant"
		default:
			role = "user"
		}
		if alone[i] {
			role += "*"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", rowID(row), role, itemTokens(row), describeRow(row, toolNames))
	}
	tw.Flush()
	b.WriteString("```\n")
	return b.String(), nil
}

// describeRow lists a row's blocks for the index, each tool call with the
// size of its output. toolNames maps tool_use ids seen so far to tool names,
// so outputs without their call in the row can be named after it.
func describeRow(row []contextItem, toolNames map[string]string) string {
	if row[0].source == nil {
		return "note " + quoteStart(row[0].note)
	}
	outputs := map[string]string{}
	for _, it := range row {
		for _, c := range it.message.Content {
			if c.Type == llm.ContentTypeToolResult {
				outputs[c.ToolUseID] = "→ trimmed"
				if !isTrimmedOutput(c) {
					outputs[c.ToolUseID] = fmt.Sprintf("→ %d", estimatePiMessageTokens(llm.Message{Content: []llm.Content{c}}))
				}
			}
		}
	}
	called := map[string]bool{}
	var parts []string
	for _, it := range row {
		for _, c := range it.message.Content {
			switch c.Type {
			case llm.ContentTypeText:
				if c.MediaType != "" {
					parts = append(parts, "image")
				} else {
					parts = append(parts, "text "+quoteStart(c.Text))
				}
			case llm.ContentTypeThinking, llm.ContentTypeRedactedThinking:
				parts = append(parts, "thinking")
			case llm.ContentTypeToolUse:
				toolNames[c.ID] = c.ToolName
				called[c.ID] = true
				part := c.ToolName
				if in := readableToolInput(c.ToolInput); in != "" {
					part += " " + quoteStart(in)
				}
				if out, ok := outputs[c.ID]; ok {
					part += " " + out
				} else {
					part += " → no output"
				}
				parts = append(parts, part)
			case llm.ContentTypeToolResult:
				if called[c.ToolUseID] {
					continue
				}
				name := toolNames[c.ToolUseID]
				if name == "" {
					name = "tool"
				}
				parts = append(parts, name+" output "+outputs[c.ToolUseID])
			}
		}
	}
	return strings.Join(parts, ", ")
}

// readableToolInput renders a tool call's input the way one would type it:
// the values of its fields in order, strings as they are and other scalars as
// key=value, leaving out empty strings and nested objects and arrays. Input
// that is not an object is shown as it is.
func readableToolInput(input json.RawMessage) string {
	var fields []string
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return string(input)
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return string(input)
		}
		var v any
		if err := dec.Decode(&v); err != nil {
			return string(input)
		}
		switch v := v.(type) {
		case string:
			if v != "" {
				fields = append(fields, v)
			}
		case json.Number, bool:
			fields = append(fields, fmt.Sprintf("%s=%v", key, v))
		}
	}
	return strings.Join(fields, " ")
}

// quoteStart quotes the start of s on one line.
func quoteStart(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const n = 60
	if r := []rune(s); len(r) > n {
		s = string(r[:n]) + "…"
	}
	return `"` + s + `"`
}

// contextNudger tells the agent the context size once it reaches at tokens,
// and again every compactNudgeStep beyond. A loop owns one; its level starts
// at the size the conversation already had, so a rebuilt loop does not
// repeat a nudge.
type contextNudger struct {
	mu     sync.Mutex
	at     uint64
	tokens uint64
	level  int
}

func newContextNudger(at int, tokens uint64) *contextNudger {
	if at <= 0 {
		at = defaultCompactNudgeTokens
	}
	n := &contextNudger{at: uint64(at), tokens: tokens}
	n.level = n.levelOf(tokens)
	return n
}

func (n *contextNudger) levelOf(tokens uint64) int {
	if tokens < n.at {
		return -1
	}
	return int((tokens - n.at) / compactNudgeStep)
}

// observe records the context size reported by a model response.
func (n *contextNudger) observe(usage llm.Usage) {
	if usage.IsZero() {
		return
	}
	n.mu.Lock()
	n.tokens = usage.ContextWindowUsed()
	n.mu.Unlock()
}

// take returns the size line of the nudge due now, if any. Dropping below a
// level (after a compaction) re-arms it.
func (n *contextNudger) take() (string, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	level := n.levelOf(n.tokens)
	due := level > n.level
	n.level = level
	if !due {
		return "", false
	}
	return fmt.Sprintf("Context is %dk.", (n.tokens+5000)/10000*10), true
}

// recordContextNudge records a user message marked context_nudge: size,
// then the index, so the agent can compact without asking for it.
func (cm *ConversationManager) recordContextNudge(ctx context.Context, size string) (llm.Message, error) {
	index, err := cm.contextIndex(ctx)
	if err != nil {
		return llm.Message{}, fmt.Errorf("index for context nudge: %w", err)
	}
	message := llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{
		{Type: llm.ContentTypeText, Text: size},
		{Type: llm.ContentTypeText, Text: index},
	}}
	created, err := cm.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: cm.conversationID,
		Type:           db.MessageTypeUser,
		LLMData:        message,
		UserData:       map[string]any{"context_nudge": true},
		UsageData:      llm.Usage{},
	})
	if err != nil {
		return message, fmt.Errorf("record context nudge: %w", err)
	}
	return message, cm.publishCreated(ctx, created)
}

// lastContextWindowSize returns the context size reported by the newest of
// rows (one generation's context rows) that carries usage.
func lastContextWindowSize(rows []generated.Message) uint64 {
	for i := len(rows) - 1; i >= 0; i-- {
		if n := calculateContextWindowSizeFromMsg(&rows[i]); n > 0 {
			return n
		}
	}
	return 0
}
