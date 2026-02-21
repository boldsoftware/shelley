package server

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"shelley.exe.dev/llm"
)

type claudeCodeLoop struct {
	cm      *ConversationManager
	msgChan chan llm.Message
	mu      sync.Mutex
	history []llm.Message
}

func newClaudeCodeLoop(cm *ConversationManager) *claudeCodeLoop {
	return &claudeCodeLoop{
		cm:      cm,
		msgChan: make(chan llm.Message, 100),
	}
}

// ccTaskInput holds the fields from CC's Task tool input that we need for bridging.
type ccTaskInput struct {
	Description  string `json:"description"`
	Prompt       string `json:"prompt"`
	SubagentType string `json:"subagent_type"`
}

// ccSubagentSession tracks an in-flight CC Task subagent so its messages can be
// recorded into a Shelley subconversation and streamed to the frontend.
type ccSubagentSession struct {
	manager          *ConversationManager
	lastMsgID        string
	pendingAssistant *llm.Message
	pendingUser      *llm.Message
}

// ccDescriptionToSlug converts a Task tool description into a slug suitable for
// use as a Shelley subconversation identifier.
func ccDescriptionToSlug(description string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(description) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == ' ', r == '_':
			b.WriteRune('-')
		}
	}
	s := b.String()
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

func conversationIDToUUID(id string) string {
	h := sha256.Sum256([]byte(id))
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

func (l *claudeCodeLoop) QueueUserMessage(msg llm.Message) {
	l.msgChan <- msg
}

func (l *claudeCodeLoop) GetHistory() []llm.Message {
	// Not fully implemented for cancellation tracking yet
	l.mu.Lock()
	defer l.mu.Unlock()
	historyCopy := make([]llm.Message, len(l.history))
	copy(historyCopy, l.history)
	return historyCopy
}

func (l *claudeCodeLoop) Go(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg := <-l.msgChan:
			if err := l.processMessage(ctx, msg); err != nil {
				l.cm.logger.Error("ClaudeCode loop processing error", "error", err)
			}
		}
	}
}

func (l *claudeCodeLoop) processMessage(ctx context.Context, msg llm.Message) error {
	l.cm.SetAgentWorking(true)
	defer l.cm.SetAgentWorking(false)

	// Extract prompt from msg
	prompt := ""
	for _, c := range msg.Content {
		if c.Type == llm.ContentTypeText {
			prompt += c.Text
		}
	}

	tmpDir := os.TempDir()
	mcpFile := filepath.Join(tmpDir, fmt.Sprintf("mcp_%s.json", l.cm.conversationID))

	// Create MCP config
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"shelley": map[string]any{
				"type": "http",
				"url":  l.cm.mcpURL,
			},
		},
	}
	b, _ := json.Marshal(cfg)
	os.WriteFile(mcpFile, b, 0600)
	defer os.Remove(mcpFile)

	// Generate deterministic UUID for Claude's session persistence
	sessionID := conversationIDToUUID(l.cm.conversationID)

	l.mu.Lock()
	isFirstTurn := len(l.history) == 0
	l.mu.Unlock()

	args := []string{
		"-p", prompt,
		"--verbose",
		"--output-format", "stream-json",
		"--max-turns", "25",
		"--permission-mode", "bypassPermissions",
		"--mcp-config", mcpFile,
		"--include-partial-messages", // Stream text tokens
	}

	if isFirstTurn {
		args = append(args, "--session-id", sessionID)
	} else {
		args = append(args, "--resume", sessionID)
	}

	// Build the command
	cmd := exec.CommandContext(ctx, "claude", args...)

	l.cm.mu.Lock()
	cwd := l.cm.cwd
	l.cm.mu.Unlock()
	cmd.Dir = cwd

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start claude: %w", err)
	}

	if err := l.processStream(ctx, stdout); err != nil {
		l.cm.logger.Error("Stream processing error", "error", err)
	}
	return cmd.Wait()
}

// processStream reads a Claude Code stream-json stream from r and records messages
// into the main conversation and any CC Task subagent conversations it detects.
// It is split out of processMessage so it can be tested independently with testdata.
func (l *claudeCodeLoop) processStream(ctx context.Context, r io.Reader) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	var currentAssistantMsg *llm.Message
	var currentUserMsg *llm.Message
	var currentMsgID string

	flushAssistant := func() {
		if currentAssistantMsg != nil {
			hasToolUse := false
			for _, c := range currentAssistantMsg.Content {
				if c.Type == llm.ContentTypeToolUse {
					hasToolUse = true
					break
				}
			}
			currentAssistantMsg.EndOfTurn = !hasToolUse

			l.cm.mu.Lock()
			recordMsg := l.cm.recordMessage
			l.cm.mu.Unlock()

			if recordMsg != nil {
				if err := recordMsg(ctx, *currentAssistantMsg, llm.Usage{}); err != nil {
					l.cm.logger.Error("Failed to record claude assistant response", "error", err)
				}
			}
			l.mu.Lock()
			l.history = append(l.history, *currentAssistantMsg)
			l.mu.Unlock()
			currentAssistantMsg = nil
		}
	}

	flushUser := func() {
		if currentUserMsg != nil {
			l.cm.mu.Lock()
			recordMsg := l.cm.recordMessage
			l.cm.mu.Unlock()

			if recordMsg != nil {
				if err := recordMsg(ctx, *currentUserMsg, llm.Usage{}); err != nil {
					l.cm.logger.Error("Failed to record claude user response", "error", err)
				}
			}
			l.mu.Lock()
			l.history = append(l.history, *currentUserMsg)
			l.mu.Unlock()
			currentUserMsg = nil
		}
	}

	// --- CC subagent bridge state ---
	// taskInputs tracks Task tool invocations by tool_use_id so we can derive a slug
	// when the first subagent event arrives.
	taskInputs := map[string]ccTaskInput{}
	// subagentSessions maps parent_tool_use_id → in-flight subagent session.
	subagentSessions := map[string]*ccSubagentSession{}

	flushSubagentAssistant := func(sa *ccSubagentSession) {
		if sa.pendingAssistant == nil {
			return
		}
		msg := sa.pendingAssistant
		sa.pendingAssistant = nil
		hasToolUse := false
		for _, c := range msg.Content {
			if c.Type == llm.ContentTypeToolUse {
				hasToolUse = true
				break
			}
		}
		msg.EndOfTurn = !hasToolUse
		if sa.manager.recordMessage != nil {
			if err := sa.manager.recordMessage(ctx, *msg, llm.Usage{}); err != nil {
				l.cm.logger.Error("Failed to record CC subagent assistant message", "error", err)
			}
		}
	}

	flushSubagentUser := func(sa *ccSubagentSession) {
		if sa.pendingUser == nil {
			return
		}
		msg := sa.pendingUser
		sa.pendingUser = nil
		if sa.manager.recordMessage != nil {
			if err := sa.manager.recordMessage(ctx, *msg, llm.Usage{}); err != nil {
				l.cm.logger.Error("Failed to record CC subagent user message", "error", err)
			}
		}
	}

	// getOrCreateSubagentSession returns the session for a given parent_tool_use_id,
	// creating the Shelley subconversation on first encounter.
	getOrCreateSubagentSession := func(parentToolUseID string) *ccSubagentSession {
		if l.cm.ccSubagentBridge == nil {
			return nil
		}
		if sa, ok := subagentSessions[parentToolUseID]; ok {
			return sa
		}
		input, ok := taskInputs[parentToolUseID]
		if !ok {
			// We haven't seen the Task tool_use yet; skip for now.
			return nil
		}
		slug := ccDescriptionToSlug(input.Description)
		if slug == "" {
			slug = parentToolUseID
			if len(slug) > 12 {
				slug = slug[:12]
			}
		}
		l.cm.mu.Lock()
		cwd := l.cm.cwd
		convID := l.cm.conversationID
		l.cm.mu.Unlock()

		subManager, err := l.cm.ccSubagentBridge(ctx, slug, convID, cwd)
		if err != nil {
			l.cm.logger.Warn("Failed to create CC subagent conversation", "slug", slug, "error", err)
			return nil
		}
		subManager.SetAgentWorking(true)
		sa := &ccSubagentSession{manager: subManager}
		subagentSessions[parentToolUseID] = sa
		return sa
	}

	defer func() {
		flushAssistant()
		flushUser()
		// Flush and close all in-flight subagent sessions.
		for _, sa := range subagentSessions {
			flushSubagentAssistant(sa)
			flushSubagentUser(sa)
			sa.manager.SetAgentWorking(false)
		}
	}()

	// parseToolResultContent converts CC's tool_result content (string or block array)
	// into []llm.Content. Used for both the main loop and subagent sessions.
	parseToolResultContent := func(raw json.RawMessage) []llm.Content {
		var plainText string
		if err := json.Unmarshal(raw, &plainText); err == nil {
			return []llm.Content{{Type: llm.ContentTypeText, Text: plainText}}
		}
		var blocks []struct {
			Type   string `json:"type"`
			Text   string `json:"text,omitempty"`
			Source *struct {
				Type      string `json:"type"`
				MediaType string `json:"media_type,omitempty"`
				Data      string `json:"data,omitempty"`
			} `json:"source,omitempty"`
		}
		if err := json.Unmarshal(raw, &blocks); err == nil {
			var out []llm.Content
			for _, bl := range blocks {
				if bl.Type == "text" {
					out = append(out, llm.Content{Type: llm.ContentTypeText, Text: bl.Text})
				} else if bl.Type == "image" && bl.Source != nil && bl.Source.Type == "base64" {
					out = append(out, llm.Content{
						Type:      llm.ContentTypeText,
						MediaType: bl.Source.MediaType,
						Data:      bl.Source.Data,
					})
				}
			}
			return out
		}
		return []llm.Content{{Type: llm.ContentTypeText, Text: string(raw)}}
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}

		typ, _ := raw["type"].(string)

		if typ == "assistant" || typ == "user" {
			var ev subagentStreamEvent
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				continue
			}

			// Track Task tool_use events so we know the description when the subagent starts.
			if typ == "assistant" && ev.ParentToolUseID == "" {
				for _, c := range ev.Message.Content {
					if c.Type == "tool_use" && c.Name == "Task" {
						var input ccTaskInput
						json.Unmarshal(c.Input, &input) //nolint:errcheck
						taskInputs[c.ID] = input
					}
				}
			}

			// When CC reports a Task tool_result at top level with tool_use_result metadata,
			// the subagent has completed. Flush and mark it done.
			if typ == "user" && ev.ParentToolUseID == "" && ev.ToolUseResult != nil {
				for _, c := range ev.Message.Content {
					if c.Type == "tool_result" {
						if sa, ok := subagentSessions[c.ToolUseID]; ok {
							flushSubagentAssistant(sa)
							flushSubagentUser(sa)
							sa.manager.SetAgentWorking(false)
						}
					}
				}
			}

			// Events with a non-empty parent_tool_use_id belong to a CC Task subagent.
			// Route them to the subagent session and skip top-level processing.
			if ev.ParentToolUseID != "" {
				sa := getOrCreateSubagentSession(ev.ParentToolUseID)
				if sa != nil {
					if typ == "assistant" {
						if ev.Message.ID != sa.lastMsgID || sa.pendingAssistant == nil {
							flushSubagentAssistant(sa)
							flushSubagentUser(sa)
							sa.lastMsgID = ev.Message.ID
							sa.pendingAssistant = &llm.Message{Role: llm.MessageRoleAssistant}
						}
						for _, c := range ev.Message.Content {
							switch c.Type {
							case "text":
								sa.pendingAssistant.Content = append(sa.pendingAssistant.Content, llm.Content{
									Type: llm.ContentTypeText,
									Text: c.Text,
								})
							case "thinking":
								sa.pendingAssistant.Content = append(sa.pendingAssistant.Content, llm.Content{
									Type:     llm.ContentTypeThinking,
									Thinking: c.Thinking,
								})
							case "tool_use":
								sa.pendingAssistant.Content = append(sa.pendingAssistant.Content, llm.Content{
									Type:      llm.ContentTypeToolUse,
									ID:        c.ID,
									ToolName:  c.Name,
									ToolInput: c.Input,
								})
							}
						}
					} else { // user
						flushSubagentAssistant(sa)
						if sa.pendingUser == nil {
							sa.pendingUser = &llm.Message{Role: llm.MessageRoleUser}
						}
						for _, c := range ev.Message.Content {
							switch c.Type {
							case "text":
								// Initial prompt forwarded to subagent.
								sa.pendingUser.Content = append(sa.pendingUser.Content, llm.Content{
									Type: llm.ContentTypeText,
									Text: c.Text,
								})
							case "tool_result":
								sa.pendingUser.Content = append(sa.pendingUser.Content, llm.Content{
									Type:       llm.ContentTypeToolResult,
									ToolUseID:  c.ToolUseID,
									ToolResult: parseToolResultContent(c.Content),
									ToolError:  c.IsError,
								})
							}
						}
					}
				}
				continue // Never process subagent events as top-level messages.
			}

			// --- Top-level event handling (main conversation) ---
			if typ == "assistant" {
				if ev.Message.ID != currentMsgID || currentAssistantMsg == nil {
					flushAssistant()
					flushUser() // A new assistant message means the previous user message (if any) is fully complete
					currentMsgID = ev.Message.ID
					currentAssistantMsg = &llm.Message{
						Role: llm.MessageRoleAssistant,
					}
				}

				for _, c := range ev.Message.Content {
					switch c.Type {
					case "text":
						currentAssistantMsg.Content = append(currentAssistantMsg.Content, llm.Content{
							Type: llm.ContentTypeText,
							Text: c.Text,
						})
					case "thinking":
						currentAssistantMsg.Content = append(currentAssistantMsg.Content, llm.Content{
							Type:     llm.ContentTypeThinking,
							Thinking: c.Thinking,
						})
					case "tool_use":
						currentAssistantMsg.Content = append(currentAssistantMsg.Content, llm.Content{
							Type:      llm.ContentTypeToolUse,
							ID:        c.ID,
							ToolName:  c.Name,
							ToolInput: c.Input,
						})
					}
				}
			} else if typ == "user" {
				flushAssistant() // If we receive a user message, the assistant turn is definitely complete

				if currentUserMsg == nil {
					currentUserMsg = &llm.Message{
						Role: llm.MessageRoleUser,
					}
				}

				for _, c := range ev.Message.Content {
					if c.Type == "tool_result" {
						currentUserMsg.Content = append(currentUserMsg.Content, llm.Content{
							Type:       llm.ContentTypeToolResult,
							ToolUseID:  c.ToolUseID,
							ToolResult: parseToolResultContent(c.Content),
							ToolError:  c.IsError,
						})
					}
				}
			}
		}
	}

	return scanner.Err()
}
