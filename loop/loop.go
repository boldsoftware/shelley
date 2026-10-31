package loop

import (
	"context"
	"errors"
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

var errMessagePersistence = errors.New("message persistence failed")

// stallTimeout bounds how long a single LLM request (including its inner
// transport retries) may go without a model event. Keepalives do not count,
// so a provider that keeps a dead turn's connection warm is still cut off,
// while a slow but steadily streaming response runs to completion. The
// transport's byte-level idle timeout separately catches silent sockets.
const stallTimeout = 5 * time.Minute

// maxRequestDuration bounds a single LLM request (including its inner
// transport retries) however steadily it streams, so a model that never
// finishes cannot hold a turn forever.
const maxRequestDuration = 60 * time.Minute

// MessageRecordFunc is called to record new messages to persistent storage.
// otherUsage carries the usage of indirect LLM calls affiliated with the
// message (e.g. LLM-backed tools for a tool-result message); nil for most
// messages.
type MessageRecordFunc func(ctx context.Context, message llm.Message, usage llm.Usage, otherUsage []llm.PurposedUsage) error

// WarningRecordFunc is called to record user-visible warnings that are not sent to the LLM.
type WarningRecordFunc func(ctx context.Context, text string) error

// TurnRunner overrides one turn without changing the loop's defaults.
type TurnRunner func(context.Context, RunConfig) error

type queuedMessage struct {
	message llm.Message
	run     TurnRunner
}

// GitStateChangeFunc is called when the git state changes at the end of a turn.
// This is used to record user-visible notifications about git changes.
type GitStateChangeFunc func(ctx context.Context, state *gitstate.GitState)

// Config contains all configuration needed to create a Loop.
type Config struct {
	LLM           llm.Service
	ModelID       string
	History       []llm.Message
	Tools         []*llm.Tool
	RecordMessage MessageRecordFunc
	RecordWarning WarningRecordFunc
	Logger        *slog.Logger
	System        []llm.SystemContent
	WorkingDir    string // working directory for tools
	// GitTracker, if set, is checked for git state changes at the end of
	// each turn, and OnGitStateChange is called with each change.
	GitTracker       *gitstate.Tracker
	OnGitStateChange GitStateChangeFunc
	// ThinkingLevel, when non-default, is sent on every llm.Request the loop
	// issues. Per-conversation override; ThinkingLevelDefault means "use the
	// service default".
	ThinkingLevel llm.ThinkingLevel
	// PromptCacheKey overrides provider cache affinity for the loop's main LLM
	// requests. Tool-initiated LLM calls retain conversation-local affinity.
	PromptCacheKey string
	// OnToolProgress is called when a tool reports progress (partial output).
	// It may be called concurrently by sibling tools.
	OnToolProgress llm.ToolProgressFunc
	// OnStreamDelta is called when the LLM streams a partial content delta.
	OnStreamDelta func(llm.StreamDelta)
	// OnStreamDone is called when a streaming LLM response completes,
	// before the assistant message is recorded. Use this to flush any
	// buffered stream deltas so they reach the UI before the full message.
	OnStreamDone func()
	// OnSuccessfulRequest receives each request the provider completed
	// successfully, exactly as sent, so its prefix can be replayed.
	OnSuccessfulRequest func(*llm.Request)
	// InjectMessages, if set, is called between LLM rounds (immediately
	// before each request is built, including the first of a turn). Its
	// Injection is applied to history and included in that request. Used to
	// splice subagent completion notifications into an in-flight turn as soon
	// as possible instead of waiting for the turn to end, and to swap in the
	// history after an in-place compaction. The callback owns persistence: it
	// must record the messages before returning them, so the DB sequence
	// order matches the in-memory splice point. An error ends the turn.
	InjectMessages func(ctx context.Context) (Injection, error)
	// RewriteFinalReply, if set, may turn a reply that would end the turn
	// into tool calls, at most once per turn. See Hooks.RewriteFinalReply.
	RewriteFinalReply func(context.Context, llm.Message) (llm.Message, bool)
}

// Injection is what Config.InjectMessages splices into a running turn.
type Injection struct {
	// History, if non-nil, replaces the whole conversation history (already
	// persisted, and not including Messages) before Messages are appended.
	History []llm.Message
	// System, if non-nil, replaces the system prompt.
	System []llm.SystemContent
	// Messages are appended to history.
	Messages []llm.Message
}

// Loop manages a conversation turn with an LLM including tool execution and message recording.
// Notably, when the turn ends, the "Loop" is over. TODO: maybe rename to Turn?
type Loop struct {
	llm              llm.Service
	modelID          string
	tools            []*llm.Tool
	recordMessage    MessageRecordFunc
	recordWarning    WarningRecordFunc
	history          []llm.Message
	messageQueue     []queuedMessage
	turnRunner       TurnRunner
	totalUsage       llm.Usage
	mu               sync.Mutex
	logger           *slog.Logger
	system           []llm.SystemContent
	workingDir       string
	gitTracker       *gitstate.Tracker
	onGitStateChange GitStateChangeFunc
	onToolProgress   llm.ToolProgressFunc
	onStreamDelta    func(llm.StreamDelta)
	onStreamDone     func()
	onSuccess        func(*llm.Request)
	injectMessages   func(ctx context.Context) (Injection, error)
	rewriteReply     func(context.Context, llm.Message) (llm.Message, bool)
	thinkingLevel    llm.ThinkingLevel
	promptCacheKey   string
	notify           chan struct{} // signaled when a message is queued or retry requested
	retryPending     bool          // set by Retry() to re-run processLLMRequest with current history
}

// NewLoop creates a new Loop instance with the provided configuration
func NewLoop(config Config) *Loop {
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Loop{
		llm:              config.LLM,
		modelID:          config.ModelID,
		history:          config.History,
		tools:            config.Tools,
		recordMessage:    config.RecordMessage,
		recordWarning:    config.RecordWarning,
		messageQueue:     make([]queuedMessage, 0),
		logger:           logger,
		system:           config.System,
		workingDir:       config.WorkingDir,
		gitTracker:       config.GitTracker,
		onGitStateChange: config.OnGitStateChange,
		onToolProgress:   config.OnToolProgress,
		onStreamDelta:    config.OnStreamDelta,
		onStreamDone:     config.OnStreamDone,
		onSuccess:        config.OnSuccessfulRequest,
		injectMessages:   config.InjectMessages,
		rewriteReply:     config.RewriteFinalReply,
		thinkingLevel:    config.ThinkingLevel,
		promptCacheKey:   config.PromptCacheKey,
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

// QueueTurn runs message separately from adjacent messages. Retry uses the
// same runner; the next ordinary message uses the loop's original settings.
func (l *Loop) QueueTurn(message llm.Message, run TurnRunner) {
	l.mu.Lock()
	l.messageQueue = append(l.messageQueue, queuedMessage{message, run})
	l.mu.Unlock()
	select {
	case l.notify <- struct{}{}:
	default:
	}
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
	for _, message := range messages {
		l.messageQueue = append(l.messageQueue, queuedMessage{message: message})
	}
	l.logger.Debug("queued messages", "count", len(messages))
	l.mu.Unlock()
	// Wake the run loop immediately.
	select {
	case l.notify <- struct{}{}:
	default:
	}
}

// AppendHistory appends messages to the loop's in-memory history without
// starting a turn.
//
// For out-of-band messages that are already persisted and must be visible to
// the NEXT request, but should not themselves provoke a response — e.g. the
// notice that the user moved the working directory. QueueUserMessage is the
// wrong tool for that: queued messages drive a turn.
//
// Only safe between turns. The caller must hold whatever lock keeps a turn from
// starting concurrently (see ConversationManager.SetCwd); appending mid-request
// would put a message into history that the in-flight request was built
// without, so the model would answer without having seen it.
func (l *Loop) AppendHistory(messages ...llm.Message) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.history = append(l.history, messages...)
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
		if msg.Origin != nil {
			origin := *msg.Origin
			historyCopy[i].Origin = &origin
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
			l.turnRunner = l.messageQueue[0].run
			l.history = append(l.history, l.takeQueuedLocked(l.turnRunner != nil)...)
		}
		retryPending := l.retryPending
		l.retryPending = false
		l.mu.Unlock()

		if hasQueuedMessages || retryPending {
			// Send request to LLM
			l.logger.Debug("processing queued messages", "count", 1)
			if err := l.processLLMRequest(ctx); err != nil {
				if errors.Is(err, errMessagePersistence) {
					return err
				}
				if ctx.Err() != nil {
					l.logger.Info("conversation loop canceled")
					return ctx.Err()
				}
				// The queue was drained for this request, so go straight back
				// to waiting for the user's next message or Retry.
				l.logger.Error("failed to process LLM request", "error", err)
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
		l.turnRunner = l.messageQueue[0].run
		l.history = append(l.history, l.takeQueuedLocked(l.turnRunner != nil)...)
	}
	l.mu.Unlock()

	// Process one LLM request and response
	return l.processLLMRequest(ctx)
}

// processLLMRequest runs one turn through Run. Messages queued or injected
// while the turn is running reach the model at its next request via Pending,
// and every message Run records is mirrored into the in-memory history.
func (l *Loop) processLLMRequest(ctx context.Context) error {
	l.mu.Lock()
	history := append([]llm.Message(nil), l.history...)
	thinkingLevel := l.thinkingLevel
	run := l.turnRunner
	l.mu.Unlock()

	if run == nil {
		run = Run
	}
	err := run(ctx, RunConfig{
		LLM:            l.llm,
		ModelID:        l.modelID,
		Messages:       history,
		Tools:          l.tools,
		CurrentSystem:  l.currentSystem,
		ThinkingLevel:  thinkingLevel,
		WorkingDir:     l.workingDir,
		PromptCacheKey: l.promptCacheKey,
		Pending:        l.pending,
		Hooks:          l.hooks(),
		Logger:         l.logger,
	})
	if err != nil {
		return err
	}
	l.checkGitStateChange(ctx)
	return nil
}

// The caller holds mu. Ordinary messages can share a turn, but an override
// is always a turn boundary.
func (l *Loop) takeQueuedLocked(isolated bool) []llm.Message {
	var messages []llm.Message
	for _, queued := range l.messageQueue {
		if len(messages) > 0 && (isolated || queued.run != nil) {
			break
		}
		messages = append(messages, queued.message)
	}
	l.messageQueue = l.messageQueue[len(messages):]
	return messages
}

func (l *Loop) currentSystem() []llm.SystemContent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.system
}

func (l *Loop) pending(ctx context.Context, messages []llm.Message) ([]llm.Message, error) {
	l.mu.Lock()
	var queued []llm.Message
	if l.turnRunner == nil && len(l.messageQueue) > 0 && l.messageQueue[0].run == nil {
		queued = l.takeQueuedLocked(false)
	}
	l.history = append(l.history, queued...)
	l.mu.Unlock()
	messages = append(messages, queued...)
	if l.injectMessages == nil {
		return messages, nil
	}
	inj, err := l.injectMessages(ctx)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	if inj.History != nil {
		l.history = cloneMessages(inj.History)
		messages = cloneMessages(inj.History)
	}
	if inj.System != nil {
		l.system = inj.System
	}
	l.mu.Unlock()
	l.appendContext(inj.Messages...)
	return append(messages, inj.Messages...), nil
}

// appendContext mirrors recorded messages into the in-memory history. Rows
// excluded from context and failed-request or refusal errors stay out; the
// truncation error stays in so the model sees its retry instructions.
func (l *Loop) appendContext(messages ...llm.Message) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, m := range messages {
		if m.ExcludedFromContext || (m.ErrorType != llm.ErrorTypeNone && m.ErrorType != llm.ErrorTypeTruncation) {
			continue
		}
		l.history = append(l.history, m)
	}
}

func (l *Loop) hooks() Hooks {
	h := Hooks{
		OnStreamDone:        l.onStreamDone,
		OnSuccessfulRequest: l.onSuccess,
		RewriteFinalReply:   l.rewriteReply,
		OnResponse: func(ctx context.Context, r Response) error {
			l.mu.Lock()
			l.totalUsage.Add(r.Usage)
			l.mu.Unlock()
			if err := l.recordMessage(ctx, r.Message, r.Usage, nil); err != nil {
				return err
			}
			l.appendContext(r.Message)
			return nil
		},
		OnToolResponse: func(ctx context.Context, r ToolResponse) error {
			if err := l.recordMessage(ctx, r.Message, llm.Usage{}, r.OtherUsage); err != nil {
				return err
			}
			l.appendContext(r.Message)
			return nil
		},
		OnWarning: l.recordWarning,
	}
	if l.onStreamDelta != nil {
		h.OnStreamingResponse = func(_ context.Context, d llm.StreamDelta) { l.onStreamDelta(d) }
	}
	if l.onToolProgress != nil {
		h.OnToolProgress = func(_ context.Context, p llm.ToolProgress) { l.onToolProgress(p) }
	}
	return h
}

func (l *Loop) checkGitStateChange(ctx context.Context) {
	if l.gitTracker == nil || l.onGitStateChange == nil {
		return
	}
	for _, state := range l.gitTracker.Changes() {
		l.logger.Debug("git state changed",
			"worktree", state.Worktree,
			"branch", state.Branch,
			"commit", state.Commit)
		l.onGitStateChange(ctx, state)
	}
}

// handleMaxTokensTruncation handles the case where the LLM response was truncated
// due to hitting the maximum output token limit. It records the truncated message
// for cost tracking (excluded from context) and an error message for the user.
func userFacingLLMError(err error, trace *llm.RequestTrace) string {
	var msg string
	var tooLong *tooLongError
	if errors.As(err, &tooLong) {
		msg = fmt.Sprintf("LLM request took too long: the model was still responding after %s, "+
			"so Shelley aborted the request. Press Retry to try again.\n\nDetails: %v",
			tooLong.limit, err)
	} else if info, ok := llm.RequestErrorInfoFromError(err); ok && info.IdleStallDuration > 0 {
		msg = fmt.Sprintf("LLM request stalled: the model produced no output for %s, "+
			"so Shelley aborted the request. Press Retry to try again.\n\nDetails: %v",
			info.IdleStallDuration, err)
	} else {
		msg = fmt.Sprintf("LLM request failed: %v", err)
	}
	if trace != nil {
		if ids := trace.String(); ids != "" {
			msg += "\n\n(" + ids + ")"
		}
	}
	return msg
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
	// Structured request metadata is authoritative when a transport or
	// provider supplies it. A provider that already exhausted its own retries
	// remains manually retryable, but must not immediately restart that policy.
	if info, ok := llm.RequestErrorInfoFromError(err); ok {
		return info.Retryable && !info.NoImmediateRetry
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
	// Structured request metadata is authoritative when a transport or
	// provider supplies it.
	if info, ok := llm.RequestErrorInfoFromError(err); ok {
		return info.Retryable
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

// maxPauseContinuations bounds how many times we will re-request to resolve a
// chain of server-side tool pauses, guarding against a pathological loop where
// the provider keeps returning pause_turn forever.
const maxPauseContinuations = 16

const (
	// cancelledToolResultText is the final line of a result cancelled by the user.
	// The UI matches this trailing sentinel in ui/src/vue/utils/toolStatus.ts.
	cancelledToolResultText   = "Tool execution cancelled by user"
	notExecutedToolResultText = "Tool not executed because cancellation happened before it started\n\n" + cancelledToolResultText
	abandonedToolResultText   = "Tool did not stop within the grace period after cancellation; its output was discarded\n\n" + cancelledToolResultText
)

const toolCancelGrace = time.Second
