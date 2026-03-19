package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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

// pendingMessage holds a user message that is queued to be sent after the
// current agent turn (or distillation) completes.
type pendingMessage struct {
	Message   llm.Message
	ModelID   string
	MessageID string // DB message ID, for cancellation/UI updates
}

// ConversationManager manages a single active conversation
type ConversationManager struct {
	conversationID      string
	conversationOptions db.ConversationOptions
	db                  *db.DB
	loop                *loop.Loop
	loopCancel          context.CancelFunc
	loopCtx             context.Context
	mu                  sync.Mutex
	lastActivity        time.Time
	modelID             string
	recordMessage       loop.MessageRecordFunc
	logger              *slog.Logger
	toolSetConfig       claudetool.ToolSetConfig
	toolSet             *claudetool.ToolSet // created per-conversation when loop starts

	subpub *subpub.SubPub[StreamResponse]

	hydrated              bool
	hasConversationEvents bool
	cwd                   string // working directory for tools
	userEmail             string // exe.dev auth email, from X-ExeDev-Email header

	// agentWorking tracks whether the agent is currently working.
	// This is explicitly managed and broadcast to subscribers when it changes.
	agentWorking bool

	// pendingMessages holds messages queued to be sent after the current turn ends.
	pendingMessages []pendingMessage

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
	modelID := cm.modelID
	cm.mu.Unlock()

	cm.logger.Debug("agent working state changed", "working", working)
	if onStateChange != nil {
		onStateChange(ConversationState{
			ConversationID: convID,
			Working:        working,
			Model:          modelID,
		})
	}
}

// IsAgentWorking returns the current agent working state.
func (cm *ConversationManager) IsAgentWorking() bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.agentWorking
}

// GetModel returns the model ID used by this conversation.
func (cm *ConversationManager) GetModel() string {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.modelID
}

// Hydrate loads conversation metadata from the database and generates a system
// prompt if one doesn't exist yet. It does NOT cache the message history;
// ensureLoop reads messages fresh from the DB when creating a loop so that
// any messages added asynchronously (e.g. distillation) are always included.
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

	// Load cwd from conversation if available - must happen before generating system prompt
	// so that the system prompt includes guidance files from the context directory
	cwd := ""
	if conversation.Cwd != nil {
		cwd = *conversation.Cwd
	}
	cm.cwd = cwd

	// Load model from conversation if available
	var modelID string
	if conversation.Model != nil {
		modelID = *conversation.Model
	}

	// Load conversation options
	cm.conversationOptions = db.ParseConversationOptions(conversation.ConversationOptions)

	// Set ParentConversationID on toolSetConfig so that subagent tool is included
	// in the display_data tools list when generating system prompt.
	// This is also set in ensureLoop, but must be set here for Hydrate's system prompt creation.
	cm.toolSetConfig.ParentConversationID = cm.conversationID

	// Generate system prompt if missing:
	// - For user-initiated conversations: full system prompt
	// - For orchestrator conversations: orchestrator system prompt
	// - For subagent conversations (has parent): minimal subagent prompt
	var messages []generated.Message
	err = cm.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		messages, err = q.ListMessagesForContext(ctx, cm.conversationID)
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to get conversation history: %w", err)
	}

	if !hasSystemMessage(messages) {
		var systemMsg *generated.Message
		var err error
		if conversation.ParentConversationID != nil {
			parentID := *conversation.ParentConversationID
			// Check if the parent is an orchestrator to use the specialized subagent prompt
			var parentOpts string
			if qErr := cm.db.Queries(ctx, func(q *generated.Queries) error {
				var e error
				parentOpts, e = q.GetConversationOptions(ctx, parentID)
				return e
			}); qErr != nil {
				cm.logger.Warn("Failed to get parent conversation options", "error", qErr)
			}
			if db.ParseConversationOptions(parentOpts).IsOrchestrator() {
				systemMsg, err = cm.createOrchestratorSubagentSystemPrompt(ctx, parentID)
			} else {
				systemMsg, err = cm.createSubagentSystemPrompt(ctx, parentID)
			}
		} else if cm.conversationOptions.IsOrchestrator() {
			systemMsg, err = cm.createOrchestratorSystemPrompt(ctx)
		} else if conversation.UserInitiated {
			systemMsg, err = cm.createSystemPrompt(ctx)
		}
		if err != nil {
			return err
		}
		_ = systemMsg // persisted to DB; ensureLoop will read it
	}

	cm.mu.Lock()
	cm.hasConversationEvents = hasNonSystemMessages(messages)
	cm.lastActivity = time.Now()
	cm.hydrated = true
	cm.modelID = modelID
	cm.mu.Unlock()

	if modelID != "" {
		cm.logger.Info("Loaded model from conversation", "model", modelID)
	}

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

	loopInstance.QueueUserMessage(message)

	// Mark agent as working - we just queued work for the loop
	cm.SetAgentWorking(true)

	return isFirst, nil
}

// QueueMessage records a user message to the database as "queued" and holds it
// for delivery after the current agent turn (or distillation) completes.
// The message is visible in the UI immediately (with queued status).
func (cm *ConversationManager) QueueMessage(ctx context.Context, s *Server, modelID string, message llm.Message) error {
	// Record to DB with queued user_data so it appears in the UI.
	// Mark as excluded_from_context so ensureLoop won't load it into
	// the loop's history — we'll feed it via QueueUserMessage when draining.
	userData := map[string]interface{}{"queued": true}
	createdMsg, err := s.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID:      cm.conversationID,
		Type:                db.MessageTypeUser,
		LLMData:             message,
		UserData:            userData,
		UsageData:           llm.Usage{},
		ExcludedFromContext: true,
	})
	if err != nil {
		return fmt.Errorf("failed to record queued message: %w", err)
	}

	// Update conversation timestamp
	if err := s.db.QueriesTx(ctx, func(q *generated.Queries) error {
		return q.UpdateConversationTimestamp(ctx, cm.conversationID)
	}); err != nil {
		cm.logger.Warn("Failed to update conversation timestamp", "error", err)
	}

	// Notify subscribers so the queued message appears in the UI
	go s.notifySubscribersNewMessage(context.WithoutCancel(ctx), cm.conversationID, createdMsg)

	cm.mu.Lock()
	cm.pendingMessages = append(cm.pendingMessages, pendingMessage{
		Message:   message,
		ModelID:   modelID,
		MessageID: createdMsg.MessageID,
	})
	cm.lastActivity = time.Now()
	// If the agent is no longer working, we need to drain immediately.
	// This handles the race where drainPendingMessages ran (finding nothing)
	// before this QueueMessage call appended the message.
	needsDrain := !cm.agentWorking
	cm.mu.Unlock()

	cm.logger.Info("Queued user message", "message_id", createdMsg.MessageID)

	if needsDrain {
		cm.logger.Info("Agent not working, draining immediately")
		go cm.drainPendingMessages(s)
	}

	return nil
}

// CancelQueuedMessages removes all pending queued messages and deletes them from the DB.
func (cm *ConversationManager) CancelQueuedMessages(ctx context.Context, s *Server) {
	cm.mu.Lock()
	pending := cm.pendingMessages
	cm.pendingMessages = nil
	cm.mu.Unlock()

	for _, pm := range pending {
		if err := s.db.QueriesTx(ctx, func(q *generated.Queries) error {
			return q.DeleteMessage(ctx, pm.MessageID)
		}); err != nil {
			cm.logger.Error("Failed to delete queued message", "message_id", pm.MessageID, "error", err)
		}
	}

	if len(pending) > 0 {
		cm.logger.Info("Cancelled queued messages", "count", len(pending))
		// Notify subscribers so the UI removes the cancelled messages
		go s.notifySubscribers(context.WithoutCancel(ctx), cm.conversationID)
	}
}

// drainPendingMessages processes any queued messages after an agent turn ends.
// Must be called when agentWorking transitions to false.
func (cm *ConversationManager) drainPendingMessages(s *Server) {
	cm.mu.Lock()
	if len(cm.pendingMessages) == 0 {
		cm.mu.Unlock()
		return
	}
	// Take all pending messages atomically
	pending := cm.pendingMessages
	cm.pendingMessages = nil
	loopInstance := cm.loop
	cm.mu.Unlock()

	cm.logger.Info("Draining pending queued messages", "count", len(pending))

	ctx := context.Background()

	modelID := pending[0].ModelID
	if modelID == "" {
		cm.mu.Lock()
		modelID = cm.modelID
		cm.mu.Unlock()
	}

	svc, err := s.llmManager.GetService(modelID)
	if err != nil {
		cm.logger.Error("Failed to get LLM service for queued message", "model", modelID, "error", err)
		return
	}

	// Feed messages to the loop FIRST, while they're still excluded_from_context.
	// This avoids duplicates: ensureLoop (for the no-loop case) won't see them
	// in DB because they're excluded, and QueueUserMessage adds them to the
	// loop's messageQueue which gets moved to history.
	if loopInstance != nil {
		for _, pm := range pending {
			loopInstance.QueueUserMessage(pm.Message)
		}
	} else {
		// No loop yet (e.g., post-distillation). Create one.
		if err := cm.Hydrate(ctx); err != nil {
			cm.logger.Error("Failed to hydrate for queued messages", "error", err)
			return
		}
		if err := cm.ensureLoop(svc, modelID); err != nil {
			cm.logger.Error("Failed to start loop for queued messages", "error", err)
			return
		}
		cm.mu.Lock()
		newLoop := cm.loop
		cm.hasConversationEvents = true
		cm.mu.Unlock()
		if newLoop != nil {
			for _, pm := range pending {
				newLoop.QueueUserMessage(pm.Message)
			}
		}
	}

	cm.SetAgentWorking(true)

	// NOW clear the queued/excluded flags and broadcast updates to the UI.
	// The messages are already queued in the loop, so clearing excluded_from_context
	// is safe — it just makes them visible in future DB reads.
	for _, pm := range pending {
		if err := s.db.QueriesTx(ctx, func(q *generated.Queries) error {
			if err := q.UpdateMessageExcludedFromContext(ctx, generated.UpdateMessageExcludedFromContextParams{
				ExcludedFromContext: false,
				MessageID:           pm.MessageID,
			}); err != nil {
				return err
			}
			newData := `{}`
			return q.UpdateMessageUserData(ctx, generated.UpdateMessageUserDataParams{
				UserData:  &newData,
				MessageID: pm.MessageID,
			})
		}); err != nil {
			cm.logger.Error("Failed to update queued message", "message_id", pm.MessageID, "error", err)
		}
		updatedMsg, err := s.db.GetMessageByID(ctx, pm.MessageID)
		if err == nil {
			go s.broadcastMessageUpdate(ctx, cm.conversationID, updatedMsg)
		}
	}
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

func hasNonSystemMessages(messages []generated.Message) bool {
	for _, msg := range messages {
		if msg.Type == string(db.MessageTypeUser) || msg.Type == string(db.MessageTypeAgent) {
			return true
		}
	}
	return false
}

func (cm *ConversationManager) createSystemPrompt(ctx context.Context) (*generated.Message, error) {
	var opts []SystemPromptOption
	if cm.userEmail != "" {
		opts = append(opts, WithUserEmail(cm.userEmail))
	}
	systemPrompt, err := GenerateSystemPrompt(cm.cwd, opts...)
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
		DisplayData:    systemPromptDisplayData(cm.toolSetConfig),
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

// toolDisplayData builds display data from a list of tools.
func toolDisplayData(tools []*llm.Tool) map[string]any {
	type toolDesc struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	var descs []toolDesc
	for _, t := range tools {
		descs = append(descs, toolDesc{Name: t.Name, Description: t.Description})
	}
	return map[string]any{
		"tools": descs,
	}
}

// systemPromptDisplayData returns display data for normal system prompt messages.
func systemPromptDisplayData(cfg claudetool.ToolSetConfig) map[string]any {
	ts := claudetool.NewToolSet(context.Background(), cfg)
	defer ts.Cleanup()
	return toolDisplayData(ts.Tools())
}

func (cm *ConversationManager) createSubagentSystemPrompt(ctx context.Context, parentConversationID string) (*generated.Message, error) {
	systemPrompt, err := GenerateSubagentSystemPrompt(cm.cwd, parentConversationID)
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
		DisplayData:    systemPromptDisplayData(cm.toolSetConfig),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to store subagent system prompt: %w", err)
	}

	cm.logger.Info("Stored subagent system prompt", "length", len(systemPrompt))
	return created, nil
}

// orchestratorContextDir returns the path to the shared context directory for this orchestrator conversation.
func (cm *ConversationManager) orchestratorContextDir(cwd string) string {
	if cwd == "" {
		cwd = os.TempDir()
	}
	return filepath.Join(cwd, ".shelley-orchestrator", cm.conversationID)
}

func (cm *ConversationManager) createOrchestratorSystemPrompt(ctx context.Context) (*generated.Message, error) {
	cwd := cm.cwd
	contextDir := cm.orchestratorContextDir(cwd)
	systemPrompt, err := GenerateOrchestratorSystemPrompt(cwd, contextDir, cm.conversationID)
	if err != nil {
		return nil, fmt.Errorf("failed to generate orchestrator system prompt: %w", err)
	}

	if systemPrompt == "" {
		cm.logger.Info("Skipping empty orchestrator system prompt generation")
		return nil, nil
	}

	systemMessage := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: systemPrompt}},
	}

	// Build orchestrator-specific display data with the orchestrator's tool set.
	// Pass SubagentRunner/SubagentDB/EnableBrowser so the tool list matches what ensureLoop creates.
	ts := claudetool.NewOrchestratorToolSet(ctx, claudetool.OrchestratorToolSetConfig{
		ContextDir:           contextDir,
		WorkingDir:           cwd,
		LLMProvider:          cm.toolSetConfig.LLMProvider,
		SubagentRunner:       cm.toolSetConfig.SubagentRunner,
		SubagentDB:           cm.toolSetConfig.SubagentDB,
		ParentConversationID: cm.conversationID,
		EnableBrowser:        cm.toolSetConfig.EnableBrowser,
		CLIAgent:             cm.conversationOptions.SubagentBackend,
	})
	defer ts.Cleanup()

	created, err := cm.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: cm.conversationID,
		Type:           db.MessageTypeSystem,
		LLMData:        systemMessage,
		UsageData:      llm.Usage{},
		DisplayData:    toolDisplayData(ts.Tools()),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to store orchestrator system prompt: %w", err)
	}

	cm.logger.Info("Stored orchestrator system prompt", "length", len(systemPrompt), "contextDir", contextDir)
	return created, nil
}

func (cm *ConversationManager) createOrchestratorSubagentSystemPrompt(ctx context.Context, parentConversationID string) (*generated.Message, error) {
	systemPrompt, err := GenerateOrchestratorSubagentSystemPrompt(cm.cwd, parentConversationID)
	if err != nil {
		return nil, fmt.Errorf("failed to generate orchestrator subagent system prompt: %w", err)
	}

	if systemPrompt == "" {
		cm.logger.Info("Skipping empty orchestrator subagent system prompt generation")
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
		DisplayData:    systemPromptDisplayData(cm.toolSetConfig),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to store orchestrator subagent system prompt: %w", err)
	}

	cm.logger.Info("Stored orchestrator subagent system prompt", "length", len(systemPrompt))
	return created, nil
}

func (cm *ConversationManager) partitionMessages(messages []generated.Message) ([]llm.Message, []llm.SystemContent) {
	var history []llm.Message
	var system []llm.SystemContent

	for _, msg := range messages {
		// Skip gitinfo messages - they are user-visible only, not sent to LLM
		if msg.Type == string(db.MessageTypeGitInfo) {
			continue
		}

		// Skip error messages - they are system-generated for user visibility,
		// but should not be sent to the LLM as they are not part of the conversation
		if msg.Type == string(db.MessageTypeError) {
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

	recordMessage := cm.recordMessage
	logger := cm.logger
	cwd := cm.cwd
	toolSetConfig := cm.toolSetConfig
	conversationID := cm.conversationID
	conversationOpts := cm.conversationOptions
	database := cm.db
	cm.mu.Unlock()

	// Load conversation history fresh from the database. This is the canonical
	// read — Hydrate only handles metadata and system prompt generation.
	// Reading here ensures we always see messages added asynchronously
	// (e.g. distillation results, subagent completions).
	var dbMessages []generated.Message
	err := database.Queries(context.Background(), func(q *generated.Queries) error {
		var err error
		dbMessages, err = q.ListMessagesForContext(context.Background(), conversationID)
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to load conversation history: %w", err)
	}
	history, system := cm.partitionMessages(dbMessages)
	cm.logSystemPromptState(system, len(dbMessages))

	// Create tools for this conversation with the conversation's working directory
	toolSetConfig.WorkingDir = cwd
	toolSetConfig.ModelID = modelID
	toolSetConfig.ConversationID = conversationID
	toolSetConfig.ParentConversationID = conversationID // For subagent tool
	toolSetConfig.OnWorkingDirChange = func(newDir string) {
		// Persist working directory change to database
		if err := database.UpdateConversationCwd(context.Background(), conversationID, newDir); err != nil {
			logger.Error("failed to persist working directory change", "error", err, "newDir", newDir)
			return
		}

		// Update local cwd
		cm.mu.Lock()
		cm.cwd = newDir
		cm.mu.Unlock()

		// Broadcast conversation update to subscribers so UI gets the new cwd
		var conv generated.Conversation
		err := database.Queries(context.Background(), func(q *generated.Queries) error {
			var err error
			conv, err = q.GetConversation(context.Background(), conversationID)
			return err
		})
		if err != nil {
			logger.Error("failed to get conversation for cwd broadcast", "error", err)
			return
		}
		cm.subpub.Broadcast(StreamResponse{
			Conversation: conv,
		})
	}

	// Create a context with the conversation ID for LLM request recording/prefix dedup
	baseCtx := llmhttp.WithConversationID(context.Background(), conversationID)
	processCtx, cancel := context.WithTimeout(baseCtx, 12*time.Hour)

	var toolSet *claudetool.ToolSet
	if conversationOpts.IsOrchestrator() {
		contextDir := cm.orchestratorContextDir(cwd)
		toolSet = claudetool.NewOrchestratorToolSet(processCtx, claudetool.OrchestratorToolSetConfig{
			ContextDir:           contextDir,
			SubagentRunner:       toolSetConfig.SubagentRunner,
			SubagentDB:           toolSetConfig.SubagentDB,
			ParentConversationID: conversationID,
			ModelID:              modelID,
			LLMProvider:          toolSetConfig.LLMProvider,
			AvailableModels:      toolSetConfig.AvailableModels,
			WorkingDir:           cwd,
			OnWorkingDirChange:   toolSetConfig.OnWorkingDirChange,
			EnableBrowser:        toolSetConfig.EnableBrowser,
			CLIAgent:             conversationOpts.SubagentBackend,
		})
	} else {
		toolSet = claudetool.NewToolSet(processCtx, toolSetConfig)
	}

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
	// Check if we need to persist the model (for conversations created before model column existed)
	needsPersist := cm.modelID == "" && modelID != ""
	cm.loop = loopInstance
	cm.loopCancel = cancel
	cm.loopCtx = processCtx
	cm.modelID = modelID
	cm.toolSet = toolSet
	cm.mu.Unlock()

	// Persist model for legacy conversations
	if needsPersist {
		if err := database.UpdateConversationModel(context.Background(), conversationID, modelID); err != nil {
			logger.Error("failed to persist model for legacy conversation", "error", err)
		}
	}

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

	// Clear pending queued messages BEFORE recording the end-of-turn message.
	// The end-of-turn message triggers drainPendingMessages via notifySubscribers;
	// clearing first ensures the drain finds nothing to process.
	cm.mu.Lock()
	pendingToDelete := cm.pendingMessages
	cm.pendingMessages = nil
	cm.mu.Unlock()

	// Delete orphaned queued messages from DB.
	// The subsequent recordMessage (end-of-turn) triggers
	// notifySubscribersNewMessage → drainPendingMessages, which will find
	// nothing to drain since we already cleared the list.
	for _, pm := range pendingToDelete {
		if err := cm.db.QueriesTx(ctx, func(q *generated.Queries) error {
			return q.DeleteMessage(ctx, pm.MessageID)
		}); err != nil {
			cm.logger.Error("Failed to delete queued message on cancel", "message_id", pm.MessageID, "error", err)
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
