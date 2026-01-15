package loop

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/gitstate"
	"shelley.exe.dev/llm"
)

// MessageRecordFunc is called to record new messages to persistent storage
type MessageRecordFunc func(ctx context.Context, message llm.Message, usage llm.Usage) error

// GitStateChangeFunc is called when the git state changes at the end of a turn.
// This is used to record user-visible notifications about git changes.
type GitStateChangeFunc func(ctx context.Context, state *gitstate.GitState)

// Config contains all configuration needed to create a Loop
type Config struct {
	LLM              llm.Service
	History          []llm.Message
	Tools            []*llm.Tool
	RecordMessage    MessageRecordFunc
	Logger           *slog.Logger
	System           []llm.SystemContent
	WorkingDir       string // working directory for tools
	OnGitStateChange GitStateChangeFunc
	// GetWorkingDir returns the current working directory for tools.
	// If set, this is called at end of turn to check for git state changes.
	// If nil, Config.WorkingDir is used as a static value.
	GetWorkingDir func() string
}

// Loop manages a conversation turn with an LLM including tool execution and message recording.
// Notably, when the turn ends, the "Loop" is over. TODO: maybe rename to Turn?
type Loop struct {
	llm              llm.Service
	tools            []*llm.Tool
	recordMessage    MessageRecordFunc
	history          []llm.Message
	messageQueue     []llm.Message
	totalUsage       llm.Usage
	mu               sync.Mutex
	logger           *slog.Logger
	system           []llm.SystemContent
	workingDir       string
	onGitStateChange GitStateChangeFunc
	getWorkingDir    func() string
	lastGitState     *gitstate.GitState
}

// NewLoop creates a new Loop instance with the provided configuration
func NewLoop(config Config) *Loop {
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// Get initial git state
	workingDir := config.WorkingDir
	if config.GetWorkingDir != nil {
		workingDir = config.GetWorkingDir()
	}
	initialGitState := gitstate.GetGitState(workingDir)

	return &Loop{
		llm:              config.LLM,
		history:          config.History,
		tools:            config.Tools,
		recordMessage:    config.RecordMessage,
		messageQueue:     make([]llm.Message, 0),
		logger:           logger,
		system:           config.System,
		workingDir:       config.WorkingDir,
		onGitStateChange: config.OnGitStateChange,
		getWorkingDir:    config.GetWorkingDir,
		lastGitState:     initialGitState,
	}
}

// SetSystem sets the system prompt for the loop.
// This should be called before processing any messages if the system
// prompt wasn't available at loop creation time.
func (l *Loop) SetSystem(system []llm.SystemContent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.system = system
	l.logger.Debug("set system prompt", "system_items", len(system))
}

// QueueUserMessage adds a user message to the queue to be processed
func (l *Loop) QueueUserMessage(message llm.Message) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messageQueue = append(l.messageQueue, message)
	l.logger.Debug("queued user message", "content_count", len(message.Content))
}

// GetUsage returns the total usage accumulated by this loop
func (l *Loop) GetUsage() llm.Usage {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.totalUsage
}

// GetHistory returns a copy of the current conversation history
func (l *Loop) GetHistory() []llm.Message {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Deep copy the messages to prevent modifications
	historyCopy := make([]llm.Message, len(l.history))
	for i, msg := range l.history {
		// Copy the message
		historyCopy[i] = llm.Message{
			Role:    msg.Role,
			ToolUse: msg.ToolUse, // This is a pointer, but we won't modify it in tests
			Content: make([]llm.Content, len(msg.Content)),
		}
		// Copy content slice
		copy(historyCopy[i].Content, msg.Content)
	}
	return historyCopy
}

// Go runs the conversation loop until the context is canceled
func (l *Loop) Go(ctx context.Context) error {
	if l.llm == nil {
		return fmt.Errorf("no LLM service configured")
	}

	l.logger.Info("starting conversation loop", "tools", len(l.tools))

	for {
		select {
		case <-ctx.Done():
			l.logger.Info("conversation loop canceled")
			return ctx.Err()
		default:
		}

		// Process any queued messages
		l.mu.Lock()
		hasQueuedMessages := len(l.messageQueue) > 0
		if hasQueuedMessages {
			// Add queued messages to history (they are already recorded to DB by ConversationManager)
			for _, msg := range l.messageQueue {
				l.history = append(l.history, msg)
			}
			l.messageQueue = l.messageQueue[:0] // Clear queue
		}
		l.mu.Unlock()

		if hasQueuedMessages {
			// Send request to LLM
			l.logger.Debug("processing queued messages", "count", 1)
			if err := l.processLLMRequest(ctx); err != nil {
				l.logger.Error("failed to process LLM request", "error", err)
				time.Sleep(time.Second) // Wait before retrying
				continue
			}
			l.logger.Debug("finished processing queued messages")
		} else {
			// No queued messages, wait a bit
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
				// Continue loop
			}
		}
	}
}

// ProcessOneTurn processes queued messages through one complete turn (user message + assistant response)
// It stops after the assistant responds, regardless of whether tools were called
func (l *Loop) ProcessOneTurn(ctx context.Context) error {
	if l.llm == nil {
		return fmt.Errorf("no LLM service configured")
	}

	// Process any queued messages first
	l.mu.Lock()
	if len(l.messageQueue) > 0 {
		// Add queued messages to history (they are already recorded to DB by ConversationManager)
		for _, msg := range l.messageQueue {
			l.history = append(l.history, msg)
		}
		l.messageQueue = nil
	}
	l.mu.Unlock()

	// Process one LLM request and response
	return l.processLLMRequest(ctx)
}

// processLLMRequest sends a request to the LLM and handles the response
func (l *Loop) processLLMRequest(ctx context.Context) error {
	l.mu.Lock()
	messages := append([]llm.Message(nil), l.history...)
	tools := l.tools
	system := l.system
	llmService := l.llm
	l.mu.Unlock()

	// Enable prompt caching: set cache flag on last tool and last user message content
	// See https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching
	if len(tools) > 0 {
		// Make a copy of tools to avoid modifying the shared slice
		tools = append([]*llm.Tool(nil), tools...)
		// Copy the last tool and enable caching
		lastTool := *tools[len(tools)-1]
		lastTool.Cache = true
		tools[len(tools)-1] = &lastTool
	}

	// Set cache flag on the last content block of the last user message
	if len(messages) > 0 {
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == llm.MessageRoleUser && len(messages[i].Content) > 0 {
				// Deep copy the message to avoid modifying the shared history
				msg := messages[i]
				msg.Content = append([]llm.Content(nil), msg.Content...)
				msg.Content[len(msg.Content)-1].Cache = true
				messages[i] = msg
				break
			}
		}
	}

	req := &llm.Request{
		Messages: messages,
		Tools:    tools,
		System:   system,
	}

	// Insert missing tool results if the previous message had tool_use blocks
	// without corresponding tool_result blocks. This can happen when a request
	// is cancelled or fails after the LLM responds but before tools execute.
	l.insertMissingToolResults(req)

	systemLen := 0
	for _, sys := range system {
		systemLen += len(sys.Text)
	}
	l.logger.Debug("sending LLM request", "message_count", len(messages), "tool_count", len(tools), "system_items", len(system), "system_length", systemLen)

	// Add a timeout for the LLM request to prevent indefinite hangs
	llmCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	resp, err := llmService.Do(llmCtx, req)
	if err != nil {
		// Record the error as a message so it can be displayed in the UI
		errorMessage := llm.Message{
			Role: llm.MessageRoleAssistant,
			Content: []llm.Content{
				{
					Type: llm.ContentTypeText,
					Text: fmt.Sprintf("LLM request failed: %v", err),
				},
			},
		}
		if recordErr := l.recordMessage(ctx, errorMessage, llm.Usage{}); recordErr != nil {
			l.logger.Error("failed to record error message", "error", recordErr)
		}
		return fmt.Errorf("LLM request failed: %w", err)
	}

	l.logger.Debug("received LLM response", "content_count", len(resp.Content), "stop_reason", resp.StopReason.String(), "usage", resp.Usage.String())

	// Update total usage
	l.mu.Lock()
	l.totalUsage.Add(resp.Usage)
	l.mu.Unlock()

	// Convert response to message and add to history
	assistantMessage := resp.ToMessage()
	l.mu.Lock()
	l.history = append(l.history, assistantMessage)
	l.mu.Unlock()

	// Record assistant message with model and timing metadata
	usageWithMeta := resp.Usage
	usageWithMeta.Model = resp.Model
	usageWithMeta.StartTime = resp.StartTime
	usageWithMeta.EndTime = resp.EndTime
	if err := l.recordMessage(ctx, assistantMessage, usageWithMeta); err != nil {
		l.logger.Error("failed to record assistant message", "error", err)
	}

	// Handle tool calls if any
	if resp.StopReason == llm.StopReasonToolUse {
		l.logger.Debug("handling tool calls", "content_count", len(resp.Content))
		return l.handleToolCalls(ctx, resp.Content)
	}

	// Handle max tokens truncation - record error message for the user
	if resp.StopReason == llm.StopReasonMaxTokens {
		l.logger.Warn("LLM response truncated due to max tokens")
		return l.handleMaxTokensTruncation(ctx)
	}

	// End of turn - check for git state changes
	l.checkGitStateChange(ctx)

	return nil
}

// checkGitStateChange checks if the git state has changed and calls the callback if so.
// This is called at the end of each turn.
func (l *Loop) checkGitStateChange(ctx context.Context) {
	if l.onGitStateChange == nil {
		return
	}

	// Get current working directory
	workingDir := l.workingDir
	if l.getWorkingDir != nil {
		workingDir = l.getWorkingDir()
	}

	// Get current git state
	currentState := gitstate.GetGitState(workingDir)

	// Compare with last known state
	l.mu.Lock()
	lastState := l.lastGitState
	l.mu.Unlock()

	// Check if state changed
	if !currentState.Equal(lastState) {
		l.mu.Lock()
		l.lastGitState = currentState
		l.mu.Unlock()

		if currentState.IsRepo {
			l.logger.Debug("git state changed",
				"worktree", currentState.Worktree,
				"branch", currentState.Branch,
				"commit", currentState.Commit)
			l.onGitStateChange(ctx, currentState)
		}
	}
}

// handleMaxTokensTruncation handles the case where the LLM response was truncated
// due to hitting the maximum output token limit. It records an error message
// informing the user and instructing the LLM to use smaller outputs.
func (l *Loop) handleMaxTokensTruncation(ctx context.Context) error {
	errorMessage := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{
				Type: llm.ContentTypeText,
				Text: "[SYSTEM ERROR: Your previous response was truncated because it exceeded the maximum output token limit. " +
					"Any tool calls in that response were lost. Please retry with smaller, incremental changes. " +
					"For file operations, break large changes into multiple smaller patches. " +
					"The user can ask you to continue if needed.]",
			},
		},
	}

	l.mu.Lock()
	l.history = append(l.history, errorMessage)
	l.mu.Unlock()

	// Record the error message
	if err := l.recordMessage(ctx, errorMessage, llm.Usage{}); err != nil {
		l.logger.Error("failed to record truncation error message", "error", err)
	}

	// End the turn - don't automatically continue
	l.checkGitStateChange(ctx)
	return nil
}

// handleToolCalls processes tool calls from the LLM response
func (l *Loop) handleToolCalls(ctx context.Context, content []llm.Content) error {
	var toolResults []llm.Content

	for _, c := range content {
		if c.Type != llm.ContentTypeToolUse {
			continue
		}

		l.logger.Debug("executing tool", "name", c.ToolName, "id", c.ID)

		// Find the tool
		var tool *llm.Tool
		for _, t := range l.tools {
			if t.Name == c.ToolName {
				tool = t
				break
			}
		}

		if tool == nil {
			l.logger.Error("tool not found", "name", c.ToolName)
			toolResults = append(toolResults, llm.Content{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: c.ID,
				ToolError: true,
				ToolResult: []llm.Content{
					{Type: llm.ContentTypeText, Text: fmt.Sprintf("Tool '%s' not found", c.ToolName)},
				},
			})
			continue
		}

		// Execute the tool with working directory set in context
		toolCtx := ctx
		if l.workingDir != "" {
			toolCtx = claudetool.WithWorkingDir(ctx, l.workingDir)
		}
		startTime := time.Now()
		result := tool.Run(toolCtx, c.ToolInput)
		endTime := time.Now()

		var toolResultContent []llm.Content
		if result.Error != nil {
			l.logger.Error("tool execution failed", "name", c.ToolName, "error", result.Error)
			toolResultContent = []llm.Content{
				{Type: llm.ContentTypeText, Text: result.Error.Error()},
			}
		} else {
			toolResultContent = result.LLMContent
			l.logger.Debug("tool executed successfully", "name", c.ToolName, "duration", endTime.Sub(startTime))
		}

		toolResults = append(toolResults, llm.Content{
			Type:             llm.ContentTypeToolResult,
			ToolUseID:        c.ID,
			ToolError:        result.Error != nil,
			ToolResult:       toolResultContent,
			ToolUseStartTime: &startTime,
			ToolUseEndTime:   &endTime,
			Display:          result.Display,
		})
	}

	if len(toolResults) > 0 {
		// Add tool results to history as a user message
		toolMessage := llm.Message{
			Role:    llm.MessageRoleUser,
			Content: toolResults,
		}

		l.mu.Lock()
		l.history = append(l.history, toolMessage)
		l.mu.Unlock()

		// Record tool result message
		if err := l.recordMessage(ctx, toolMessage, llm.Usage{}); err != nil {
			l.logger.Error("failed to record tool result message", "error", err)
		}

		// Process another LLM request with the tool results
		return l.processLLMRequest(ctx)
	}

	return nil
}

// insertMissingToolResults fixes tool_result issues in the conversation history:
//  1. Adds error results for tool_uses that were requested but not included in the next message.
//     This can happen when a request is cancelled or fails after the LLM responds with tool_use
//     blocks but before the tools execute.
//  2. Removes orphan tool_results that reference tool_use IDs not present in the immediately
//     preceding assistant message. This can happen when a tool execution completes after
//     CancelConversation has already written cancellation messages.
//
// This prevents API errors like:
//   - "tool_use ids were found without tool_result blocks"
//   - "unexpected tool_use_id found in tool_result blocks ... Each tool_result block must have
//     a corresponding tool_use block in the previous message"
//
// Mutates the request's Messages slice.
func (l *Loop) insertMissingToolResults(req *llm.Request) {
	if len(req.Messages) < 1 {
		return
	}

	// Scan through all messages looking for assistant messages with tool_use
	// that are not immediately followed by a user message with corresponding tool_results.
	// We may need to insert synthetic user messages with tool_results or filter orphans.
	var newMessages []llm.Message
	totalInserted := 0
	totalRemoved := 0

	// Track the tool_use IDs from the most recent assistant message
	var prevAssistantToolUseIDs map[string]bool

	for i := 0; i < len(req.Messages); i++ {
		msg := req.Messages[i]

		if msg.Role == llm.MessageRoleAssistant {
			// Handle empty assistant messages - add placeholder content if not the last message
			// The API requires all messages to have non-empty content except for the optional
			// final assistant message. Empty content can happen when the model ends its turn
			// without producing any output.
			if len(msg.Content) == 0 && i < len(req.Messages)-1 {
				req.Messages[i].Content = []llm.Content{{Type: llm.ContentTypeText, Text: "(no response)"}}
				msg = req.Messages[i] // update local copy for subsequent processing
				l.logger.Debug("added placeholder content to empty assistant message", "index", i)
			}

			// Track all tool_use IDs in this assistant message
			prevAssistantToolUseIDs = make(map[string]bool)
			for _, c := range msg.Content {
				if c.Type == llm.ContentTypeToolUse {
					prevAssistantToolUseIDs[c.ID] = true
				}
			}
			newMessages = append(newMessages, msg)

			// Check if next message needs synthetic tool_results
			var toolUseContents []llm.Content
			for _, c := range msg.Content {
				if c.Type == llm.ContentTypeToolUse {
					toolUseContents = append(toolUseContents, c)
				}
			}

			if len(toolUseContents) == 0 {
				continue
			}

			// Check if next message is a user message with corresponding tool_results
			var nextMsg *llm.Message
			if i+1 < len(req.Messages) {
				nextMsg = &req.Messages[i+1]
			}

			if nextMsg == nil || nextMsg.Role != llm.MessageRoleUser {
				// Next message is not a user message (or there is no next message).
				// Insert a synthetic user message with tool_results for all tool_uses.
				var toolResultContent []llm.Content
				for _, tu := range toolUseContents {
					toolResultContent = append(toolResultContent, llm.Content{
						Type:      llm.ContentTypeToolResult,
						ToolUseID: tu.ID,
						ToolError: true,
						ToolResult: []llm.Content{{
							Type: llm.ContentTypeText,
							Text: "not executed; retry possible",
						}},
					})
				}
				syntheticMsg := llm.Message{
					Role:    llm.MessageRoleUser,
					Content: toolResultContent,
				}
				newMessages = append(newMessages, syntheticMsg)
				totalInserted += len(toolResultContent)
			}
		} else if msg.Role == llm.MessageRoleUser {
			// Filter out orphan tool_results and add missing ones
			var filteredContent []llm.Content
			existingResultIDs := make(map[string]bool)

			for _, c := range msg.Content {
				if c.Type == llm.ContentTypeToolResult {
					// Only keep tool_results that match a tool_use in the previous assistant message
					if prevAssistantToolUseIDs != nil && prevAssistantToolUseIDs[c.ToolUseID] {
						filteredContent = append(filteredContent, c)
						existingResultIDs[c.ToolUseID] = true
					} else {
						// Orphan tool_result - skip it
						totalRemoved++
						l.logger.Debug("removing orphan tool_result", "tool_use_id", c.ToolUseID)
					}
				} else {
					// Keep non-tool_result content
					filteredContent = append(filteredContent, c)
				}
			}

			// Check if we need to add missing tool_results for this user message
			if prevAssistantToolUseIDs != nil {
				var prefix []llm.Content
				for toolUseID := range prevAssistantToolUseIDs {
					if !existingResultIDs[toolUseID] {
						prefix = append(prefix, llm.Content{
							Type:      llm.ContentTypeToolResult,
							ToolUseID: toolUseID,
							ToolError: true,
							ToolResult: []llm.Content{{
								Type: llm.ContentTypeText,
								Text: "not executed; retry possible",
							}},
						})
						totalInserted++
					}
				}
				if len(prefix) > 0 {
					filteredContent = append(prefix, filteredContent...)
				}
			}

			// Only add the message if it has content
			if len(filteredContent) > 0 {
				msg.Content = filteredContent
				newMessages = append(newMessages, msg)
			} else {
				// Message is now empty after filtering - skip it entirely
				l.logger.Debug("removing empty user message after filtering orphan tool_results")
			}

			// Reset for next iteration - user message "consumes" the previous tool_uses
			prevAssistantToolUseIDs = nil
		} else {
			newMessages = append(newMessages, msg)
		}
	}

	if totalInserted > 0 || totalRemoved > 0 {
		req.Messages = newMessages
		if totalInserted > 0 {
			l.logger.Debug("inserted missing tool results", "count", totalInserted)
		}
		if totalRemoved > 0 {
			l.logger.Debug("removed orphan tool results", "count", totalRemoved)
		}
	}
}
