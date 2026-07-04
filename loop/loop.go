package loop

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"shelley.exe.dev/gitstate"
	"shelley.exe.dev/llm"
)

// MessageRecordFunc is called to record new messages to persistent storage.
type MessageRecordFunc func(ctx context.Context, message llm.Message, usage llm.Usage) error

// WarningRecordFunc is called to record user-visible warnings that are not sent to the LLM.
type WarningRecordFunc func(ctx context.Context, text string) error

// GitStateChangeFunc is called when the git state changes at the end of a turn.
// This is used to record user-visible notifications about git changes.
type GitStateChangeFunc func(ctx context.Context, state *gitstate.GitState)

// Config contains all configuration needed to create a Loop.
type Config struct {
	LLM              llm.Service
	History          []llm.Message
	Tools            []*llm.Tool
	RecordMessage    MessageRecordFunc
	RecordWarning    WarningRecordFunc
	Logger           *slog.Logger
	System           []llm.SystemContent
	WorkingDir       string // working directory for tools
	OnGitStateChange GitStateChangeFunc
	// ThinkingLevel, when non-default, is sent on every llm.Request the loop
	// issues. Per-conversation override; ThinkingLevelDefault means "use the
	// service default".
	ThinkingLevel llm.ThinkingLevel
	// GetWorkingDir returns the current working directory for tools.
	// If set, this is called at end of turn to check for git state changes.
	// If nil, Config.WorkingDir is used as a static value.
	GetWorkingDir func() string
	// OnToolProgress is called when a tool reports progress (partial output).
	OnToolProgress llm.ToolProgressFunc
	// OnStreamDelta is called when the LLM streams a partial content delta.
	OnStreamDelta func(llm.StreamDelta)
	// OnStreamDone is called when a streaming LLM response completes,
	// before the assistant message is recorded. Use this to flush any
	// buffered stream deltas so they reach the UI before the full message.
	OnStreamDone func()
}

// Loop manages a conversation turn with an LLM including tool execution and message recording.
// Notably, when the turn ends, the "Loop" is over. TODO: maybe rename to Turn?
type Loop struct {
	llm              llm.Service
	tools            []*llm.Tool
	recordMessage    MessageRecordFunc
	recordWarning    WarningRecordFunc
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
	onToolProgress   llm.ToolProgressFunc
	onStreamDelta    func(llm.StreamDelta)
	onStreamDone     func()
	thinkingLevel    llm.ThinkingLevel
	notify           chan struct{} // signaled when a message is queued or retry requested
	retryPending     bool          // set by Retry() to re-run processLLMRequest with current history
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
		recordWarning:    config.RecordWarning,
		messageQueue:     make([]llm.Message, 0),
		logger:           logger,
		system:           config.System,
		workingDir:       config.WorkingDir,
		onGitStateChange: config.OnGitStateChange,
		getWorkingDir:    config.GetWorkingDir,
		lastGitState:     initialGitState,
		onToolProgress:   config.OnToolProgress,
		onStreamDelta:    config.OnStreamDelta,
		onStreamDone:     config.OnStreamDone,
		thinkingLevel:    config.ThinkingLevel,
		notify:           make(chan struct{}, 1),
	}
}

// Retry signals the loop to re-attempt the next LLM request without queueing
// a new user message. The loop's in-memory history is unchanged (failed
// requests don't append anything to history, and error messages are persisted
// to the DB but excluded from context on reload), so the request body sent
// will match the one that originally failed. Safe to call concurrently;
// Go() consumes the retryPending flag exactly once per outer iteration.
func (l *Loop) Retry() {
	l.mu.Lock()
	l.retryPending = true
	l.logger.Debug("retry requested", "history_len", len(l.history))
	l.mu.Unlock()
	select {
	case l.notify <- struct{}{}:
	default:
	}
}

// SetThinkingLevel updates the reasoning/thinking level sent on subsequent
// LLM requests. Safe to call concurrently; the new level applies to the next
// request the loop issues.
func (l *Loop) SetThinkingLevel(level llm.ThinkingLevel) {
	l.mu.Lock()
	l.thinkingLevel = level
	l.mu.Unlock()
}

// QueueUserMessage adds a user message to the queue to be processed
func (l *Loop) QueueUserMessage(message llm.Message) {
	l.QueueMessages(message)
}

// QueueMessages atomically appends one or more messages to the loop's queue
// in order, then wakes the loop. The messages can be of any role; this is
// useful for splicing in a synthetic tool_use / tool_result pair that must
// be appended together so the LLM sees a coherent history.
func (l *Loop) QueueMessages(messages ...llm.Message) {
	if len(messages) == 0 {
		return
	}
	l.mu.Lock()
	l.messageQueue = append(l.messageQueue, messages...)
	l.logger.Debug("queued messages", "count", len(messages))
	l.mu.Unlock()
	// Wake the run loop immediately.
	select {
	case l.notify <- struct{}{}:
	default:
	}
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
		retryPending := l.retryPending
		l.retryPending = false
		l.mu.Unlock()

		if hasQueuedMessages || retryPending {
			// Send request to LLM
			l.logger.Debug("processing queued messages", "count", 1)
			if err := l.processLLMRequest(ctx); err != nil {
				l.logger.Error("failed to process LLM request", "error", err)
				time.Sleep(time.Second) // Wait before retrying
				continue
			}
			l.logger.Debug("finished processing queued messages")
		} else {
			// No queued messages, wait for a signal or context cancellation.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-l.notify:
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

// processLLMRequest sends a request to the LLM and handles the response.
// It loops internally: when the LLM responds with tool calls, it executes
// the tools and sends another request, repeating until the turn ends or an
// error occurs. This iterative design avoids the O(n²) peak memory that
// mutual recursion (processLLMRequest ↔ executeToolCalls) caused, because
// each iteration's locals are freed before the next iteration starts.
func (l *Loop) processLLMRequest(ctx context.Context) error {
	for {
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
			Messages:      messages,
			Tools:         tools,
			System:        system,
			ThinkingLevel: l.thinkingLevel,
			OnStream:      l.onStreamDelta,
			OnRetry:       l.recordRetryWarning(ctx),
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

		// sendWithRetry issues a single LLM request, retrying transient transport
		// failures (EOF, connection reset). Provider-internal retries own
		// user-visible retry warnings; this outer retry catches transport failures
		// that escape the provider without adding noise. Each call gets its own
		// timeout to prevent indefinite hangs.
		sendWithRetry := func(req *llm.Request) (*llm.Response, error) {
			llmCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			const maxRetries = 2
			var resp *llm.Response
			var err error
			for attempt := 1; attempt <= maxRetries; attempt++ {
				resp, err = llmService.Do(llmCtx, req)
				if err == nil {
					return resp, nil
				}
				if !isRetryableError(err) || attempt == maxRetries {
					return nil, err
				}
				sleep := time.Second * time.Duration(attempt)
				l.logger.Warn("LLM request failed with retryable error, retrying",
					"error", err,
					"attempt", attempt,
					"max_retries", maxRetries)
				select {
				case <-time.After(sleep):
				case <-llmCtx.Done():
					return nil, llmCtx.Err()
				}
			}
			return resp, err
		}

		resp, err := sendWithRetry(req)

		// Resolve server-side tool "pause_turn" responses before any further
		// handling. When Anthropic pauses mid-turn to run a server-side tool
		// (e.g. web_search), it returns stop_reason=pause_turn with a
		// server_tool_use block that has no result yet. The continuation arrives
		// in a *separate* response that begins with the matching
		// web_search_tool_result. Anthropic requires the server_tool_use and its
		// result to live in the SAME message, so we re-request and merge the
		// continuation into a single assistant message rather than letting the
		// loop interleave client tool execution (which permanently splits the
		// pair and wedges the conversation). See resolvePausedTurn.
		if err == nil && resp != nil && resp.StopReason == llm.StopReasonPause {
			resp, err = l.resolvePausedTurn(ctx, sendWithRetry, req, resp)
		}

		// Flush any buffered stream deltas before recording the message,
		// so the UI sees the streaming text before the full message replaces it.
		if l.onStreamDone != nil {
			l.onStreamDone()
		}

		if err != nil {
			// Record the error as a message so it can be displayed in the UI
			// EndOfTurn must be true so the agent working state is properly updated
			errorMessage := llm.Message{
				Role: llm.MessageRoleAssistant,
				Content: []llm.Content{
					{
						Type: llm.ContentTypeText,
						Text: fmt.Sprintf("LLM request failed: %v", err),
					},
				},
				EndOfTurn:      true,
				ErrorType:      llm.ErrorTypeLLMRequest,
				ErrorRetryable: IsRetryableLLMError(err),
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

		// Handle max tokens truncation BEFORE adding to history - truncated responses
		// should not be added to history normally (they get special handling)
		if resp.StopReason == llm.StopReasonMaxTokens {
			l.logger.Warn("LLM response truncated due to max tokens")
			return l.handleMaxTokensTruncation(ctx, resp)
		}

		// Handle refusals BEFORE adding to history. On stop_reason=refusal the
		// model declines to continue and typically returns no visible content
		// (often just a thinking block). Recorded normally it becomes a silent
		// empty end-of-turn bubble, and — worse — re-queuing "continue" replays
		// the same context and refuses again, wedging the conversation in an
		// endless string of blank turns. Surface it as a visible error instead.
		if resp.StopReason == llm.StopReasonRefusal {
			l.logger.Warn("LLM declined to continue (stop_reason=refusal)")
			return l.handleRefusal(ctx, resp)
		}

		// Convert response to message and add to history
		assistantMessage := resp.ToMessage()
		l.mu.Lock()
		l.history = append(l.history, assistantMessage)
		l.mu.Unlock()

		// Record assistant message with model and timing metadata
		usageWithMeta := resp.Usage
		usageWithMeta.Model = resp.Model
		usageWithMeta.URL = resp.URL
		usageWithMeta.StartTime = resp.StartTime
		usageWithMeta.EndTime = resp.EndTime
		if err := l.recordMessage(ctx, assistantMessage, usageWithMeta); err != nil {
			l.logger.Error("failed to record assistant message", "error", err)
		}

		// If no tool calls, the turn is over
		if resp.StopReason != llm.StopReasonToolUse {
			l.checkGitStateChange(ctx)
			return nil
		}

		// Execute tool calls and loop back for the next LLM request
		l.logger.Debug("handling tool calls", "content_count", len(resp.Content))
		if err := l.executeToolCalls(ctx, resp.Content); err != nil {
			return err
		}
	}
}

// maxPauseContinuations bounds how many times we will re-request to resolve a
// chain of server-side tool pauses, guarding against a pathological loop where
// the provider keeps returning pause_turn forever.
const maxPauseContinuations = 16

// resolvePausedTurn handles a stop_reason=pause_turn response by re-requesting
// the continuation(s) and merging all blocks into a single assistant message.
//
// Anthropic pauses a turn to run a server-side tool (e.g. web_search). The
// paused response ends with a server_tool_use block whose result is not yet
// available; the continuation arrives in a follow-up response that begins with
// the matching web_search_tool_result. Because Anthropic requires the
// server_tool_use and its web_search_tool_result to live in the SAME message,
// we accumulate every block across the pause chain and return a single response
// with the final (non-pause) stop reason. This keeps the stored history valid
// on reload and prevents the client tool loop from interleaving a tool_result
// message between the server_tool_use and its result.
//
// req is the request that produced the initial paused response; it is not
// mutated — each continuation request is a shallow copy with a fresh Messages
// slice that has the running assistant turn appended.
func (l *Loop) resolvePausedTurn(
	ctx context.Context,
	send func(*llm.Request) (*llm.Response, error),
	req *llm.Request,
	resp *llm.Response,
) (*llm.Response, error) {
	// Copy the initial content so appends never alias the first response's
	// backing array.
	merged := append([]llm.Content(nil), resp.Content...)
	// Accumulate usage across the whole pause chain, starting with the initial
	// paused response's usage.
	totalUsage := resp.Usage
	// Preserve the start time of the first (paused) leg so the merged turn
	// reflects the full wall-clock duration, not just the last continuation.
	startTime := resp.StartTime
	for i := 0; resp.StopReason == llm.StopReasonPause; i++ {
		if i >= maxPauseContinuations {
			l.logger.Warn("server-side tool pause did not resolve", "continuations", i)
			break
		}
		l.logger.Debug("resolving paused turn (server-side tool)", "continuation", i+1)

		// Append the running assistant turn so the provider resumes from it.
		continueReq := *req
		continueReq.Messages = append(append([]llm.Message(nil), req.Messages...),
			llm.Message{Role: llm.MessageRoleAssistant, Content: merged})

		next, err := send(&continueReq)
		if err != nil {
			return nil, err
		}
		totalUsage.Add(next.Usage)
		merged = append(merged, next.Content...)
		resp = next
	}

	// Return a single response carrying every block from the pause chain with
	// the final (resolved) stop reason. Usage is the sum across the whole chain
	// (initial paused response + every continuation) so billing is not lost.
	resolved := *resp
	resolved.Content = merged
	resolved.Usage = totalUsage
	resolved.StartTime = startTime // EndTime stays at the final continuation
	return &resolved, nil
}

func (l *Loop) recordRetryWarning(ctx context.Context) func(llm.RetryEvent) {
	if l.recordWarning == nil {
		return nil
	}
	return func(event llm.RetryEvent) {
		msg := llm.FormatRetryEvent(event)
		if err := l.recordWarning(ctx, msg); err != nil {
			l.logger.Error("failed to record retry warning", "error", err)
		}
	}
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
// due to hitting the maximum output token limit. It records the truncated message
// for cost tracking (excluded from context) and an error message for the user.
func (l *Loop) handleMaxTokensTruncation(ctx context.Context, resp *llm.Response) error {
	// Record the truncated message for cost tracking, but mark it as excluded from context.
	// This preserves billing information without confusing the LLM on future turns.
	truncatedMessage := resp.ToMessage()
	truncatedMessage.ExcludedFromContext = true

	// Record the truncated message with usage metadata
	usageWithMeta := resp.Usage
	usageWithMeta.Model = resp.Model
	usageWithMeta.URL = resp.URL
	usageWithMeta.StartTime = resp.StartTime
	usageWithMeta.EndTime = resp.EndTime
	if err := l.recordMessage(ctx, truncatedMessage, usageWithMeta); err != nil {
		l.logger.Error("failed to record truncated message", "error", err)
	}

	// Record a truncation error message with EndOfTurn=true to properly signal end of turn.
	errorMessage := llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{
			{
				Type: llm.ContentTypeText,
				Text: "[SYSTEM ERROR: Your previous response was truncated because it exceeded the maximum output token limit. " +
					"Any tool calls in that response were lost. Please retry with smaller, incremental changes. " +
					"For file operations, break large changes into multiple smaller patches. " +
					"The user can ask you to continue if needed.]",
			},
		},
		EndOfTurn: true,
		ErrorType: llm.ErrorTypeTruncation,
	}

	l.mu.Lock()
	l.history = append(l.history, errorMessage)
	l.mu.Unlock()

	// Record the truncation error message
	if err := l.recordMessage(ctx, errorMessage, llm.Usage{}); err != nil {
		l.logger.Error("failed to record truncation error message", "error", err)
	}

	// End the turn - don't automatically continue
	l.checkGitStateChange(ctx)
	return nil
}

// handleRefusal handles a stop_reason=refusal response: the model declined to
// continue. Such responses usually carry no visible content (just a thinking
// block, or nothing), so recording them normally leaves a blank agent bubble
// and, because the empty response ends up in history, every follow-up
// "continue" replays the same context and refuses again. We instead record the
// raw response excluded from context (for cost tracking) and append a visible,
// non-retryable error message that ends the turn.
func (l *Loop) handleRefusal(ctx context.Context, resp *llm.Response) error {
	// Record the raw refusal for cost tracking, but keep it out of context so it
	// doesn't poison future turns (an empty/near-empty assistant turn biases the
	// model toward refusing again, and empty content blocks can wedge replay).
	rawMessage := resp.ToMessage()
	rawMessage.ExcludedFromContext = true

	usageWithMeta := resp.Usage
	usageWithMeta.Model = resp.Model
	usageWithMeta.URL = resp.URL
	usageWithMeta.StartTime = resp.StartTime
	usageWithMeta.EndTime = resp.EndTime
	if err := l.recordMessage(ctx, rawMessage, usageWithMeta); err != nil {
		l.logger.Error("failed to record refusal message", "error", err)
	}

	// Record a visible refusal notice with EndOfTurn=true. Marked non-retryable:
	// re-running the identical request just refuses again, so the UI should not
	// offer a Retry button. Rephrasing the request is what actually helps.
	errorMessage := llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{
			{
				Type: llm.ContentTypeText,
				Text: "[The model declined to continue this request (content-policy refusal) " +
					"and returned no response. Retrying the same request will likely be refused " +
					"again; try rephrasing or clarifying the intent instead.]",
			},
		},
		EndOfTurn:      true,
		ErrorType:      llm.ErrorTypeRefusal,
		ErrorRetryable: false,
	}

	l.mu.Lock()
	l.history = append(l.history, errorMessage)
	l.mu.Unlock()

	if err := l.recordMessage(ctx, errorMessage, llm.Usage{}); err != nil {
		l.logger.Error("failed to record refusal error message", "error", err)
	}

	// End the turn - don't automatically continue.
	l.checkGitStateChange(ctx)
	return nil
}

// executeToolCalls runs the tools from an LLM response and appends the results
// to l.history. It does NOT call processLLMRequest — the caller loops instead.
func (l *Loop) executeToolCalls(ctx context.Context, content []llm.Content) error {
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

		// Execute the tool with working directory and progress callback set in context
		toolCtx := ctx
		if l.workingDir != "" {
			toolCtx = llm.WithWorkingDir(ctx, l.workingDir)
		}
		if l.onToolProgress != nil {
			toolCtx = llm.WithToolProgress(toolCtx, l.onToolProgress)
		}
		toolCtx = llm.WithToolUseID(toolCtx, c.ID)
		toolCtx = llm.WithLLMService(toolCtx, l.llm)
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
		// Check for queued user messages (interruptions) before continuing.
		// This allows user messages to be processed as soon as possible.
		if len(l.messageQueue) > 0 {
			for _, msg := range l.messageQueue {
				l.history = append(l.history, msg)
			}
			l.messageQueue = l.messageQueue[:0]
			l.logger.Info("processing user interruption during tool execution")
		}
		l.mu.Unlock()

		// Record tool result message
		if err := l.recordMessage(ctx, toolMessage, llm.Usage{}); err != nil {
			l.logger.Error("failed to record tool result message", "error", err)
		}
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

// isRetryableError checks if an LLM request error should be retried by the
// loop's tight inner-retry loop (max 2 attempts, ~1s sleep). Keep this set
// narrow: this is for transport-level hiccups that have a good chance of
// succeeding immediately. Provider-level 5xx, rate limits, and scale-up
// hints are handled by the user-facing Retry button (IsRetryableLLMError),
// which has no short retry budget and won't hammer providers.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return true
	}
	lower := strings.ToLower(err.Error())
	for _, p := range []string{
		"eof",
		"connection reset",
		"connection refused",
		"no such host",
		"network is unreachable",
		"i/o timeout",
		"reset by peer",
		"broken pipe",
	} {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// IsRetryableLLMError reports whether an LLM request failure is transient and
// safe to retry by re-sending the same conversation state.
//
// Retryable: transport hiccups (EOF, resets, timeouts), upstream 5xx, gateway
// errors, Fireworks scale-up hints, rate limits. NOT retryable: auth,
// quota/credits, 400 validation errors, missing models.
//
// Note: a generic "context canceled" string CAN come from a user-initiated
// cancel as well as a server-side timeout. We classify it retryable here
// because the cancel path records its own "[Operation cancelled]" tool
// result (not an llm_request error message), so the only thing reaching this
// classifier is a non-user-initiated timeout/disconnect.
func IsRetryableLLMError(err error) bool {
	if err == nil {
		return false
	}
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return true
	}
	lower := strings.ToLower(err.Error())

	// Hard non-retryable signals override anything else.
	nonRetryable := []string{
		"credits exhausted",
		"insufficient_quota",
		"invalid api key",
		"invalid_api_key",
		"unauthorized",
		"permission denied",
		"forbidden",
		"invalid_request_error", // 400 from providers
		"model_not_found",
		"does not exist or you do not have access",
	}
	for _, p := range nonRetryable {
		if strings.Contains(lower, p) {
			return false
		}
	}

	retryableSubstrings := []string{
		// Transport-layer
		"eof",
		"connection reset",
		"connection refused",
		"no such host",
		"network is unreachable",
		"i/o timeout",
		"context deadline exceeded",
		"context canceled",
		"context cancelled",
		"deadline exceeded",
		"broken pipe",
		"reset by peer",
		"tls handshake",
		// Provider/gateway 5xx (as words, not bare numerics)
		"internal server error",
		"bad gateway",
		"service unavailable",
		"gateway timeout",
		"gateway proxy error",
		"upstream connect error",
		"overloaded",
		"rate limit",
		"too many requests",
		"server had an error processing your request",
		// Fireworks scale-up hint
		"deployment_scaling_up",
		"scaling up",
		// Generic provider "please retry" hint
		"please retry",
	}
	for _, pattern := range retryableSubstrings {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	// HTTP status codes — match them in contexts where they look like a
	// status code rather than a random number in the body.
	if httpStatus5xxRE.MatchString(lower) {
		return true
	}
	return false
}

// httpStatus5xxRE matches 5xx HTTP status codes when they appear in
// status-like contexts (after "status", "http", "code", "returned", "error
// code", or as a bare number in a typical "error code: 503" line). Avoids
// matching numbers like 500 in token counts or other unrelated payloads.
var httpStatus5xxRE = regexp.MustCompile(`(?:status|http|code|returned|response)[ :=]+5(?:00|02|03|04)\b`)
