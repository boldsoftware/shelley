package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

const (
	liveContextMessages  = 6    // user requests and final assistant replies kept
	liveContextTailRows  = 100  // rows scanned (tool rows included) to find them
	liveContextItemRunes = 800  // per-item cap
	liveContextRunes     = 4000 // total budget; keeps CJK/code well under Live's 8192-token input cap
)

// liveHistoryItem is one prior text turn in Live's session.input format.
type liveHistoryItem struct {
	Type    string            `json:"type"`
	Role    string            `json:"role"`
	Content []liveHistoryText `json:"content"`
}

type liveHistoryText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// liveContext returns the recent human requests and final assistant replies of
// a conversation. Tool calls and results, thinking, system prompts and
// automated user notices (background jobs, MCP changes) are left out.
func (s *Server) liveContext(ctx context.Context, conversationID string) ([]liveHistoryItem, error) {
	var rows []generated.Message
	err := s.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		rows, err = q.ListMessagesTail(ctx, generated.ListMessagesTailParams{ConversationID: conversationID, Limit: liveContextTailRows})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("load live context: %w", err)
	}
	var items []liveHistoryItem
	for _, row := range rows {
		role, contentType := "", ""
		switch row.Type {
		case "user":
			role, contentType = "user", "input_text"
		case "agent":
			role, contentType = "assistant", "output_text"
		default:
			continue
		}
		if row.LlmData == nil || row.ExcludedFromContext {
			continue
		}
		if row.Type == "user" && automatedUserData(row.UserData) {
			continue
		}
		var message llm.Message
		if err := json.Unmarshal([]byte(*row.LlmData), &message); err != nil {
			return nil, fmt.Errorf("decode message %s: %w", row.MessageID, err)
		}
		if row.Type == "agent" && !message.EndOfTurn {
			continue
		}
		var text []string
		for _, c := range message.Content {
			if c.Type == llm.ContentTypeText && strings.TrimSpace(c.Text) != "" {
				text = append(text, strings.TrimSpace(c.Text))
			}
		}
		if len(text) == 0 {
			continue
		}
		items = append(items, liveHistoryItem{
			Type:    "message",
			Role:    role,
			Content: []liveHistoryText{{Type: contentType, Text: truncateRunes(strings.Join(text, "\n\n"), liveContextItemRunes)}},
		})
	}
	// Keep the newest items that fit the count and rune budgets.
	start, used := len(items), 0
	for start > 0 && len(items)-start < liveContextMessages {
		n := utf8.RuneCountInString(items[start-1].Content[0].Text)
		if used+n > liveContextRunes {
			break
		}
		used += n
		start--
	}
	return items[start:], nil
}

func automatedUserData(userData *string) bool {
	if userData == nil {
		return false
	}
	var marker struct {
		BackgroundJobID string `json:"background_job_id"`
		MCPServerChange string `json:"mcp_server_change"`
	}
	return json.Unmarshal([]byte(*userData), &marker) == nil && (marker.BackgroundJobID != "" || marker.MCPServerChange != "")
}

func truncateRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}

func liveHistoryTranscript(items []liveHistoryItem) string {
	var b strings.Builder
	for _, item := range items {
		fmt.Fprintf(&b, "%s: %s\n\n", strings.ToUpper(item.Role), item.Content[0].Text)
	}
	return strings.TrimSpace(b.String())
}

// liveModelID is the model Shelley runs the conversation on.
func (s *Server) liveModelID(conversation *generated.Conversation) string {
	if conversation.Model != nil && *conversation.Model != "" {
		return *conversation.Model
	}
	return s.effectiveDefaultModel(s.getModelList())
}

// liveSituation is the bounded state line shared by Live's instructions and
// the rewrite prompt, so both resolve "that file" against the same facts.
func (s *Server) liveSituation(conversation *generated.Conversation) string {
	state := "idle"
	if conversation.AgentWorking {
		state = "working"
	}
	situation := "Shelley is currently " + state + "."
	if conversation.Cwd != nil && *conversation.Cwd != "" {
		situation += " Working directory: " + truncateRunes(*conversation.Cwd, 300) + "."
	}
	return situation + " Shelley's model: " + truncateRunes(s.liveModelID(conversation), 100) + "."
}
