package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/llm"
)

// SubagentRunner implements claudetool.SubagentRunner.
type SubagentRunner struct {
	server *Server
}

// NewSubagentRunner creates a new SubagentRunner.
func NewSubagentRunner(s *Server) *SubagentRunner {
	return &SubagentRunner{server: s}
}

// RunSubagent implements claudetool.SubagentRunner.
func (r *SubagentRunner) RunSubagent(ctx context.Context, conversationID, prompt string, wait bool, timeout time.Duration) (string, error) {
	s := r.server

	// Get or create conversation manager for the subagent
	manager, err := s.getOrCreateConversationManager(ctx, conversationID)
	if err != nil {
		return "", fmt.Errorf("failed to get conversation manager: %w", err)
	}

	// Get the model ID from the server's default
	// In predictable-only mode, use "predictable" as the model
	modelID := s.defaultModel
	if modelID == "" && s.predictableOnly {
		modelID = "predictable"
	}

	// Get LLM service
	llmService, err := s.llmManager.GetService(modelID)
	if err != nil {
		return "", fmt.Errorf("failed to get LLM service: %w", err)
	}

	// Create user message
	userMessage := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: prompt}},
	}

	// Accept the user message (this starts processing)
	_, err = manager.AcceptUserMessage(ctx, llmService, modelID, userMessage)
	if err != nil {
		return "", fmt.Errorf("failed to accept user message: %w", err)
	}

	if !wait {
		return fmt.Sprintf("Subagent started processing. Conversation ID: %s", conversationID), nil
	}

	// Wait for the agent to finish (or timeout)
	return r.waitForResponse(ctx, conversationID, timeout)
}

func (r *SubagentRunner) waitForResponse(ctx context.Context, conversationID string, timeout time.Duration) (string, error) {
	s := r.server

	deadline := time.Now().Add(timeout)
	pollInterval := 500 * time.Millisecond

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		if time.Now().After(deadline) {
			return "Subagent is still working (timeout reached). Send another message to check status.", nil
		}

		// Check if agent is still working
		working, err := r.isAgentWorking(ctx, conversationID)
		if err != nil {
			return "", fmt.Errorf("failed to check agent status: %w", err)
		}

		if !working {
			// Agent is done, get the last message
			return r.getLastAssistantResponse(ctx, conversationID)
		}

		// Wait before polling again
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollInterval):
		}

		// Don't hog the conversation manager mutex
		s.mu.Lock()
		if mgr, ok := s.activeConversations[conversationID]; ok {
			mgr.Touch()
		}
		s.mu.Unlock()
	}
}

func (r *SubagentRunner) isAgentWorking(ctx context.Context, conversationID string) (bool, error) {
	s := r.server

	// Get the conversation manager - it tracks the working state
	s.mu.Lock()
	mgr, ok := s.activeConversations[conversationID]
	s.mu.Unlock()

	if !ok {
		// No active manager means the agent is not working
		return false, nil
	}

	return mgr.IsAgentWorking(), nil
}

func (r *SubagentRunner) getLastAssistantResponse(ctx context.Context, conversationID string) (string, error) {
	s := r.server

	// Get the latest message
	msg, err := s.db.GetLatestMessage(ctx, conversationID)
	if err != nil {
		return "", fmt.Errorf("failed to get latest message: %w", err)
	}

	// Extract text content
	if msg.LlmData == nil {
		return "", nil
	}

	var llmMsg llm.Message
	if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
		return "", fmt.Errorf("failed to parse message: %w", err)
	}

	var texts []string
	for _, content := range llmMsg.Content {
		if content.Type == llm.ContentTypeText && content.Text != "" {
			texts = append(texts, content.Text)
		}
	}

	return strings.Join(texts, "\n"), nil
}

// createSubagentToolSetConfig creates a ToolSetConfig for subagent conversations.
// Subagent conversations don't have nested subagents to avoid complexity.
func (s *Server) createSubagentToolSetConfig(conversationID string) claudetool.ToolSetConfig {
	return claudetool.ToolSetConfig{
		LLMProvider:      s.llmManager,
		EnableJITInstall: true,
		EnableBrowser:    true, // Subagents can use browser tools
		// No SubagentRunner/DB - subagents can't spawn nested subagents
	}
}

// Ensure SubagentRunner implements claudetool.SubagentRunner.
var _ claudetool.SubagentRunner = (*SubagentRunner)(nil)

// handleGetSubagents returns the list of subagents for a conversation.
func (s *Server) handleGetSubagents(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", 405)
		return
	}

	subagents, err := s.db.GetSubagents(r.Context(), conversationID)
	if err != nil {
		s.logger.Error("Failed to get subagents", "conversationID", conversationID, "error", err)
		http.Error(w, "Failed to get subagents", 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(subagents)
}
