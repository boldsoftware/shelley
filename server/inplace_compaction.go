package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// contextItem is one message of the LLM's view of a conversation, together
// with the span of sequence ids it stands for. An original message spans just
// its own sequence id and keeps its source row; a squish summary spans the
// whole range it replaced, has no source row, and is identified by noteID.
type contextItem struct {
	from, to int64
	source   *generated.Message
	noteID   string // "s<record sequence id>.<squish index>"
	note     string // the squish summary
	message  llm.Message
}

// compactedContext converts one generation's context rows (in sequence order)
// into raw LLM messages: the system prompt messages, and the history with
// every in-place compaction record applied. Rows that are not part of the
// conversation (gitinfo, modelchange, slug, error) are skipped. It is the
// single place that decides what the model sees; callers layer presentation
// (sender provenance, distillation overrides) on top via each item's source.
func compactedContext(logger logWarner, rows []generated.Message) (history []contextItem, system []llm.Message, err error) {
	for i := range rows {
		row := &rows[i]
		switch db.MessageType(row.Type) {
		case db.MessageTypeGitInfo, db.MessageTypeModelChange, db.MessageTypeSlug, db.MessageTypeError:
			continue
		case db.MessageTypeInPlaceCompaction:
			c, err := decodeInPlaceCompaction(*row)
			if err != nil {
				return nil, nil, err
			}
			if history, err = applyInPlaceCompaction(history, c, row.ConversationID, row.SequenceID); err != nil {
				return nil, nil, fmt.Errorf("apply in-place compaction %d: %w", row.SequenceID, err)
			}
			continue
		}
		msg, err := convertToLLMMessage(*row)
		if err != nil {
			logger.Warn("Failed to convert message to LLM format", "messageID", row.MessageID, "error", err)
			continue
		}
		if row.Type == string(db.MessageTypeSystem) {
			// A settings change renders a new system prompt (see
			// ApplySettings); the latest one is in effect.
			system = []llm.Message{msg}
			continue
		}
		history = append(history, contextItem{from: row.SequenceID, to: row.SequenceID, source: row, message: msg})
	}
	return history, system, nil
}

// compactionNoteText is what the model sees in place of squished messages.
func compactionNoteText(conversationID string, from, to int64, summary string) string {
	return fmt.Sprintf("[Messages compacted: conversation_id %s sequence_id %d-%d. Summary:]\n%s", conversationID, from, to, summary)
}

// trimmedToolOutputPrefix starts every trimmed tool output. Real tool output
// may start with it too; see isTrimmedOutput.
const trimmedToolOutputPrefix = "[Tool output compacted"

// trimmedToolOutputText is what the model sees in place of a trimmed tool
// output.
func trimmedToolOutputText(conversationID string, sequenceID int64) string {
	return fmt.Sprintf("%s: conversation_id %s sequence_id %d]", trimmedToolOutputPrefix, conversationID, sequenceID)
}

func decodeInPlaceCompaction(msg generated.Message) (db.InPlaceCompaction, error) {
	var c db.InPlaceCompaction
	if msg.UserData == nil {
		return c, fmt.Errorf("in-place compaction %s has no user data", msg.MessageID)
	}
	if err := json.Unmarshal([]byte(*msg.UserData), &c); err != nil {
		return c, fmt.Errorf("decode in-place compaction %s: %w", msg.MessageID, err)
	}
	return c, nil
}

// applyInPlaceCompaction applies c, recorded as recordSeq in conversationID,
// to items, returning the new view. items is not modified.
func applyInPlaceCompaction(items []contextItem, c db.InPlaceCompaction, conversationID string, recordSeq int64) ([]contextItem, error) {
	out := append([]contextItem(nil), items...)
	for _, t := range c.Trims {
		i := indexOfItem(out, func(it contextItem) bool { return it.source != nil && it.from == t.SequenceID })
		if i < 0 {
			return nil, fmt.Errorf("trim: no message with sequence id %d in context", t.SequenceID)
		}
		trimmed, ok := trimToolResult(out[i].message, t.ToolUseID, trimmedToolOutputText(conversationID, t.SequenceID))
		if !ok {
			return nil, fmt.Errorf("trim: message %d has no tool result for %s", t.SequenceID, t.ToolUseID)
		}
		out[i].message = trimmed
	}
	for n, s := range c.Squishes {
		i := indexOfItem(out, func(it contextItem) bool { return it.from == s.FromSequenceID })
		j := indexOfItem(out, func(it contextItem) bool { return it.to == s.ToSequenceID })
		if i < 0 || j < 0 || j < i {
			return nil, fmt.Errorf("squish: range %d-%d does not align with messages in context", s.FromSequenceID, s.ToSequenceID)
		}
		if hasContent(out[i].message, llm.ContentTypeToolResult) {
			return nil, fmt.Errorf("squish: range %d-%d starts with a tool result whose tool use precedes it", s.FromSequenceID, s.ToSequenceID)
		}
		if hasContent(out[j].message, llm.ContentTypeToolUse) {
			return nil, fmt.Errorf("squish: range %d-%d ends with a tool use whose result follows it", s.FromSequenceID, s.ToSequenceID)
		}
		summary := contextItem{
			from:   s.FromSequenceID,
			to:     s.ToSequenceID,
			noteID: fmt.Sprintf("s%d.%d", recordSeq, n),
			note:   s.Summary,
			message: llm.Message{
				Role:    llm.MessageRoleUser,
				Content: []llm.Content{{Type: llm.ContentTypeText, Text: compactionNoteText(conversationID, s.FromSequenceID, s.ToSequenceID, s.Summary)}},
			},
		}
		out = append(out[:i], append([]contextItem{summary}, out[j+1:]...)...)
	}
	return hideItems(out, c.HiddenSequenceIDs, c.HiddenToolUseIDs)
}

// hideItems drops the original messages with sequence ids seqs, and the tool
// calls toolUseIDs with their results, from items (which it may modify).
func hideItems(items []contextItem, seqs []int64, toolUseIDs []string) ([]contextItem, error) {
	for _, seq := range seqs {
		i := indexOfItem(items, func(it contextItem) bool { return it.source != nil && it.from == seq })
		if i < 0 {
			return nil, fmt.Errorf("hide: no message with sequence id %d in context", seq)
		}
		items = append(items[:i:i], items[i+1:]...)
	}
	for _, id := range toolUseIDs {
		found := 0
		for i := range items {
			before := len(items[i].message.Content)
			content := slices.DeleteFunc(slices.Clone(items[i].message.Content), func(c llm.Content) bool {
				return c.Type == llm.ContentTypeToolUse && c.ID == id || c.Type == llm.ContentTypeToolResult && c.ToolUseID == id
			})
			if len(content) == before {
				continue
			}
			found++
			items[i].message.Content = content
			// A call's message is about the call; without one it goes.
			if items[i].message.Role == llm.MessageRoleAssistant && !hasContent(items[i].message, llm.ContentTypeToolUse) {
				items[i].message.Content = nil
			}
		}
		if found != 2 {
			return nil, fmt.Errorf("hide: tool call %s and its result are not both in context", id)
		}
	}
	return slices.DeleteFunc(items, func(it contextItem) bool { return len(it.message.Content) == 0 }), nil
}

func indexOfItem(items []contextItem, match func(contextItem) bool) int {
	for i, it := range items {
		if match(it) {
			return i
		}
	}
	return -1
}

func hasContent(m llm.Message, t llm.ContentType) bool {
	for _, c := range m.Content {
		if c.Type == t {
			return true
		}
	}
	return false
}

// trimToolResult returns a copy of m with the content of the tool result for
// toolUseID replaced by replacement, and whether m had that tool result.
func trimToolResult(m llm.Message, toolUseID, replacement string) (llm.Message, bool) {
	content := append([]llm.Content(nil), m.Content...)
	for i := range content {
		if content[i].Type == llm.ContentTypeToolResult && content[i].ToolUseID == toolUseID {
			content[i].ToolResult = []llm.Content{{Type: llm.ContentTypeText, Text: replacement}}
			m.Content = content
			return m, true
		}
	}
	return m, false
}

var errCompactWhileWorking = errors.New("cannot compact while the agent is working")

// loadContextItems returns the LLM's current view of the conversation.
func (cm *ConversationManager) loadContextItems(ctx context.Context) ([]contextItem, error) {
	var rows []generated.Message
	err := cm.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		rows, err = q.ListMessagesForContext(ctx, cm.conversationID)
		return err
	})
	if err != nil {
		return nil, err
	}
	items, _, err := cm.contextItems(rows)
	return items, err
}

// createInPlaceCompaction appends c as an in-place compaction record. The
// caller validates c and publishes the row (publishCreated).
func (cm *ConversationManager) createInPlaceCompaction(ctx context.Context, c db.InPlaceCompaction) (*generated.Message, error) {
	created, err := cm.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: cm.conversationID,
		Type:           db.MessageTypeInPlaceCompaction,
		UserData:       c,
		BumpTimestamp:  true,
	})
	if err != nil {
		return nil, err
	}
	cm.Touch()
	return created, nil
}

// publishCreated sends a row this manager created to stream subscribers.
func (cm *ConversationManager) publishCreated(ctx context.Context, created *generated.Message) error {
	var conversation generated.Conversation
	if err := cm.db.Queries(ctx, func(q *generated.Queries) error {
		var qerr error
		conversation, qerr = q.GetConversation(ctx, cm.conversationID)
		return qerr
	}); err != nil {
		return err
	}
	cm.publishStream(created.SequenceID, StreamResponse{
		Messages:     toAPIMessages([]generated.Message{*created}),
		Conversation: &conversation,
	})
	return nil
}

// RecordInPlaceCompaction validates c against the current context, appends it
// as an in-place compaction record, and drops the loop so the next turn
// rebuilds its history from the DB. It refuses while a turn is running.
func (cm *ConversationManager) RecordInPlaceCompaction(ctx context.Context, c db.InPlaceCompaction) error {
	var created *generated.Message
	err := cm.resetLoopAfter(true, func() error {
		if cm.IsAgentWorking() {
			return errCompactWhileWorking
		}
		items, err := cm.loadContextItems(ctx)
		if err != nil {
			return err
		}
		if _, err := applyInPlaceCompaction(items, c, cm.conversationID, 0); err != nil {
			return err
		}
		created, err = cm.createInPlaceCompaction(ctx, c)
		return err
	})
	if err != nil {
		return err
	}
	return cm.publishCreated(ctx, created)
}

const compactDebugUsage = "usage: /compact-debug | /compact-debug squish <from>-<to> <summary> | /compact-debug trim <id>"

// handleCompactDebugCommand intercepts "/compact-debug", a testing aid for
// in-place compaction. It reports whether message was the command (and the
// response has been written).
//
//	/compact-debug                          — shows the compact_in_place index as a warning
//	/compact-debug squish <from>-<to> <sum> — squishes a sequence-id range into sum
//	/compact-debug trim <id>                — trims the tool outputs of an index row
func (s *Server) handleCompactDebugCommand(ctx context.Context, w http.ResponseWriter, manager *ConversationManager, message string) bool {
	fields := strings.Fields(message)
	if len(fields) == 0 || fields[0] != "/compact-debug" {
		return false
	}
	var c db.InPlaceCompaction
	var err error
	switch {
	case len(fields) == 1:
		var index string
		if index, err = manager.contextIndex(ctx); err == nil {
			err = manager.recordWarning(ctx, index)
		}
		if err != nil {
			s.internalError(w, "Failed to list context", err, "conversationID", manager.conversationID)
			return true
		}
		w.WriteHeader(http.StatusAccepted)
		return true
	case fields[1] == "squish" && len(fields) >= 4:
		var sq db.CompactionSquish
		if _, err = fmt.Sscanf(fields[2], "%d-%d", &sq.FromSequenceID, &sq.ToSequenceID); err != nil {
			http.Error(w, compactDebugUsage, http.StatusBadRequest)
			return true
		}
		// Keep the summary verbatim, whitespace included.
		sq.Summary = strings.TrimSpace(message[strings.Index(message, fields[2])+len(fields[2]):])
		c.Squishes = []db.CompactionSquish{sq}
	case fields[1] == "trim" && len(fields) == 3:
		var items []contextItem
		if items, err = manager.loadContextItems(ctx); err != nil {
			s.internalError(w, "Failed to list context", err, "conversationID", manager.conversationID)
			return true
		}
		var view []contextItem
		if view, err = compactionView(items); err != nil {
			s.internalError(w, "Failed to list context", err, "conversationID", manager.conversationID)
			return true
		}
		rows, _ := splitRows(view, len(view))
		r := indexOfRow(rows, claudetool.IndexID(fields[2]))
		if r < 0 {
			http.Error(w, "compact-debug: no index row "+fields[2], http.StatusBadRequest)
			return true
		}
		if c.Trims = rowTrims(rows[r]); len(c.Trims) == 0 {
			http.Error(w, "compact-debug: no tool output to trim in "+fields[2], http.StatusBadRequest)
			return true
		}
	default:
		http.Error(w, compactDebugUsage, http.StatusBadRequest)
		return true
	}
	if err := manager.RecordInPlaceCompaction(ctx, c); err != nil {
		http.Error(w, "compact-debug: "+err.Error(), http.StatusBadRequest)
		return true
	}
	s.broadcastEstimatedContextSize(ctx, manager.conversationID)
	w.WriteHeader(http.StatusAccepted)
	return true
}
