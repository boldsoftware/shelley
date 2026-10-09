package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

type senderRelationship string

const (
	senderRelationshipSubagent senderRelationship = "subagent"
	senderRelationshipParent   senderRelationship = "parent"
)

type senderMessageUserData struct {
	SenderConversationID string             `json:"sender_conversation_id"`
	SenderSlug           string             `json:"sender_slug"`
	SenderRelationship   senderRelationship `json:"sender_relationship"`
	// Text duplicates the final user message because the FTS trigger searches
	// user_data instead of llm_data whenever user_data is present.
	Text string `json:"Text"`
}

// backgroundJobUserData marks a notice that a backgrounded bash command
// finished; it is not from a human or another conversation.
type backgroundJobUserData struct {
	BackgroundJobID string `json:"background_job_id"`
	Command         string `json:"command"`
	// ExitCode is absent when the job was lost.
	ExitCode *int `json:"exit_code,omitempty"`
	// Duration is the job's run time as a Go duration string, or "" when
	// the job was lost.
	Duration string `json:"duration"`
	LogPath  string `json:"log_path"`
	// Tail is the last lines of the job's output.
	Tail string `json:"tail"`
	// Text duplicates the message for full-text search, as in
	// senderMessageUserData.
	Text string `json:"Text"`
}

// mcpNoticeUserData marks a notice that the registered MCP servers changed
// while the agent was working; it is not from a human. MCPServerChange is
// "added", "updated" or "removed".
type mcpNoticeUserData struct {
	MCPServerChange string `json:"mcp_server_change"`
	ServerName      string `json:"server_name"`
	// Text duplicates the message for full-text search, as in
	// senderMessageUserData.
	Text string `json:"Text"`
}

func provenanceEligibleConversation(conversation generated.Conversation) bool {
	return !isBtwReader(conversation) && db.ParseConversationOptions(conversation.ConversationOptions).Kind != transcriptionKind
}

// senderUserData validates CLI-supplied sender provenance against the
// conversation graph. Only the trusted local CLI may identify either side of
// a direct parent/managed-child relationship.
func (s *Server) senderUserData(ctx context.Context, target generated.Conversation, senderConversationID string) (*senderMessageUserData, error) {
	if !isLocalCLIRequest(ctx) || senderConversationID == "" || senderConversationID == target.ConversationID || !provenanceEligibleConversation(target) {
		return nil, nil
	}
	var sender generated.Conversation
	err := s.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		sender, err = q.GetConversation(ctx, senderConversationID)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load chat sender %s: %w", senderConversationID, err)
	}
	if !provenanceEligibleConversation(sender) {
		return nil, nil
	}

	var relationship senderRelationship
	switch {
	case isManagedChild(sender) && sender.ParentConversationID != nil && *sender.ParentConversationID == target.ConversationID:
		relationship = senderRelationshipSubagent
	case isManagedChild(target) && target.ParentConversationID != nil && *target.ParentConversationID == senderConversationID:
		relationship = senderRelationshipParent
	default:
		return nil, nil
	}
	return &senderMessageUserData{
		SenderConversationID: senderConversationID,
		SenderSlug:           derefString(sender.Slug),
		SenderRelationship:   relationship,
	}, nil
}

func parseSenderMessageUserData(raw []byte) (senderMessageUserData, bool, error) {
	if len(raw) == 0 {
		return senderMessageUserData{}, false, nil
	}
	var data senderMessageUserData
	if err := json.Unmarshal(raw, &data); err != nil {
		return senderMessageUserData{}, false, fmt.Errorf("decode sender provenance: %w", err)
	}
	if data.SenderConversationID == "" && data.SenderSlug == "" && data.SenderRelationship == "" {
		return senderMessageUserData{}, false, nil
	}
	if data.SenderConversationID == "" ||
		(data.SenderRelationship != senderRelationshipSubagent && data.SenderRelationship != senderRelationshipParent) {
		return senderMessageUserData{}, false, fmt.Errorf("incomplete sender provenance")
	}
	return data, true, nil
}

func xmlProvenanceOpeningTag(tag string, attrs ...xml.Attr) (string, error) {
	var out bytes.Buffer
	encoder := xml.NewEncoder(&out)
	if err := encoder.EncodeToken(xml.StartElement{Name: xml.Name{Local: tag}, Attr: attrs}); err != nil {
		return "", err
	}
	if err := encoder.Flush(); err != nil {
		return "", err
	}
	return out.String(), nil
}

// Element text needs only markup delimiters escaped. Keep whitespace and quotes
// literal so code blocks and structured reports stay readable to the model.
var provenanceTextEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func xmlProvenanceText(text string) string {
	return provenanceTextEscaper.Replace(text)
}

// provenanceTag returns the XML element that wraps a message with the given
// user_data for the model, or "" if the message is a plain user message.
func provenanceTag(rawUserData []byte) (string, []xml.Attr, error) {
	if len(rawUserData) > 0 {
		var job backgroundJobUserData
		if err := json.Unmarshal(rawUserData, &job); err == nil && job.BackgroundJobID != "" {
			return "background_job", []xml.Attr{{Name: xml.Name{Local: "id"}, Value: job.BackgroundJobID}}, nil
		}
		var mcpN mcpNoticeUserData
		if err := json.Unmarshal(rawUserData, &mcpN); err == nil && mcpN.MCPServerChange != "" {
			return "mcp_servers_changed", []xml.Attr{
				{Name: xml.Name{Local: "server"}, Value: mcpN.ServerName},
				{Name: xml.Name{Local: "change"}, Value: mcpN.MCPServerChange},
			}, nil
		}
	}
	data, ok, err := parseSenderMessageUserData(rawUserData)
	if err != nil || !ok {
		return "", nil, err
	}
	tag := "subagent_message"
	if data.SenderRelationship == senderRelationshipParent {
		tag = "parent_message"
	}
	return tag, []xml.Attr{
		{Name: xml.Name{Local: "conversation_id"}, Value: data.SenderConversationID},
		{Name: xml.Name{Local: "slug"}, Value: data.SenderSlug},
	}, nil
}

func messageWithSenderProvenance(message llm.Message, rawUserData []byte) (llm.Message, error) {
	tag, attrs, err := provenanceTag(rawUserData)
	if err != nil || tag == "" {
		return message, err
	}
	return wrapMessageForLLM(message, tag, attrs...)
}

// messageWithSequenceID shows the stable sequence ID of a typed user message
// to the LLM, without changing its persisted or UI text. For channel messages,
// the transport's own ID stays out of the prompt and is resolved on reply.
func messageWithSequenceID(message llm.Message, externalID string, sequenceID int64) (llm.Message, error) {
	tag := "user_message"
	if externalID != "" {
		tag = "external_message"
	}
	return wrapMessageForLLM(message, tag, xml.Attr{
		Name: xml.Name{Local: "sequence_id"}, Value: strconv.FormatInt(sequenceID, 10),
	})
}

// typedUserForLLM mirrors ListTypedUserMessages: machine notices and tool
// results aren't reply targets, even when they have the user role. Compaction
// copies are still the user's messages.
func typedUserForLLM(message llm.Message, rawUserData []byte) bool {
	if message.Role != llm.MessageRoleUser {
		return false
	}
	for _, content := range message.Content {
		if content.Type == llm.ContentTypeToolResult {
			return false
		}
	}
	if len(rawUserData) == 0 || string(rawUserData) == "null" {
		return true
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(rawUserData, &data); err != nil {
		return false
	}
	for key := range data {
		if key != "compaction_carried" && key != carriedFromKey {
			return false
		}
	}
	return true
}

func wrapMessageForLLM(message llm.Message, tag string, attrs ...xml.Attr) (llm.Message, error) {
	opening, err := xmlProvenanceOpeningTag(tag, attrs...)
	if err != nil {
		return message, fmt.Errorf("encode %s tag: %w", tag, err)
	}
	closing := "</" + tag + ">"

	message.Content = append([]llm.Content(nil), message.Content...)
	firstText, lastText := -1, -1
	for i := range message.Content {
		if message.Content[i].Type != llm.ContentTypeText || message.Content[i].MediaType != "" {
			continue
		}
		message.Content[i].Text = xmlProvenanceText(message.Content[i].Text)
		if firstText < 0 {
			firstText = i
		}
		lastText = i
	}
	if firstText < 0 {
		if tag != "user_message" && tag != "external_message" {
			return message, fmt.Errorf("%s message has no text content", tag)
		}
		message.Content = append([]llm.Content{{Type: llm.ContentTypeText, Text: opening}}, message.Content...)
		message.Content = append(message.Content, llm.Content{Type: llm.ContentTypeText, Text: closing})
		return message, nil
	}
	message.Content[firstText].Text = opening + "\n" + message.Content[firstText].Text
	message.Content[lastText].Text += "\n" + closing
	return message, nil
}

func messageWithContextSenderProvenance(ctx context.Context, message llm.Message) (llm.Message, error) {
	raw, err := marshalTurnUserData(ctx)
	if err != nil {
		return message, err
	}
	return messageWithSenderProvenance(message, raw)
}
