package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/gitstate"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/llmhttp"
	"shelley.exe.dev/loop"
	"shelley.exe.dev/subpub"
)

var errConversationModelMismatch = errors.New("conversation model mismatch")

// ConversationManager manages a single active conversation
type ConversationManager struct {
	conversationID string
	db             *db.DB
	loop           *loop.Loop
	loopCancel     context.CancelFunc
	loopCtx        context.Context
	mu             sync.Mutex
	lastActivity   time.Time
	modelID        string
	history        []llm.Message
	system         []llm.SystemContent
	recordMessage  loop.MessageRecordFunc
	logger         *slog.Logger
	toolSetConfig  claudetool.ToolSetConfig
	toolSet        *claudetool.ToolSet // created per-conversation when loop starts

	subpub *subpub.SubPub[StreamResponse]

	hydrated              bool
	hasConversationEvents bool
	cwd                   string // working directory for tools
	startupHookOutput     string // output from startup hook, prepended to first user message

	// agentWorking tracks whether the agent is currently working.
	// This is explicitly managed and broadcast to subscribers when it changes.
	agentWorking bool

	// onStateChange is called when the conversation state changes.
	// This allows the server to broadcast state changes to all subscribers.
	onStateChange func(state ConversationState)
}

// NewConversationManager constructs a manager with dependencies but defers hydration until needed.
func NewConversationManager(conversationID string, database *db.DB, baseLogger *slog.Logger, toolSetConfig claudetool.ToolSetConfig, recordMessage loop.MessageRecordFunc, onStateChange func(ConversationState)) *ConversationManager {
	logger := baseLogger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("conversationID", conversationID)

	return &ConversationManager{
		conversationID: conversationID,
		db:             database,
		lastActivity:   time.Now(),
		recordMessage:  recordMessage,
		logger:         logger,
		toolSetConfig:  toolSetConfig,
		subpub:         subpub.New[StreamResponse](),
		onStateChange:  onStateChange,
	}
}

// SetAgentWorking updates the agent working state and notifies the server to broadcast.
func (cm *ConversationManager) SetAgentWorking(working bool) {
	cm.mu.Lock()
	if cm.agentWorking == working {
		cm.mu.Unlock()
		return
	}
	cm.agentWorking = working
	onStateChange := cm.onStateChange
	convID := cm.conversationID
	cm.mu.Unlock()

	cm.logger.Debug("agent working state changed", "working", working)
	if onStateChange != nil {
		onStateChange(ConversationState{
			ConversationID: convID,
			Working:        working,
		})
	}
}

// IsAgentWorking returns the current agent working state.
func (cm *ConversationManager) IsAgentWorking() bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.agentWorking
}

// Hydrate loads conversation state from the database, generating a system prompt if missing.
func (cm *ConversationManager) Hydrate(ctx context.Context) error {
	cm.mu.Lock()
	if cm.hydrated {
		cm.lastActivity = time.Now()
		cm.mu.Unlock()
		return nil
	}
	cm.mu.Unlock()

	conversation, err := cm.db.GetConversationByID(ctx, cm.conversationID)
	if err != nil {
		return fmt.Errorf("conversation not found: %w", err)
	}

	var messages []generated.Message
	err = cm.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		messages, err = q.ListMessages(ctx, cm.conversationID)
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to get conversation history: %w", err)
	}

	// Load cwd from conversation if available - must happen before generating system prompt
	// so that the system prompt includes guidance files from the context directory
	cwd := ""
	if conversation.Cwd != nil {
		cwd = *conversation.Cwd
	}
	cm.cwd = cwd

	// Generate system prompt if missing:
	// - For user-initiated conversations: full system prompt
	// - For subagent conversations (has parent): minimal subagent prompt
	isNewUserConversation := false
	if !hasSystemMessage(messages) {
		var systemMsg *generated.Message
		var err error
		if conversation.ParentConversationID != nil {
			// Subagent conversation - use minimal prompt
			systemMsg, err = cm.createSubagentSystemPrompt(ctx)
		} else if conversation.UserInitiated {
			// User-initiated conversation - use full prompt
			systemMsg, err = cm.createSystemPrompt(ctx)
			isNewUserConversation = true
		}
		if err != nil {
			return err
		}
		if systemMsg != nil {
			messages = append(messages, *systemMsg)
		}
	}

	// Run startup hook for new user-initiated conversations
	if isNewUserConversation {
		if hookMsg, err := cm.runStartupHook(ctx); err != nil {
			cm.logger.Warn("Failed to run startup hook", "error", err)
		} else if hookMsg != nil {
			messages = append(messages, *hookMsg)
		}
	}

	history, system := cm.partitionMessages(messages)

	cm.mu.Lock()
	cm.history = history
	cm.system = system
	cm.hasConversationEvents = len(history) > 0
	cm.lastActivity = time.Now()
	cm.hydrated = true
	cm.mu.Unlock()

	cm.logSystemPromptState(system, len(messages))

	return nil
}

// AcceptUserMessage enqueues a user message, ensuring the loop is ready first.
// The message is recorded to the database immediately so it appears in the UI,
// even if the loop is busy processing a previous request.
func (cm *ConversationManager) AcceptUserMessage(ctx context.Context, service llm.Service, modelID string, message llm.Message) (bool, error) {
	if service == nil {
		return false, fmt.Errorf("llm service is required")
	}

	if err := cm.Hydrate(ctx); err != nil {
		return false, err
	}

	if err := cm.ensureLoop(service, modelID); err != nil {
		return false, err
	}

	cm.mu.Lock()
	isFirst := !cm.hasConversationEvents
	cm.hasConversationEvents = true
	loopInstance := cm.loop
	cm.lastActivity = time.Now()
	recordMessage := cm.recordMessage
	startupHookOutput := cm.startupHookOutput
	cm.startupHookOutput = "" // consume it
	cm.mu.Unlock()

	if loopInstance == nil {
		return false, fmt.Errorf("conversation loop not initialized")
	}

	// Record the user message to the database immediately so it appears in the UI,
	// even if the loop is busy processing a previous request
	if recordMessage != nil {
		if err := recordMessage(ctx, message, llm.Usage{}); err != nil {
			cm.logger.Error("failed to record user message immediately", "error", err)
			// Continue anyway - the loop will also try to record it
		}
	}

	// Prepend startup hook output to first user message for the LLM
	llmMessage := message
	if startupHookOutput != "" {
		llmMessage = prependStartupHook(message, startupHookOutput)
	}

	loopInstance.QueueUserMessage(llmMessage)

	// Mark agent as working - we just queued work for the loop
	cm.SetAgentWorking(true)

	return isFirst, nil
}

// Touch updates last activity timestamp.
func (cm *ConversationManager) Touch() {
	cm.mu.Lock()
	cm.lastActivity = time.Now()
	cm.mu.Unlock()
}

func hasSystemMessage(messages []generated.Message) bool {
	for _, msg := range messages {
		if msg.Type == string(db.MessageTypeSystem) {
			return true
		}
	}
	return false
}

func (cm *ConversationManager) createSystemPrompt(ctx context.Context) (*generated.Message, error) {
	systemPrompt, err := GenerateSystemPrompt(cm.cwd)
	if err != nil {
		return nil, fmt.Errorf("failed to generate system prompt: %w", err)
	}

	if systemPrompt == "" {
		cm.logger.Info("Skipping empty system prompt generation")
		return nil, nil
	}

	systemMessage := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: systemPrompt}},
	}

	created, err := cm.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: cm.conversationID,
		Type:           db.MessageTypeSystem,
		LLMData:        systemMessage,
		UsageData:      llm.Usage{},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to store system prompt: %w", err)
	}

	if err := cm.db.QueriesTx(ctx, func(q *generated.Queries) error {
		return q.UpdateConversationTimestamp(ctx, cm.conversationID)
	}); err != nil {
		cm.logger.Warn("Failed to update conversation timestamp after system prompt", "error", err)
	}

	cm.logger.Info("Stored system prompt", "length", len(systemPrompt))
	return created, nil
}

func (cm *ConversationManager) createSubagentSystemPrompt(ctx context.Context) (*generated.Message, error) {
	systemPrompt, err := GenerateSubagentSystemPrompt(cm.cwd)
	if err != nil {
		return nil, fmt.Errorf("failed to generate subagent system prompt: %w", err)
	}

	if systemPrompt == "" {
		cm.logger.Info("Skipping empty subagent system prompt generation")
		return nil, nil
	}

	systemMessage := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: systemPrompt}},
	}

	created, err := cm.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: cm.conversationID,
		Type:           db.MessageTypeSystem,
		LLMData:        systemMessage,
		UsageData:      llm.Usage{},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to store subagent system prompt: %w", err)
	}

	cm.logger.Info("Stored subagent system prompt", "length", len(systemPrompt))
	return created, nil
}

func (cm *ConversationManager) partitionMessages(messages []generated.Message) ([]llm.Message, []llm.SystemContent) {
	var history []llm.Message
	var system []llm.SystemContent

	for _, msg := range messages {
		// Skip messages that are user-visible only, not sent to LLM
		if msg.Type == string(db.MessageTypeGitInfo) || msg.Type == string(db.MessageTypeStartupHook) {
			continue
		}

		llmMsg, err := convertToLLMMessage(msg)
		if err != nil {
			cm.logger.Warn("Failed to convert message to LLM format", "messageID", msg.MessageID, "error", err)
			continue
		}

		if msg.Type == string(db.MessageTypeSystem) {
			for _, content := range llmMsg.Content {
				if content.Type == llm.ContentTypeText && content.Text != "" {
					system = append(system, llm.SystemContent{Type: "text", Text: content.Text})
				}
			}
			continue
		}

		history = append(history, llmMsg)
	}

	return history, system
}

func (cm *ConversationManager) logSystemPromptState(system []llm.SystemContent, messageCount int) {
	if len(system) == 0 {
		cm.logger.Warn("No system prompt found in database", "message_count", messageCount)
		return
	}

	length := 0
	for _, sys := range system {
		length += len(sys.Text)
	}
	cm.logger.Info("Loaded system prompt from database", "system_items", len(system), "total_length", length)
}

func (cm *ConversationManager) ensureLoop(service llm.Service, modelID string) error {
	cm.mu.Lock()
	if cm.loop != nil {
		existingModel := cm.modelID
		cm.mu.Unlock()
		if existingModel != "" && modelID != "" && existingModel != modelID {
			return fmt.Errorf("%w: conversation already uses model %s; requested %s", errConversationModelMismatch, existingModel, modelID)
		}
		return nil
	}

	history := append([]llm.Message(nil), cm.history...)
	system := append([]llm.SystemContent(nil), cm.system...)
	recordMessage := cm.recordMessage
	logger := cm.logger
	cwd := cm.cwd
	toolSetConfig := cm.toolSetConfig
	conversationID := cm.conversationID
	db := cm.db
	cm.mu.Unlock()

	// Create tools for this conversation with the conversation's working directory
	toolSetConfig.WorkingDir = cwd
	toolSetConfig.ModelID = modelID
	toolSetConfig.ConversationID = conversationID
	toolSetConfig.ParentConversationID = conversationID // For subagent tool
	toolSetConfig.OnWorkingDirChange = func(newDir string) {
		// Persist working directory change to database
		if err := db.UpdateConversationCwd(context.Background(), conversationID, newDir); err != nil {
			logger.Error("failed to persist working directory change", "error", err, "newDir", newDir)
		}
	}

	// Create a context with the conversation ID for LLM request recording/prefix dedup
	baseCtx := llmhttp.WithConversationID(context.Background(), conversationID)
	processCtx, cancel := context.WithTimeout(baseCtx, 12*time.Hour)
	toolSet := claudetool.NewToolSet(processCtx, toolSetConfig)

	loopInstance := loop.NewLoop(loop.Config{
		LLM:           service,
		History:       history,
		Tools:         toolSet.Tools(),
		RecordMessage: recordMessage,
		Logger:        logger,
		System:        system,
		WorkingDir:    cwd,
		GetWorkingDir: toolSet.WorkingDir().Get,
		OnGitStateChange: func(ctx context.Context, state *gitstate.GitState) {
			cm.recordGitStateChange(ctx, state)
		},
	})

	cm.mu.Lock()
	if cm.loop != nil {
		cm.mu.Unlock()
		cancel()
		toolSet.Cleanup()
		existingModel := cm.modelID
		if existingModel != "" && modelID != "" && existingModel != modelID {
			return fmt.Errorf("%w: conversation already uses model %s; requested %s", errConversationModelMismatch, existingModel, modelID)
		}
		return nil
	}
	cm.loop = loopInstance
	cm.loopCancel = cancel
	cm.loopCtx = processCtx
	cm.modelID = modelID
	cm.toolSet = toolSet
	cm.history = nil
	cm.system = nil
	cm.mu.Unlock()

	go func() {
		if err := loopInstance.Go(processCtx); err != nil && err != context.DeadlineExceeded && err != context.Canceled {
			if logger != nil {
				logger.Error("Conversation loop stopped", "error", err)
			} else {
				slog.Default().Error("Conversation loop stopped", "error", err)
			}
		}
	}()

	return nil
}

func (cm *ConversationManager) stopLoop() {
	cm.mu.Lock()
	cancel := cm.loopCancel
	toolSet := cm.toolSet
	cm.loopCancel = nil
	cm.loopCtx = nil
	cm.loop = nil
	cm.modelID = ""
	cm.toolSet = nil
	cm.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if toolSet != nil {
		toolSet.Cleanup()
	}
}

// CancelConversation cancels the current conversation loop and records a cancelled tool result if a tool was in progress
func (cm *ConversationManager) CancelConversation(ctx context.Context) error {
	cm.mu.Lock()
	loopInstance := cm.loop
	loopCtx := cm.loopCtx
	cancel := cm.loopCancel
	cm.mu.Unlock()

	if loopInstance == nil {
		cm.logger.Info("No active loop to cancel")
		return nil
	}

	cm.logger.Info("Cancelling conversation")

	// Check if there's an in-progress tool call by examining the history
	history := loopInstance.GetHistory()
	var inProgressToolID string
	var inProgressToolName string

	// Find tool_uses that don't have corresponding tool_results.
	// Strategy:
	// 1. Find the last assistant message that contains tool_uses
	// 2. Collect all tool_result IDs from user messages AFTER that assistant message
	// 3. Find tool_uses that don't have matching results

	// Step 1: Find the index of the last assistant message with tool_uses
	lastToolUseAssistantIdx := -1
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		if msg.Role == llm.MessageRoleAssistant {
			hasToolUse := false
			for _, content := range msg.Content {
				if content.Type == llm.ContentTypeToolUse {
					hasToolUse = true
					break
				}
			}
			if hasToolUse {
				lastToolUseAssistantIdx = i
				break
			}
		}
	}

	if lastToolUseAssistantIdx >= 0 {
		// Step 2: Collect all tool_result IDs from messages after the assistant message
		toolResultIDs := make(map[string]bool)
		for i := lastToolUseAssistantIdx + 1; i < len(history); i++ {
			msg := history[i]
			if msg.Role == llm.MessageRoleUser {
				for _, content := range msg.Content {
					if content.Type == llm.ContentTypeToolResult {
						toolResultIDs[content.ToolUseID] = true
					}
				}
			}
		}

		// Step 3: Find the first tool_use that doesn't have a result
		assistantMsg := history[lastToolUseAssistantIdx]
		for _, content := range assistantMsg.Content {
			if content.Type == llm.ContentTypeToolUse {
				if !toolResultIDs[content.ID] {
					inProgressToolID = content.ID
					inProgressToolName = content.ToolName
					break
				}
			}
		}
	}

	// Cancel the context
	if cancel != nil {
		cancel()
	}

	// Wait briefly for the loop to stop
	if loopCtx != nil {
		select {
		case <-loopCtx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}

	// Record cancellation messages
	if inProgressToolID != "" {
		// If there was an in-progress tool, record a cancelled result
		cm.logger.Info("Recording cancelled tool result", "tool_id", inProgressToolID, "tool_name", inProgressToolName)
		cancelTime := time.Now()
		cancelledMessage := llm.Message{
			Role: llm.MessageRoleUser,
			Content: []llm.Content{
				{
					Type:             llm.ContentTypeToolResult,
					ToolUseID:        inProgressToolID,
					ToolError:        true,
					ToolResult:       []llm.Content{{Type: llm.ContentTypeText, Text: "Tool execution cancelled by user"}},
					ToolUseStartTime: &cancelTime,
					ToolUseEndTime:   &cancelTime,
				},
			},
		}

		if err := cm.recordMessage(ctx, cancelledMessage, llm.Usage{}); err != nil {
			cm.logger.Error("Failed to record cancelled tool result", "error", err)
			return fmt.Errorf("failed to record cancelled tool result: %w", err)
		}
	}

	// Always record an assistant message with EndOfTurn to properly end the turn
	// This ensures agentWorking() returns false, even if no tool was executing
	endTurnMessage := llm.Message{
		Role:      llm.MessageRoleAssistant,
		Content:   []llm.Content{{Type: llm.ContentTypeText, Text: "[Operation cancelled]"}},
		EndOfTurn: true,
	}

	if err := cm.recordMessage(ctx, endTurnMessage, llm.Usage{}); err != nil {
		cm.logger.Error("Failed to record end turn message", "error", err)
		return fmt.Errorf("failed to record end turn message: %w", err)
	}

	// Mark agent as not working
	cm.SetAgentWorking(false)

	cm.mu.Lock()
	cm.loopCancel = nil
	cm.loopCtx = nil
	cm.loop = nil
	cm.modelID = ""
	// Reset hydrated so that the next AcceptUserMessage will reload history from the database
	cm.hydrated = false
	cm.mu.Unlock()

	return nil
}

// GitInfoUserData is the structured data stored in user_data for gitinfo messages.
type GitInfoUserData struct {
	Worktree string `json:"worktree"`
	Branch   string `json:"branch"`
	Commit   string `json:"commit"`
	Subject  string `json:"subject"`
	Text     string `json:"text"` // Human-readable description
}

// recordGitStateChange creates a gitinfo message when git state changes.
// This message is visible to users in the UI but is not sent to the LLM.
func (cm *ConversationManager) recordGitStateChange(ctx context.Context, state *gitstate.GitState) {
	if state == nil || !state.IsRepo {
		return
	}

	// Create a gitinfo message with the state description
	message := llm.Message{
		Role:    llm.MessageRoleAssistant,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: state.String()}},
	}

	userData := GitInfoUserData{
		Worktree: state.Worktree,
		Branch:   state.Branch,
		Commit:   state.Commit,
		Subject:  state.Subject,
		Text:     state.String(),
	}

	createdMsg, err := cm.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: cm.conversationID,
		Type:           db.MessageTypeGitInfo,
		LLMData:        message,
		UserData:       userData,
		UsageData:      llm.Usage{},
	})
	if err != nil {
		cm.logger.Error("Failed to record git state change", "error", err)
		return
	}

	cm.logger.Debug("Recorded git state change", "state", state.String())

	// Notify subscribers so the UI updates
	go cm.notifyGitStateChange(context.WithoutCancel(ctx), createdMsg)
}

// notifyGitStateChange publishes a gitinfo message to subscribers.
func (cm *ConversationManager) notifyGitStateChange(ctx context.Context, msg *generated.Message) {
	var conversation generated.Conversation
	err := cm.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		conversation, err = q.GetConversation(ctx, cm.conversationID)
		return err
	})
	if err != nil {
		cm.logger.Error("Failed to get conversation for git state notification", "error", err)
		return
	}

	apiMessages := toAPIMessages([]generated.Message{*msg})
	streamData := StreamResponse{
		Messages:     apiMessages,
		Conversation: conversation,
	}
	cm.subpub.Publish(msg.SequenceID, streamData)
}

// prependStartupHook prepends startup hook output to the first text content in a user message.
func prependStartupHook(message llm.Message, hookOutput string) llm.Message {
	result := llm.Message{
		Role:    message.Role,
		Content: make([]llm.Content, len(message.Content)),
	}
	copy(result.Content, message.Content)

	for i, content := range result.Content {
		if content.Type == llm.ContentTypeText {
			result.Content[i].Text = "[Startup hook output]\n" + hookOutput + "\n\n[User message]\n" + content.Text
			break
		}
	}
	return result
}

// runStartupHook runs the startup hook and creates a startup-hook message if output is produced.
// The output is stored for later prepending to the first user message.
func (cm *ConversationManager) runStartupHook(ctx context.Context) (*generated.Message, error) {
	result := RunStartupHook(ctx, cm.cwd)
	if result == nil {
		return nil, nil // No hook configured
	}

	// Build message text
	var text string
	if result.Error != nil {
		if result.Output != "" {
			text = fmt.Sprintf("Startup hook error: %v\n\nOutput:\n%s", result.Error, result.Output)
		} else {
			text = fmt.Sprintf("Startup hook error: %v", result.Error)
		}
	} else {
		text = result.Output
	}

	if text == "" {
		return nil, nil // No output
	}

	// Store for prepending to first user message
	cm.mu.Lock()
	cm.startupHookOutput = text
	cm.mu.Unlock()

	// Create message for UI display (not sent to LLM directly)
	message := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: text}},
	}

	created, err := cm.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: cm.conversationID,
		Type:           db.MessageTypeStartupHook,
		LLMData:        message,
		UsageData:      llm.Usage{},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to store startup hook message: %w", err)
	}

	cm.logger.Info("Stored startup hook output", "length", len(text))
	return created, nil
}
