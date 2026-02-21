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

	scanner := bufio.NewScanner(stdout)
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

	defer func() {
		flushAssistant()
		flushUser()
	}()

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

			if typ == "assistant" {
				if ev.Message.ID != currentMsgID {
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
						var resultContents []llm.Content

						var plainText string
						if err := json.Unmarshal(c.Content, &plainText); err == nil {
							resultContents = append(resultContents, llm.Content{Type: llm.ContentTypeText, Text: plainText})
						} else {
							var blocks []struct {
								Type   string `json:"type"`
								Text   string `json:"text,omitempty"`
								Source *struct {
									Type      string `json:"type"`
									MediaType string `json:"media_type,omitempty"`
									Data      string `json:"data,omitempty"`
								} `json:"source,omitempty"`
							}
							if err := json.Unmarshal(c.Content, &blocks); err == nil {
								for _, bl := range blocks {
									if bl.Type == "text" {
										resultContents = append(resultContents, llm.Content{Type: llm.ContentTypeText, Text: bl.Text})
									} else if bl.Type == "image" && bl.Source != nil && bl.Source.Type == "base64" {
										resultContents = append(resultContents, llm.Content{
											Type:      llm.ContentTypeText,
											MediaType: bl.Source.MediaType,
											Data:      bl.Source.Data,
										})
									}
								}
							} else {
								resultContents = append(resultContents, llm.Content{Type: llm.ContentTypeText, Text: string(c.Content)})
							}
						}

						currentUserMsg.Content = append(currentUserMsg.Content, llm.Content{
							Type:       llm.ContentTypeToolResult,
							ToolUseID:  c.ToolUseID,
							ToolResult: resultContents,
							ToolError:  c.IsError,
						})
					}
				}
			}
		}
	}

	return cmd.Wait()
}
