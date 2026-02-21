package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// ClaudeCodeRunner implements a SubagentRunner using the external claude CLI.
type ClaudeCodeRunner struct {
	server *Server
}

func NewClaudeCodeRunner(s *Server) *ClaudeCodeRunner {
	return &ClaudeCodeRunner{server: s}
}

func mcpConfigJSON(port string, conversationID string) string {
	url := fmt.Sprintf("http://localhost:%s/api/conversation/%s/mcp", port, conversationID)
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"shelley": map[string]any{
				"type": "http",
				"url":  url,
			},
		},
	}
	b, _ := json.Marshal(cfg)
	return string(b)
}

func (r *ClaudeCodeRunner) RunSubagent(ctx context.Context, conversationID, prompt string, wait bool, timeout time.Duration) (string, error) {
	s := r.server

	go func() {
		s.mu.Lock()
		_, ok := s.activeConversations[conversationID]
		s.mu.Unlock()
		if ok {
			s.logger.Info("Notifying subagent conversation", "conversationID", conversationID)
		}
	}()

	if !wait {
		go r.run(context.Background(), conversationID, prompt, timeout)
		return fmt.Sprintf("Claude Code subagent started processing. Conversation ID: %s", conversationID), nil
	}
	return r.run(ctx, conversationID, prompt, timeout)
}

type claudeStreamMessage struct {
	Role    string `json:"role"`
	Content []struct {
		Type      string          `json:"type"`
		Text      string          `json:"text,omitempty"`
		ID        string          `json:"id,omitempty"`
		Name      string          `json:"name,omitempty"`
		Input     json.RawMessage `json:"input,omitempty"`
		ToolUseID string          `json:"tool_use_id,omitempty"`
		Content   string          `json:"content,omitempty"`
		IsError   bool            `json:"is_error,omitempty"`
	} `json:"content"`
}

type subagentStreamEvent struct {
	Type    string              `json:"type"`
	Message claudeStreamMessage `json:"message,omitempty"`
	Result  string              `json:"result,omitempty"`
}

func (r *ClaudeCodeRunner) run(ctx context.Context, conversationID, prompt string, timeout time.Duration) (string, error) {
	s := r.server

	manager, err := s.getOrCreateSubagentConversationManager(ctx, conversationID)
	if err != nil {
		return "", fmt.Errorf("failed to get conversation manager: %w", err)
	}

	port := "8000"
	parts := strings.Split(s.listenAddr, ":")
	if len(parts) > 1 {
		port = parts[len(parts)-1]
	}

	tmpDir := os.TempDir()
	mcpFile := filepath.Join(tmpDir, fmt.Sprintf("mcp_%s.json", conversationID))
	os.WriteFile(mcpFile, []byte(mcpConfigJSON(port, conversationID)), 0600)
	defer os.Remove(mcpFile)

	claudeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(claudeCtx, "claude",
		"--print", "--verbose",
		"--output-format", "stream-json",
		"--max-turns", "25",
		"--permission-mode", "bypassPermissions",
		"--mcp-config", mcpFile,
		"-p", prompt,
	)

	manager.mu.Lock()
	if manager.toolSet == nil {
		manager.toolSet = claudetool.NewToolSet(ctx, manager.toolSetConfig)
	}
	cwd := manager.toolSet.WorkingDir().Get()
	manager.mu.Unlock()
	cmd.Dir = cwd

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start claude: %w", err)
	}

	manager.SetAgentWorking(true)
	defer manager.SetAgentWorking(false)

	userMsg := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: prompt}},
	}
	createdUserMsg, err := s.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conversationID,
		Type:           db.MessageTypeUser,
		LLMData:        userMsg,
	})
	if err == nil {
		var conv generated.Conversation
		s.db.Queries(ctx, func(q *generated.Queries) error {
			var err error
			conv, err = q.GetConversation(ctx, conversationID)
			return err
		})
		manager.subpub.Publish(createdUserMsg.SequenceID, StreamResponse{
			Messages:     toAPIMessages([]generated.Message{*createdUserMsg}),
			Conversation: conv,
		})
	}

	var finalResult string
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var ev subagentStreamEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}

		if ev.Type == "assistant" || ev.Type == "user" {
			var msg llm.Message
			msg.Role = llm.MessageRoleAssistant
			msgType := db.MessageTypeAgent
			if ev.Type == "user" {
				msg.Role = llm.MessageRoleUser
				msgType = db.MessageTypeUser
			}

			hasContent := false
			for _, c := range ev.Message.Content {
				switch c.Type {
				case "text":
					msg.Content = append(msg.Content, llm.Content{
						Type: llm.ContentTypeText,
						Text: c.Text,
					})
					hasContent = true
				case "tool_use":
					msg.Content = append(msg.Content, llm.Content{
						Type:      llm.ContentTypeToolUse,
						ID:        c.ID,
						ToolName:  c.Name,
						ToolInput: c.Input,
					})
					hasContent = true
				case "tool_result":
					msg.Content = append(msg.Content, llm.Content{
						Type:       llm.ContentTypeToolResult,
						ToolUseID:  c.ToolUseID,
						ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: c.Content}},
						ToolError:  c.IsError,
					})
					hasContent = true
				}
			}

			if hasContent {
				createdMsg, err := s.db.CreateMessage(ctx, db.CreateMessageParams{
					ConversationID: conversationID,
					Type:           msgType,
					LLMData:        msg,
				})
				if err == nil {
					var conv generated.Conversation
					s.db.Queries(ctx, func(q *generated.Queries) error {
						var err error
						conv, err = q.GetConversation(ctx, conversationID)
						return err
					})
					manager.subpub.Publish(createdMsg.SequenceID, StreamResponse{
						Messages:     toAPIMessages([]generated.Message{*createdMsg}),
						Conversation: conv,
					})
				}
			}
		} else if ev.Type == "result" {
			finalResult = ev.Result
		}
	}

	err = cmd.Wait()
	if err != nil && finalResult == "" {
		return "", fmt.Errorf("claude code error: %w", err)
	}

	return finalResult, nil
}
