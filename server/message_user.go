package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// userMessageFinder is the claudetool.UserMessageFinder of one conversation.
type userMessageFinder struct {
	db             *db.DB
	conversationID string
}

// userMessageFinder returns the finder that provides the message_user tool,
// or nil: only top-level conversations talk to the user.
func (cm *ConversationManager) userMessageFinder() claudetool.UserMessageFinder {
	if cm.role != roleTopLevel {
		return nil
	}
	return userMessageFinder{db: cm.db, conversationID: cm.conversationID}
}

// showsSequenceIDs reports whether the model sees the sequence_id of each
// message the user typed, which message_user's reply_to names. Only
// conversations offering message_user do; elsewhere the model sees exactly
// what the user typed.
func (cm *ConversationManager) showsSequenceIDs() bool {
	if cm.userMessageFinder() == nil {
		return false
	}
	cm.mu.Lock()
	opts := cm.conversationOptions
	cm.mu.Unlock()
	return claudetool.IsToolEnabled(claudetool.MessageUserName, opts.ToolOverrides, opts.DisableAllTools)
}

func (f userMessageFinder) FindUserMessageBySequence(ctx context.Context, sequenceID int64) (claudetool.UserMessage, bool, error) {
	msgs, err := f.db.ListTypedUserMessages(ctx, f.conversationID)
	if err != nil {
		return claudetool.UserMessage{}, false, err
	}
	for _, m := range msgs {
		origin, err := originSequenceID(m)
		if err != nil {
			return claudetool.UserMessage{}, false, fmt.Errorf("message %s: %w", m.MessageID, err)
		}
		if m.SequenceID != sequenceID && origin != sequenceID {
			continue
		}
		msg, err := parseLLMData(m.LlmData)
		if err != nil {
			return claudetool.UserMessage{}, false, fmt.Errorf("message %s: %w", m.MessageID, err)
		}
		um, err := typedUserMessage(m, userMessageText(*msg))
		return um, err == nil, err
	}
	return claudetool.UserMessage{}, false, nil
}

func typedUserMessage(m generated.Message, text string) (claudetool.UserMessage, error) {
	seq, err := originSequenceID(m)
	if err != nil {
		return claudetool.UserMessage{}, fmt.Errorf("message %s: %w", m.MessageID, err)
	}
	um := claudetool.UserMessage{ID: m.MessageID, SequenceID: seq, Text: text}
	if m.ExternalMessageID != nil {
		um.ExternalID = *m.ExternalMessageID
	}
	return um, nil
}

// carriedFromKey is the user_data key of a compaction's copy that holds the
// sequence_id of the message first copied.
const carriedFromKey = "carried_from_sequence_id"

// originSequenceID is the sequence_id of the message m is a copy of, or of m.
func originSequenceID(m generated.Message) (int64, error) {
	if m.UserData == nil {
		return m.SequenceID, nil
	}
	var ud map[string]string
	if err := json.Unmarshal([]byte(*m.UserData), &ud); err != nil {
		return 0, err
	}
	if from, ok := ud[carriedFromKey]; ok {
		return strconv.ParseInt(from, 10, 64)
	}
	return m.SequenceID, nil
}

// userMessageText is the text of a typed user message, as the UI shows it.
func userMessageText(m llm.Message) string {
	var parts []string
	for _, c := range m.Content {
		if c.Type == llm.ContentTypeText && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// handleMessageAttachment serves a file the agent sent with message_user.
// Route: GET /api/message/{message_id}/attachment?path=<path>
//
// message_id is the tool-result message of the call. The capability is that
// call's Display: only files the tool validated and listed are served. Images
// are served inline, for previews; anything else, or any file with
// ?download=1, as an attachment, so that it never renders on Shelley's origin.
func (s *Server) handleMessageAttachment(w http.ResponseWriter, r *http.Request) {
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	paths, convID, err := s.messageUserAttachmentPaths(r.Context(), r.PathValue("message_id"))
	if err != nil || !slices.Contains(paths, reqPath) {
		http.Error(w, "not an attachment of this message", http.StatusNotFound)
		return
	}
	// message_user reads its attachments here, on a VM conversation too.
	f, fi, err := openHostFile(s.conversationCwd(r.Context(), convID), reqPath)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()

	head := make([]byte, sniffLen)
	n, _ := f.Read(head)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		http.Error(w, "seek failed", http.StatusInternalServerError)
		return
	}
	contentType, image := servableImageType(head[:n], fi.Name())
	if image && r.URL.Query().Get("download") == "" {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": fi.Name()}))
		w.Header().Set("Content-Security-Policy", "sandbox")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-cache")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// messageUserAttachmentPaths returns the attachments of the message_user
// calls answered by the tool-result message messageID, and its conversation.
// Tool results do not name their tool, so the tool is read off the agent
// message whose calls they answer.
func (s *Server) messageUserAttachmentPaths(ctx context.Context, messageID string) ([]string, string, error) {
	row, err := s.db.GetMessageByID(ctx, messageID)
	if err != nil {
		return nil, "", err
	}
	results, err := parseLLMData(row.LlmData)
	if err != nil {
		return nil, "", err
	}
	call, err := s.db.GetAgentMessageBefore(ctx, row.ConversationID, row.SequenceID)
	if err != nil {
		return nil, "", err
	}
	calls, err := parseLLMData(call.LlmData)
	if err != nil {
		return nil, "", err
	}
	var paths []string
	for _, c := range results.Content {
		if c.Type != llm.ContentTypeToolResult || c.ToolError || c.Display == nil {
			continue
		}
		if !slices.ContainsFunc(calls.Content, func(u llm.Content) bool {
			return u.Type == llm.ContentTypeToolUse && u.ID == c.ToolUseID && u.ToolName == claudetool.MessageUserName
		}) {
			continue
		}
		// Display was decoded generically; round-trip it into its type.
		b, err := json.Marshal(c.Display)
		if err != nil {
			return nil, "", err
		}
		var d claudetool.MessageUserDisplay
		if err := json.Unmarshal(b, &d); err != nil {
			return nil, "", err
		}
		for _, a := range d.Attachments {
			paths = append(paths, a.Path)
		}
	}
	return paths, row.ConversationID, nil
}

func parseLLMData(data *string) (*llm.Message, error) {
	if data == nil {
		return nil, fmt.Errorf("message has no llm_data")
	}
	m := new(llm.Message)
	if err := json.Unmarshal([]byte(*data), m); err != nil {
		return nil, err
	}
	return m, nil
}
