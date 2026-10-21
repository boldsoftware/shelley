package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/llmhttp"
)

// PendingMessages returns the messages for the next model request: messages
// with whatever arrived since the previous check appended, or a replacement
// history (e.g. after an in-place compaction). Run checks before every model
// request.
type PendingMessages func(ctx context.Context, messages []llm.Message) ([]llm.Message, error)

// Response is a completed assistant message and its direct model usage.
type Response struct {
	Message llm.Message
	Usage   llm.Usage
}

// ToolResponse is a completed tool-result message and any indirect LLM usage
// incurred while executing its tools.
type ToolResponse struct {
	Message    llm.Message
	OtherUsage []llm.PurposedUsage
}

// Hooks observe agent-loop lifecycle events. Failed tool-use and tool-result
// persistence aborts the run; streaming and tool-progress hooks cannot fail.
type Hooks struct {
	OnStreamingResponse func(context.Context, llm.StreamDelta)
	OnStreamDone        func()
	OnToolProgress      func(context.Context, llm.ToolProgress)
	OnResponse          func(context.Context, Response) error
	OnSuccessfulRequest func(*llm.Request)
	OnToolResponse      func(context.Context, ToolResponse) error
	OnWarning           func(context.Context, string) error
	// RewriteFinalReply may turn a reply that would end the turn into tool
	// calls, which then run as if the model had made them. It is consulted
	// at most once per turn. A turn that ends with an error offers the
	// error message, once it is recorded.
	RewriteFinalReply func(context.Context, llm.Message) (llm.Message, bool)
}

// RunConfig describes one agent turn. It is separate from the long-lived
// Config until the conversation manager switches to Run.
type RunConfig struct {
	LLM           llm.Service
	ModelID       string
	Messages      []llm.Message
	Tools         []*llm.Tool
	System        []llm.SystemContent
	ThinkingLevel llm.ThinkingLevel
	WorkingDir    string
	// PromptCacheKey, when set, overrides provider prompt-cache affinity for
	// model requests. Tool-initiated model calls keep the context's key.
	PromptCacheKey string
	// MaxIterations bounds model requests in one run. Zero means unlimited.
	MaxIterations int
	Pending       PendingMessages
	// CurrentSystem, if set, returns the system prompt for each model
	// request, after Pending; it replaces System. It lets a turn pick up a
	// system prompt refreshed mid-turn.
	CurrentSystem func() []llm.SystemContent
	Hooks         Hooks
	Logger        *slog.Logger
}

// Run executes one agent turn. It checks RunConfig.Pending before every model
// request, appending every returned message. A terminal response ends the run.
func Run(ctx context.Context, config RunConfig) error {
	if config.LLM == nil {
		return fmt.Errorf("no LLM service configured")
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	config.Logger.Info("starting agent run", "tools", len(config.Tools))
	return config.run(ctx)
}

func cloneMessages(messages []llm.Message) []llm.Message {
	out := make([]llm.Message, len(messages))
	for i, message := range messages {
		out[i] = message
		out[i].Content = cloneContents(message.Content)
	}
	return out
}

func cloneContents(contents []llm.Content) []llm.Content {
	if contents == nil {
		return nil
	}
	out := make([]llm.Content, len(contents))
	copy(out, contents)
	for i := range out {
		if out[i].ToolResult != nil {
			out[i].ToolResult = cloneContents(out[i].ToolResult)
		}
	}
	return out
}

// run sends requests to the LLM and handles responses until the model finishes
// without requesting another client-side tool call.
func (l *RunConfig) run(ctx context.Context) error {
	messages := cloneMessages(l.Messages)
	iterations := 0
	rewritten := false
	for {
		if l.MaxIterations > 0 && iterations >= l.MaxIterations {
			return fmt.Errorf("agent loop exceeded %d iterations", l.MaxIterations)
		}
		iterations++
		if l.Pending != nil {
			next, err := l.Pending(ctx, messages)
			if err != nil {
				return fmt.Errorf("load pending messages: %w", err)
			}
			messages = next
		}

		requestMessages := cloneMessages(messages)
		tools := l.Tools
		system := l.System
		if l.CurrentSystem != nil {
			system = l.CurrentSystem()
		}
		system = append([]llm.SystemContent(nil), system...)

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
		if len(requestMessages) > 0 {
			for i := len(requestMessages) - 1; i >= 0; i-- {
				if requestMessages[i].Role == llm.MessageRoleUser && len(requestMessages[i].Content) > 0 {
					// Deep copy the message to avoid modifying the shared history
					msg := requestMessages[i]
					msg.Content = append([]llm.Content(nil), msg.Content...)
					msg.Content[len(msg.Content)-1].Cache = true
					requestMessages[i] = msg
					break
				}
			}
		}

		onRetry := l.retryWarningHook(ctx)
		req := &llm.Request{
			Messages:      requestMessages,
			Tools:         tools,
			System:        system,
			ThinkingLevel: l.ThinkingLevel,
			OnRetry:       onRetry,
		}
		if l.Hooks.OnStreamingResponse != nil {
			req.OnStream = func(delta llm.StreamDelta) {
				l.Hooks.OnStreamingResponse(ctx, delta)
			}
		}

		// Insert missing tool results if the previous message had tool_use blocks
		// without corresponding tool_result blocks. This can happen when a request
		// is cancelled or fails after the LLM responds but before tools execute.
		l.insertMissingToolResults(req)

		systemLen := 0
		for _, sys := range system {
			systemLen += len(sys.Text)
		}
		l.Logger.Debug("sending LLM request", "message_count", len(requestMessages), "tool_count", len(tools), "system_items", len(system), "system_length", systemLen)

		var requestTrace *llm.RequestTrace
		resp, err := l.sendWithRetry(ctx, req, &requestTrace)

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
			resp, err = l.resolvePausedTurn(ctx, req, resp, &requestTrace)
		}
		// Flush buffered deltas before a complete response or error is recorded.
		if l.Hooks.OnStreamDone != nil {
			l.Hooks.OnStreamDone()
		}
		if err != nil {
			// User cancellation owns its own end-of-turn bookkeeping. Avoid a
			// duplicate LLM error row, and avoid trying to persist it on a dead
			// context.
			if errors.Is(ctx.Err(), context.Canceled) {
				l.Logger.Info("LLM request aborted by loop cancellation", "error", err)
				return fmt.Errorf("LLM request failed: %w", err)
			}

			errorMessage := llm.Message{
				Role: llm.MessageRoleAssistant,
				Content: []llm.Content{
					{
						Type: llm.ContentTypeText,
						Text: userFacingLLMError(err, requestTrace),
					},
				},
				EndOfTurn:      true,
				ErrorType:      llm.ErrorTypeLLMRequest,
				ErrorRetryable: IsRetryableLLMError(err),
			}
			l.failTurn(ctx, errorMessage, &messages, &rewritten)
			return fmt.Errorf("LLM request failed: %w", err)
		}

		l.Logger.Debug("received LLM response", "content_count", len(resp.Content), "stop_reason", resp.StopReason.String(), "usage", resp.Usage.String())

		// Handle max tokens truncation BEFORE adding to history - truncated responses
		// should not be added to history normally (they get special handling)
		if resp.StopReason == llm.StopReasonMaxTokens {
			l.Logger.Warn("LLM response truncated due to max tokens")
			l.failTurn(ctx, l.truncationError(ctx, resp), &messages, &rewritten)
			return nil
		}

		// Handle refusals BEFORE adding to history. On stop_reason=refusal the
		// model declines to continue and typically returns no visible content
		// (often just a thinking block). Recorded normally it becomes a silent
		// empty end-of-turn bubble, and — worse — re-queuing "continue" replays
		// the same context and refuses again, wedging the conversation in an
		// endless string of blank turns. Surface it as a visible error instead.
		if resp.StopReason == llm.StopReasonRefusal {
			l.Logger.Warn("LLM declined to continue (stop_reason=refusal)")
			l.failTurn(ctx, l.refusalError(ctx, resp), &messages, &rewritten)
			return nil
		}

		// Retain the exact provider-visible prefix for an idle cache refresh.
		if l.Hooks.OnSuccessfulRequest != nil {
			l.Hooks.OnSuccessfulRequest(req)
		}

		// Convert response to a message, persist it through the response hook,
		// and retain it for the next model request.
		assistantMessage := resp.ToMessage()
		toolUse := resp.StopReason == llm.StopReasonToolUse
		if !toolUse && !rewritten && l.Hooks.RewriteFinalReply != nil {
			if m, ok := l.Hooks.RewriteFinalReply(ctx, assistantMessage); ok {
				assistantMessage, toolUse, rewritten = m, true, true
			}
		}
		if toolUse {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := l.emitResponse(context.WithoutCancel(ctx), assistantMessage, resp.UsageWithMeta(), "failed to record assistant tool-use message"); err != nil {
				return fmt.Errorf("%w: assistant tool-use message: %v", errMessagePersistence, err)
			}
		} else {
			l.emitResponse(ctx, assistantMessage, resp.UsageWithMeta(), "failed to record assistant message")
		}
		messages = append(messages, assistantMessage)

		if !toolUse {
			return nil
		}

		l.Logger.Debug("handling tool calls", "content_count", len(assistantMessage.Content))
		if err := l.executeToolCalls(ctx, assistantMessage.Content, &messages); err != nil {
			if errors.Is(err, errToolEndedTurn) {
				return nil
			}
			return err
		}
	}
}

// sendWithRetry issues one model request with transport retries. Provider-internal
// retries own user-visible warnings; this catches transient failures that escape
// the provider. The transport's idle timeout is the primary bound, while
// maxTurnDuration is an absolute backstop for sockets kept alive without progress.
// requestTrace retains correlation IDs even if the request fails without a response.
func (l *RunConfig) sendWithRetry(ctx context.Context, req *llm.Request, requestTrace **llm.RequestTrace) (*llm.Response, error) {
	llmCtx, cancel := context.WithTimeout(ctx, maxTurnDuration)
	defer cancel()
	if l.PromptCacheKey != "" {
		llmCtx = llmhttp.WithPromptCacheKey(llmCtx, l.PromptCacheKey)
	}
	llmCtx, *requestTrace = llm.WithRequestTrace(llmCtx)
	const maxRetries = 2
	var resp *llm.Response
	var err error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		resp, err = l.LLM.Do(llmCtx, req)
		if err == nil {
			return resp, nil
		}
		if !isRetryableError(err) || attempt == maxRetries {
			return nil, err
		}
		sleep := time.Second * time.Duration(attempt)
		l.Logger.Warn("LLM request failed with retryable error, retrying",
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

// maxPauseContinuations bounds how many times we will re-request to resolve a
// chain of server-side tool pauses, guarding against a pathological loop where
// the provider keeps returning pause_turn forever.

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
func (l *RunConfig) resolvePausedTurn(
	ctx context.Context,
	req *llm.Request,
	resp *llm.Response,
	requestTrace **llm.RequestTrace,
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
			l.Logger.Warn("server-side tool pause did not resolve", "continuations", i)
			break
		}
		l.Logger.Debug("resolving paused turn (server-side tool)", "continuation", i+1)

		// Append the running assistant turn so the provider resumes from it.
		continueReq := *req
		continueReq.Messages = append(append([]llm.Message(nil), req.Messages...),
			llm.Message{Role: llm.MessageRoleAssistant, Content: merged})

		next, err := l.sendWithRetry(ctx, &continueReq, requestTrace)
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

func (l *RunConfig) emitResponse(ctx context.Context, message llm.Message, usage llm.Usage, failureMessage string) error {
	if l.Hooks.OnResponse == nil {
		return nil
	}
	if err := l.Hooks.OnResponse(ctx, Response{Message: message, Usage: usage}); err != nil {
		l.Logger.Error(failureMessage, "error", err)
		return err
	}
	return nil
}

func (l *RunConfig) emitToolResponse(ctx context.Context, message llm.Message, otherUsage []llm.PurposedUsage) error {
	if l.Hooks.OnToolResponse == nil {
		return nil
	}
	if err := l.Hooks.OnToolResponse(ctx, ToolResponse{Message: message, OtherUsage: otherUsage}); err != nil {
		l.Logger.Error("failed to record tool result message", "error", err)
		return err
	}
	return nil
}

func (l *RunConfig) retryWarningHook(ctx context.Context) func(llm.RetryEvent) {
	if l.Hooks.OnWarning == nil {
		return nil
	}
	return func(event llm.RetryEvent) {
		if err := l.Hooks.OnWarning(ctx, llm.FormatRetryEvent(event)); err != nil {
			l.Logger.Error("failed to record retry warning", "error", err)
		}
	}
}

// failTurn records the error message that ends the turn. If
// RewriteFinalReply turns it into tool calls, they run as the model's own: a
// delegated subagent reports the error to its parent as it would a reply.
func (l *RunConfig) failTurn(ctx context.Context, errorMessage llm.Message, messages *[]llm.Message, rewritten *bool) {
	// Persist genuine failures even if the run context expired. This
	// terminal row clears the durable agent-working state and provides the
	// user-visible error and Retry affordance.
	l.emitResponse(context.WithoutCancel(ctx), errorMessage, llm.Usage{}, "failed to record error message")
	if *rewritten || l.Hooks.RewriteFinalReply == nil {
		return
	}
	report, ok := l.Hooks.RewriteFinalReply(ctx, errorMessage)
	if !ok {
		return
	}
	*rewritten = true
	if err := l.emitResponse(context.WithoutCancel(ctx), report, llm.Usage{}, "failed to record error report"); err != nil {
		return
	}
	*messages = append(*messages, report)
	l.executeToolCalls(ctx, report.Content, messages)
}

// truncationError records a response truncated at the maximum output token
// limit for cost tracking (excluded from context) and returns the error
// message for the user.
func (l *RunConfig) truncationError(ctx context.Context, resp *llm.Response) llm.Message {
	// Record the truncated message for cost tracking, but mark it as excluded from context.
	// This preserves billing information without confusing the LLM on future turns.
	truncatedMessage := resp.ToMessage()
	truncatedMessage.ExcludedFromContext = true

	l.emitResponse(ctx, truncatedMessage, resp.UsageWithMeta(), "failed to record truncated message")

	// The truncation error message has EndOfTurn=true to properly signal end of turn.
	return llm.Message{
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
}

// refusalError handles a stop_reason=refusal response: the model declined to
// continue. Such responses usually carry no visible content (just a thinking
// block, or nothing), so recording them normally leaves a blank agent bubble
// and, because the empty response ends up in history, every follow-up
// "continue" replays the same context and refuses again. We instead record the
// raw response excluded from context (for cost tracking) and return a visible,
// non-retryable error message that ends the turn. Neither is added to the live
// context history, matching the cold-start rehydration path.
func (l *RunConfig) refusalError(ctx context.Context, resp *llm.Response) llm.Message {
	// Record the raw refusal for cost tracking, but keep it out of context so it
	// doesn't poison future turns (an empty/near-empty assistant turn biases the
	// model toward refusing again, and empty content blocks can wedge replay).
	rawMessage := resp.ToMessage()
	rawMessage.ExcludedFromContext = true

	l.emitResponse(ctx, rawMessage, resp.UsageWithMeta(), "failed to record refusal message")

	// Build the user-visible notice. Start with the standard guidance, then
	// append every field the provider gave us in the refusal reason (category
	// and full explanation), so nothing is hidden from the user.
	noticeText := "[The model declined to continue this request. Choose another model " +
		"or rephrase the request.]"
	var refusalCategory, refusalExplanation string
	if resp.RefusalDetails != nil {
		refusalCategory = strings.TrimSpace(resp.RefusalDetails.Category)
		refusalExplanation = strings.TrimSpace(resp.RefusalDetails.Explanation)
		if refusalCategory != "" {
			noticeText += "\n\nCategory: " + refusalCategory
		}
		if refusalExplanation != "" {
			noticeText += "\n\nReason: " + refusalExplanation
		}
	}

	// The visible refusal notice has EndOfTurn=true. Marked non-retryable:
	// re-running the identical request just refuses again, so the UI should not
	// offer a Retry button. Rephrasing the request is what actually helps.
	//
	// Deliberately not appended to model-visible messages: like other error
	// system-generated, user-visible artifact that must not be sent back to the
	// model. The cold-start path already excludes it from context
	// (partitionMessages skips MessageTypeError, ListMessagesForContext skips
	// excluded rows), so keeping it out of the live in-memory history too makes
	// active-session and rehydrated behavior identical. Otherwise a rephrase in
	// the same session would show the model an assistant turn narrating its own
	// refusal, biasing it toward refusing again. (Mirrors the llm_request error
	// path above, which also records without appending.)
	refusalModel := l.ModelID
	if refusalModel == "" {
		refusalModel = resp.Model
	}
	return llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{
			{
				Type: llm.ContentTypeText,
				Text: noticeText,
			},
		},
		EndOfTurn:          true,
		ErrorType:          llm.ErrorTypeRefusal,
		ErrorRetryable:     false,
		RefusalModel:       refusalModel,
		RefusalCategory:    refusalCategory,
		RefusalExplanation: refusalExplanation,
	}
}

func (l *RunConfig) findTool(name string) *llm.Tool {
	for _, tool := range l.Tools {
		if tool.Name == name {
			return tool
		}
	}
	return nil
}

var errToolEndedTurn = errors.New("tool ended turn")

type toolCallExecution struct {
	content  llm.Content
	endsTurn bool
}

// executeToolCalls runs directory-delimited groups in order, with concurrent
// calls within each group, and appends results in original tool-call order.
func (l *RunConfig) executeToolCalls(ctx context.Context, content []llm.Content, messages *[]llm.Message) error {
	var calls []llm.Content
	for _, c := range content {
		if c.Type == llm.ContentTypeToolUse {
			calls = append(calls, c)
		}
	}
	if len(calls) == 0 {
		return nil
	}

	// Collect the usage of indirect LLM calls made by tools (keyword_search,
	// tool install validation, subagent progress summaries, ...)
	// so it can be attached to the tool-result message below. Tool calls run
	// concurrently, so the accumulator is mutex-guarded.
	var otherUsage llm.UsageAccumulator
	ctx = llm.WithUsageCollector(ctx, otherUsage.Collect)

	toolResults := make([]toolCallExecution, len(calls))
	directoryFailed := false
	for first := 0; first < len(calls); {
		directoryChange := calls[first].ToolName == "change_dir"
		last := first + 1
		if !directoryChange {
			for last < len(calls) && calls[last].ToolName != "change_dir" {
				last++
			}
		}

		skipReason := ""
		if ctx.Err() != nil {
			skipReason = notExecutedToolResultText
		} else if directoryFailed {
			var request struct {
				Path string `json:"path"`
			}
			if !directoryChange || json.Unmarshal(calls[first].ToolInput, &request) != nil || !filepath.IsAbs(request.Path) {
				skipReason = "Not executed: a preceding change_dir failed. Use an absolute change_dir to recover."
			}
		}
		if skipReason != "" {
			for i := first; i < last; i++ {
				toolResults[i].content = llm.Content{
					Type:       llm.ContentTypeToolResult,
					ToolUseID:  calls[i].ID,
					ToolError:  true,
					ToolResult: llm.TextContent(skipReason),
				}
			}
		} else {
			l.executeToolGroup(ctx, calls[first:last], toolResults[first:last], len(calls))
		}
		if directoryChange {
			directoryFailed = toolResults[first].content.ToolError
		}
		first = last
	}

	contents := make([]llm.Content, len(toolResults))
	endsTurn := false
	for i, result := range toolResults {
		contents[i] = result.content
		endsTurn = endsTurn || result.endsTurn
	}

	// Keep every result in history and persist it, including interrupted and
	// never-started calls, without inheriting the canceled tool context.
	toolMessage := llm.Message{Role: llm.MessageRoleUser, Content: contents}
	if err := l.emitToolResponse(context.WithoutCancel(ctx), toolMessage, otherUsage.Take()); err != nil {
		return fmt.Errorf("%w: tool result message: %v", errMessagePersistence, err)
	}
	*messages = append(*messages, toolMessage)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if endsTurn {
		// A tool result is a user-role row; by itself it cannot clear the
		// persisted agent_working flag or signal end-of-turn to subscribers.
		// The empty excluded assistant marker does both without adding a
		// visible bubble or another message to the model's future context.
		marker := llm.Message{Role: llm.MessageRoleAssistant, EndOfTurn: true, ExcludedFromContext: true}
		if err := l.emitResponse(context.WithoutCancel(ctx), marker, llm.Usage{}, "failed to record tool end-of-turn"); err != nil {
			return fmt.Errorf("%w: tool end-of-turn: %v", errMessagePersistence, err)
		}
		return errToolEndedTurn
	}
	return nil
}

func (l *RunConfig) executeToolGroup(ctx context.Context, calls []llm.Content, toolResults []toolCallExecution, batchSize int) {
	// Every worker reaches start before any tool is invoked. Cancellation
	// before release produces a never-started result for every call.
	var ready, finishedWorkers sync.WaitGroup
	ready.Add(len(calls))
	start := make(chan struct{})
	run := false
	sequentialTails := make(map[string]<-chan struct{})
	for i, call := range calls {
		tool := l.findTool(call.ToolName)
		var predecessor <-chan struct{}
		var successor chan struct{}
		if tool != nil && tool.Sequential {
			predecessor = sequentialTails[call.ToolName]
			successor = make(chan struct{})
			sequentialTails[call.ToolName] = successor
		}

		finishedWorkers.Go(func() {
			if successor != nil {
				defer close(successor)
			}
			ready.Done()
			<-start
			execute := run
			if execute && predecessor != nil {
				<-predecessor
				execute = ctx.Err() == nil
			}
			if !execute {
				toolResults[i].content = llm.Content{
					Type:       llm.ContentTypeToolResult,
					ToolUseID:  call.ID,
					ToolError:  true,
					ToolResult: llm.TextContent(notExecutedToolResultText),
				}
				return
			}
			if tool != nil && (tool.EndsTurn || (tool.EndsTurnWhen != nil && tool.EndsTurnWhen(call.ToolInput))) && batchSize != 1 {
				toolResults[i].content = llm.Content{
					Type:       llm.ContentTypeToolResult,
					ToolUseID:  call.ID,
					ToolError:  true,
					ToolResult: llm.TextContent("turn-ending tools must be called alone"),
				}
				return
			}
			toolResults[i] = l.executeToolCall(ctx, call, tool)
		})
	}
	ready.Wait()
	run = ctx.Err() == nil
	close(start)
	finishedWorkers.Wait()
}

// executeToolCall runs one client-side tool call after its sibling cohort has
// crossed the start barrier. Do not pre-check ctx: a released sibling is
// logically started even when cancellation reaches it before the scheduler.
func (l *RunConfig) executeToolCall(ctx context.Context, call llm.Content, tool *llm.Tool) toolCallExecution {
	l.Logger.Debug("executing tool", "name", call.ToolName, "id", call.ID)

	if tool == nil {
		l.Logger.Error("tool not found", "name", call.ToolName)
		return toolCallExecution{content: llm.Content{
			Type:      llm.ContentTypeToolResult,
			ToolUseID: call.ID,
			ToolError: true,
			ToolResult: []llm.Content{
				{Type: llm.ContentTypeText, Text: fmt.Sprintf("Tool '%s' not found", call.ToolName)},
			},
		}}
	}

	toolCtx := ctx
	if l.WorkingDir != "" {
		toolCtx = llm.WithWorkingDir(toolCtx, l.WorkingDir)
	}
	if l.Hooks.OnToolProgress != nil {
		toolCtx = llm.WithToolProgress(toolCtx, func(progress llm.ToolProgress) {
			l.Hooks.OnToolProgress(ctx, progress)
		})
	}
	toolCtx = llm.WithToolUseID(toolCtx, call.ID)
	toolCtx = llm.WithLLMService(toolCtx, l.LLM)

	startTime := time.Now()
	resultCh := make(chan llm.ToolOut, 1)
	go func() { resultCh <- tool.Run(toolCtx, call.ToolInput) }()

	var result llm.ToolOut
	abandoned := false
	// Prefer an already-completed result before looking at cancellation. Once
	// selected, that result remains authoritative even if ctx is cancelled.
	select {
	case result = <-resultCh:
	default:
		select {
		case result = <-resultCh:
		case <-ctx.Done():
			grace := time.NewTimer(toolCancelGrace)
			select {
			case result = <-resultCh:
				if !grace.Stop() {
					<-grace.C
				}
			case <-grace.C:
				abandoned = true
			}
		}
	}
	endTime := time.Now()

	if abandoned {
		// A context-ignoring goroutine cannot be forcefully stopped. Its buffered
		// result has no consumer after this point, so it cannot publish late.
		l.Logger.Warn("tool ignored cancellation; abandoning", "name", call.ToolName, "id", call.ID)
		return toolCallExecution{content: llm.Content{
			Type:             llm.ContentTypeToolResult,
			ToolUseID:        call.ID,
			ToolError:        true,
			ToolResult:       llm.TextContent(abandonedToolResultText),
			ToolUseStartTime: &startTime,
			ToolUseEndTime:   &endTime,
		}}
	}

	toolResultContent := result.LLMContent
	if result.Error != nil {
		text := result.Error.Error()
		// Cancellation is a property of this tool's own result, not the shared
		// context: a sibling may cancel after this tool has already failed.
		if errors.Is(result.Error, context.Canceled) {
			l.Logger.Info("tool cancelled by user", "name", call.ToolName)
			text = strings.TrimRight(text, "\r\n") + "\n\n" + cancelledToolResultText
		} else {
			l.Logger.Error("tool execution failed", "name", call.ToolName, "error", result.Error)
		}
		toolResultContent = llm.TextContent(text)
	} else {
		l.Logger.Debug("tool executed successfully", "name", call.ToolName, "duration", endTime.Sub(startTime))
	}

	return toolCallExecution{
		content: llm.Content{
			Type:             llm.ContentTypeToolResult,
			ToolUseID:        call.ID,
			ToolError:        result.Error != nil,
			ToolResult:       toolResultContent,
			ToolUseStartTime: &startTime,
			ToolUseEndTime:   &endTime,
			Display:          result.Display,
		},
		endsTurn: (tool.EndsTurn || result.EndTurn) && result.Error == nil,
	}
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
func (l *RunConfig) insertMissingToolResults(req *llm.Request) {
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
				l.Logger.Debug("added placeholder content to empty assistant message", "index", i)
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
						l.Logger.Debug("removing orphan tool_result", "tool_use_id", c.ToolUseID)
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
				l.Logger.Debug("removing empty user message after filtering orphan tool_results")
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
			l.Logger.Debug("inserted missing tool results", "count", totalInserted)
		}
		if totalRemoved > 0 {
			l.Logger.Debug("removed orphan tool results", "count", totalRemoved)
		}
	}
}
