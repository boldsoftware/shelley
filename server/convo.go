package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
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

// pendingBatchKind discriminates the two sources of queued work.
type pendingBatchKind int

const (
	// pendingBatchUser is a user-typed message queued during a busy turn or
	// distillation. There is NO messages row for it: the message lives in the
	// conversation's queued_messages JSON array (the persistent + broadcast
	// mirror). This in-memory batch only carries the QueuedMessage id(s) so
	// drain knows which array entries it is consuming. On drain the message is
	// inserted as a normal, immutable user row and removed from the array.
	pendingBatchUser pendingBatchKind = iota
	// pendingBatchSubagentDone is a synthetic tool_use/tool_result pair
	// from a finished child subagent. The DB rows do NOT exist yet; drain
	// records them in order, then feeds them to the loop as a single
	// atomic batch via loop.QueueMessages.
	pendingBatchSubagentDone
)

// pendingBatch is one atomic unit of work waiting in the conversation's queue.
// All Messages in a batch are fed to the loop together (loop.QueueMessages),
// so paired sequences like (assistant tool_use, user tool_result) never
// interleave with other batches.
type pendingBatch struct {
	Kind     pendingBatchKind
	Messages []llm.Message
	ModelID  string
	// MessageIDs is non-empty only for Kind=pendingBatchUser. It holds the
	// QueuedMessage ids in the conversation's queued_messages array (NOT
	// messages-row ids — no row exists yet). Used to remove the entry from
	// the array on drain or cancel. Indexed parallel to Messages.
	MessageIDs []string
	// UserEmail is the exe.dev author of a queued user message (Kind=
	// pendingBatchUser), captured at queue time. Stamped onto the messages
	// row when the batch drains (drain runs on a background context, so the
	// value can't be read from the request there). Empty for other kinds and
	// for requests without the X-ExeDev-Email header.
	UserEmail string
	// SubagentConversationID is set only for Kind=pendingBatchSubagentDone.
	// It identifies the child subagent whose completion this batch notifies
	// the parent about. Used to coalesce stale notifications: if a subagent
	// finishes more than once while the parent is busy (e.g. the parent hit
	// its wait=true timeout, re-prompted the subagent, and each turn
	// finished), only the newest queued notification for that subagent
	// should remain — the earlier ones echo turns that have already been
	// superseded and would surface as stray duplicate completions.
	SubagentConversationID string
	// isStale, optionally set on Kind=pendingBatchSubagentDone batches, is
	// evaluated by enqueueBatch under cm.mu immediately before appending.
	// When it reports true, the batch is discarded: the result it announces
	// has already been delivered to the parent synchronously (see
	// notifyParentSubagentDone's producer-side invalidation). Evaluating
	// under the same mutex dropStaleParentNotification uses to scrub the
	// queue makes enqueue-vs-scrub ordering irrelevant — whichever runs
	// second sees the other's effect.
	isStale func() bool
}

// ConversationManager manages a single active conversation
type ConversationManager struct {
	conversationID      string
	conversationOptions db.ConversationOptions
	db                  *db.DB
	loop                *loop.Loop
	loopCancel          context.CancelFunc
	loopCtx             context.Context
	// loopLifecycleMu serializes loop creation and lifecycle transitions. A
	// transition releases this mutex while waiting for a loop goroutine, then
	// re-acquires and revalidates its generation before publishing teardown.
	loopLifecycleMu sync.Mutex
	// loopGeneration identifies the currently installed loop. It advances
	// before a loop is cancelled or reset, so callbacks and delayed work can
	// reject stale generations.
	loopGeneration uint64
	// loopTearingDown prevents a replacement loop from being installed while
	// cancellation/reset is waiting for the prior loop to exit. Closed once the
	// lifecycle boundary (including cancellation bookkeeping) is complete.
	loopTearingDown   bool
	loopLifecycleDone chan struct{}
	// loopDone closes after the current loop goroutine has stopped writing.
	loopDone      chan struct{}
	mu            sync.Mutex
	lastActivity  time.Time
	modelID       string
	recordMessage loop.MessageRecordFunc
	// recordMessageBatch persists several messages in one Tx (consecutive
	// sequence ids). Used by mid-turn injection of subagent-done pairs.
	recordMessageBatch messageBatchRecordFunc
	// recordTurnStartMessage records the user message that begins a turn,
	// folding the agent_working=true flip and timestamp bump into the INSERT Tx
	// (see Server.recordTurnStartMessage). Falls back to recordMessage when nil.
	recordTurnStartMessage loop.MessageRecordFunc
	logger                 *slog.Logger
	toolSetConfig          claudetool.ToolSetConfig
	toolSet                *claudetool.ToolSet // created per-conversation when loop starts

	subpub *subpub.SubPub[StreamResponse]
	// streamPub mirrors per-conversation events to the server-wide /api/stream2
	// subscribers. Each event is tagged with the manager's ConversationID by
	// the publish helpers below before fan-out.
	streamPub *subpub.SubPub[StreamResponse]

	// streamDeltaSeq is a per-conversation, monotonically increasing counter
	// assigned to each partial stream delta broadcast to clients (see
	// streamFlusher). It lives on the manager rather than the per-loop
	// streamFlusher so the sequence keeps increasing across loop resets
	// (distillation, cancellation, model changes, new generations) within
	// the same conversation. Clients use it to detect dropped or
	// out-of-order partial updates.
	streamDeltaSeq atomic.Int64

	// hydrateMu serializes Hydrate so concurrent callers don't race on the
	// fields it populates (cwd, modelID, conversationOptions, toolSetConfig,
	// hasConversationEvents, agentWorking) between the initial unlocked
	// hydrated-check and the final write under cm.mu.
	hydrateMu sync.Mutex
	// cwdMu serializes SetCwd. The three writes it makes (the conversation row,
	// the live toolset, the notice message) are not atomic together, so two
	// concurrent moves could otherwise interleave into a state where the row
	// says one directory and the tools are in the other.
	cwdMu                 sync.Mutex
	hydrated              bool
	hasConversationEvents bool
	cwd                   string // working directory for tools
	userEmail             string // exe.dev auth email, from X-ExeDev-Email header
	serverPort            int    // TCP port the shelley server listens on, for SHELLEY_PORT/SHELLEY_URL
	slug                  string // conversation slug, for SHELLEY_CONVERSATION_SLUG

	// agentWorking tracks whether the agent is currently working.
	// This is explicitly managed and broadcast to subscribers when it changes.
	agentWorking bool

	// distilling is true while a distillation goroutine is inserting content
	// into this conversation. When true, queued messages should NOT be drained
	// immediately — they must wait until distillation finishes.
	distilling bool
	// distillSetupDone is non-nil while generation setup is creating the first
	// status/system messages. QueueMessage waits on it so user messages cannot
	// appear before the distillation status.
	distillSetupDone chan struct{}

	// pendingBatches holds batches of messages queued for delivery to the
	// loop. One queue serves both user messages and subagent-done
	// notifications, so distillation and turn-end serialization — which
	// already gate drainPendingMessages — gate both sources uniformly.
	// User batches wait for the current turn to end; subagent-done batches
	// are additionally consumed MID-TURN by takeInjectableSubagentDone at
	// the loop's next LLM round, so the parent reacts to completions
	// without waiting out its own turn.
	pendingBatches []pendingBatch

	// draining is true while a drainPendingMessages goroutine is in flight
	// for this conversation. It ensures at most one drainer runs at a time
	// so concurrent enqueues don't race to start parallel drainers (which
	// would interleave each other's batches into the loop and history).
	draining bool

	// retryMu serializes RetryLastLLMRequest so concurrent retry POSTs don't
	// produce duplicate LLM calls or double-broadcast user_data updates.
	retryMu sync.Mutex
	// thinkingMu serializes SetThinkingLevel so concurrent calls can't leave
	// the in-memory conversationOptions / loop level inconsistent with the
	// persisted value (an earlier call's in-memory assignment racing a later
	// call's DB write).
	thinkingMu sync.Mutex
	// lastRetriedErrorMessageID dedupes retry double-clicks WITHOUT mutating the
	// error message row (which would reintroduce the immutability violation).
	// Guarded by cm.mu. Once a retry kicks off for a given bottom error message,
	// a second POST for the SAME message id is rejected. It naturally resets
	// because the retried turn appends a new bottom message, so a future error
	// has a different id.
	lastRetriedErrorMessageID string

	// onStateChange is called when the conversation state changes.
	// This allows the server to broadcast state changes to all subscribers.
	onStateChange func(state ConversationState)

	// onDone is called when the agent finishes working (transitions to not working).
	// Used by subagents to notify their parent conversation.
	onDone func()

	// subagentWaitOwners counts in-flight synchronous (wait=true) subagent
	// tool calls targeting THIS (subagent) conversation. While it is >0, a
	// caller is blocked inside the subagent tool and is expected to deliver
	// this subagent's response via the tool's own return value, so
	// SetAgentWorking must NOT also fire the async onDone notification (that
	// would duplicate the response). The count is read under cm.mu atomically
	// with the working-state transition, and it is keyed by the manager
	// itself — i.e. the immutable conversation ID — so it is immune to the
	// slug renaming ("rev1" → "rev1-4") that defeated the older,
	// history-parsing suppression.
	//
	// In practice there is at most one waiter at a time: SubagentTool serializes
	// calls to the same slug, a subagent has exactly one parent, and a re-send
	// to a busy subagent waits for or queues behind the prior run. The count
	// (rather than a bool) keeps register/finish robustly balanced; the
	// "exactly one delivery" guarantee in finishSubagentWait assumes this
	// single-waiter precondition.
	subagentWaitOwners int

	// subagentFinishSuppressed records that a working→idle transition fired
	// while a synchronous waiter held a slot (so onDone was suppressed). If
	// that waiter ultimately returns WITHOUT delivering the final response
	// (the timeout path), it consults this flag to know an async completion
	// notification is still owed. Guarded by cm.mu.
	subagentFinishSuppressed bool

	// handledResponseSeq is the highest sequence id of an agent message of
	// THIS (subagent) conversation whose content the parent has either
	// RECEIVED (a wait=true tool call read it for synchronous delivery) or
	// deliberately SUPERSEDED (the parent sent the subagent new work,
	// making earlier turns' completions moot). Recorded by
	// markResponseHandled at each delivery/supersession point in
	// SubagentRunner — always BEFORE the corresponding queue-scrub
	// (dropStaleParentNotification).
	//
	// notifyParentSubagentDone builds an isStale closure over it; the
	// PARENT's enqueueBatch evaluates that closure under the parent's cm.mu
	// immediately before appending, and discards the batch when the response
	// it announces has sequence id <= this value. This closes the race where
	// the onDone notifier goroutine is delayed past the queue-scrub: the
	// scrub takes the same parent mutex the enqueue-time check runs under,
	// so whichever runs second sees the other's effect — either the scrub
	// removes the enqueued batch, or the late enqueue sees the watermark
	// (published before the scrub) and skips. Atomic (not cm.mu) because the
	// closure reads it while holding the PARENT manager's mutex — no
	// cross-manager lock ordering to reason about.
	handledResponseSeq atomic.Int64

	// notifiedResponseSeq is the highest sequence id of an agent message of
	// THIS (subagent) conversation for which a completion notification has
	// been APPENDED to the parent's queue (claimed at enqueue time by the
	// isStale closure, under the parent's cm.mu). A notifier whose response
	// seq is <= this value skips: some other notifier already announced that
	// response (or a newer one). This closes the duplicate where a DELAYED
	// notifier goroutine for turn A re-reads the subagent's latest response
	// at run time — seeing turn B's response — after B's own notifier
	// already enqueued (and possibly mid-turn-injected) it: both notifiers
	// read the same seq, only the first claim wins. Queue coalescing cannot
	// catch this case because B's batch may have already left the queue.
	notifiedResponseSeq atomic.Int64

	// cancelling is true while CancelConversation is tearing down the current
	// turn. The cancel path records a synthetic "[Operation cancelled]"
	// end-of-turn message, which flips agentWorking→idle and would otherwise
	// fire onDone — delivering a spurious subagent-completion notification to
	// the parent for a turn the user (or a resend) cut short. A cancellation
	// is not a completion, so we suppress onDone for its working→idle
	// transition. Guarded by cm.mu.
	cancelling bool
}

// messageBatchRecordFunc persists a batch of messages atomically (one Tx,
// consecutive sequence ids). See Server.recordMessages.
type messageBatchRecordFunc func(ctx context.Context, msgs []recordMessageInput) error

// NewConversationManager constructs a manager with dependencies but defers hydration until needed.
func NewConversationManager(conversationID string, database *db.DB, baseLogger *slog.Logger, toolSetConfig claudetool.ToolSetConfig, recordMessage, recordTurnStartMessage loop.MessageRecordFunc, recordMessageBatch messageBatchRecordFunc, onStateChange func(ConversationState), streamPub *subpub.SubPub[StreamResponse]) *ConversationManager {
	logger := baseLogger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("conversationID", conversationID)

	return &ConversationManager{
		conversationID:         conversationID,
		db:                     database,
		lastActivity:           time.Now(),
		recordMessage:          recordMessage,
		recordTurnStartMessage: recordTurnStartMessage,
		recordMessageBatch:     recordMessageBatch,
		logger:                 logger,
		toolSetConfig:          toolSetConfig,
		subpub:                 subpub.New[StreamResponse](),
		streamPub:              streamPub,
		onStateChange:          onStateChange,
	}
}

// broadcastStream tags data with the conversation ID and fans it out to both
// the per-conversation subpub (used by the legacy /api/conversation/<id>/stream
// endpoint) and the server-wide stream (used by /api/stream2).
func (cm *ConversationManager) broadcastStream(data StreamResponse) {
	data.ConversationID = cm.conversationID
	cm.subpub.Broadcast(data)
	if cm.streamPub != nil {
		cm.streamPub.Broadcast(data)
	}
}

// publishStream tags data with the conversation ID and publishes to the
// per-conversation subpub at the given sequence id, also broadcasting to the
// server-wide stream. Sequence ids are per-conversation and meaningless on
// the global stream, so we Broadcast rather than Publish there.
func (cm *ConversationManager) publishStream(seqID int64, data StreamResponse) {
	data.ConversationID = cm.conversationID
	cm.subpub.Publish(seqID, data)
	if cm.streamPub != nil {
		cm.streamPub.Broadcast(data)
	}
}

// RegisterEndOfTurnHook records a webhook URL to post whenever a top-level turn ends.
func (cm *ConversationManager) RegisterEndOfTurnHook(ctx context.Context, hook db.ConversationHook) error {
	if err := cm.Hydrate(ctx); err != nil {
		return err
	}
	opts, err := cm.db.RegisterConversationHook(ctx, cm.conversationID, hook)
	if err != nil {
		return err
	}
	cm.mu.Lock()
	cm.conversationOptions = opts
	cm.mu.Unlock()
	return nil
}

// SetThinkingLevel updates the conversation's reasoning/thinking level. It
// persists the new level to the conversation's stored options and, if a loop
// is already running, updates it live so the next turn uses the new level.
// reasoning is a user-facing level name ("off", "minimal", "low", "medium",
// "high", "xhigh").
//
// An empty string is a no-op: it keeps whatever level the conversation already
// has rather than resetting to the service default. This is deliberate for the
// subagent path — a caller who omits "reasoning" on a follow-up message must
// not silently downgrade a subagent that was previously given an explicit
// level. Inheriting the parent's level happens at the tool layer
// (SubagentTool.ParentReasoning), which only reaches here with a concrete
// level, never "".
//
// thinkingMu serializes the whole DB-write-then-apply sequence so concurrent
// calls can't persist one level while an earlier call's in-memory assignment
// leaves conversationOptions / the loop pinned to a stale level.
func (cm *ConversationManager) SetThinkingLevel(ctx context.Context, reasoning string) error {
	if reasoning == "" {
		return nil
	}
	if err := cm.Hydrate(ctx); err != nil {
		return err
	}

	cm.thinkingMu.Lock()
	defer cm.thinkingMu.Unlock()

	cm.mu.Lock()
	if cm.conversationOptions.ThinkingLevel == reasoning {
		cm.mu.Unlock()
		return nil
	}
	cm.mu.Unlock()

	// Atomic read-modify-write of the stored options blob so a concurrent
	// mutation of a different option field (e.g. RegisterConversationHook)
	// can't clobber, or be clobbered by, this update.
	opts, err := cm.db.SetConversationThinkingLevel(ctx, cm.conversationID, reasoning)
	if err != nil {
		return err
	}

	cm.mu.Lock()
	cm.conversationOptions = opts
	loopInstance := cm.loop
	cm.mu.Unlock()

	if loopInstance != nil {
		loopInstance.SetThinkingLevel(llm.ParseThinkingLevel(reasoning))
	}
	return nil
}

// EndOfTurnHooks returns the registered top-level end-of-turn hooks.
func (cm *ConversationManager) EndOfTurnHooks(ctx context.Context) ([]db.ConversationHook, error) {
	if err := cm.Hydrate(ctx); err != nil {
		return nil, err
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	hooks := make([]db.ConversationHook, len(cm.conversationOptions.EndOfTurnHooks))
	copy(hooks, cm.conversationOptions.EndOfTurnHooks)
	return hooks, nil
}

// SetAgentWorking updates the agent working state, persists it to the
// conversations table (so the conversation list patch stream picks it up via
// the standard Pool.OnCommit hook), and notifies the server to broadcast.
func (cm *ConversationManager) SetAgentWorking(working bool) {
	cm.setAgentWorking(working, true)
}

// syncAgentWorking flips the in-memory flag and fires the same notifications as
// SetAgentWorking but WITHOUT writing conversations.agent_working. Use it when
// the persisted value has already been written in another transaction — e.g.
// folded into a message INSERT via CreateMessageParams.MarkAgentStart/
// MarkAgentDone — so we don't pay a second commit (and a second full
// conversation-list recompute) just to re-write a value the DB already holds.
func (cm *ConversationManager) syncAgentWorking(working bool) {
	cm.setAgentWorking(working, false)
}

func (cm *ConversationManager) setAgentWorking(working, persist bool) {
	cm.mu.Lock()
	if cm.agentWorking == working {
		cm.mu.Unlock()
		return
	}
	cm.agentWorking = working
	onStateChange := cm.onStateChange
	onDone := cm.onDone
	convID := cm.conversationID
	modelID := cm.modelID
	// Decide whether to fire the async done-notification under the SAME lock
	// as the working-state flip. If a synchronous waiter is in flight against
	// this subagent, it is expected to return the response itself, so the
	// async path stays silent. Reading the counter here (atomically with
	// "agent finished") closes the race the older timeout-map/DB suppression
	// tried to paper over. We also remember that we suppressed a real finish,
	// so a waiter that gives up (times out) without delivering can recover the
	// notification rather than drop it.
	// A cancellation's working→idle transition is not a completion: suppress
	// onDone for it too, and do NOT record it as a suppressed finish (no
	// waiter is owed a deferred notification for a turn that was cut short).
	suppressDone := cm.subagentWaitOwners > 0 || cm.cancelling
	if !working && cm.subagentWaitOwners > 0 && !cm.cancelling {
		cm.subagentFinishSuppressed = true
	}
	cm.mu.Unlock()

	cm.logger.Debug("agent working state changed", "working", working, "persist", persist)
	if persist {
		if err := cm.db.SetConversationAgentWorking(context.Background(), convID, working); err != nil {
			cm.logger.Error("failed to persist agent working state", "error", err, "working", working)
		}
	}
	if onStateChange != nil {
		onStateChange(ConversationState{
			ConversationID: convID,
			Working:        working,
			Model:          modelID,
		})
	}
	if !working && onDone != nil && !suppressDone {
		onDone()
	}
}

// registerSubagentWaiter marks that a synchronous (wait=true) subagent tool
// call is in flight against this (subagent) conversation. While at least one
// waiter is registered, SetAgentWorking suppresses the async onDone
// notification, since the waiter is expected to deliver the subagent's
// response via the tool's return value. Each call must be paired with exactly
// one finishSubagentWait.
func (cm *ConversationManager) registerSubagentWaiter() {
	cm.mu.Lock()
	cm.subagentWaitOwners++
	cm.mu.Unlock()
}

// consumeSuppressedFinish clears any pending suppressed-finish flag without
// owing an async notification. The wait=true path uses it after an in-flight
// turn finishes while we wait to send a follow-up: that earlier turn's
// completion is exactly what we waited for and is superseded by the follow-up
// we are about to send, so it must NOT later be mistaken for an undelivered
// finish of the follow-up turn (which would fire a premature/duplicate
// notification if our own wait subsequently timed out).
func (cm *ConversationManager) consumeSuppressedFinish() {
	cm.mu.Lock()
	cm.subagentFinishSuppressed = false
	cm.mu.Unlock()
}

// markResponseHandled records that the parent has received or superseded
// this (subagent) conversation's agent message with the given sequence id;
// completion notifications for it (or anything older) are moot. See
// handledResponseSeq.
func (cm *ConversationManager) markResponseHandled(seq int64) {
	for {
		cur := cm.handledResponseSeq.Load()
		if seq <= cur || cm.handledResponseSeq.CompareAndSwap(cur, seq) {
			return
		}
	}
}

// handledSeq returns the highest sequence id recorded by
// markResponseHandled.
func (cm *ConversationManager) handledSeq() int64 {
	return cm.handledResponseSeq.Load()
}

// claimNotified attempts to claim the right to notify the parent about this
// (subagent) conversation's agent message with the given sequence id. It
// returns false when a notification for that response (or a newer one) has
// already been claimed. See notifiedResponseSeq.
func (cm *ConversationManager) claimNotified(seq int64) bool {
	for {
		cur := cm.notifiedResponseSeq.Load()
		if seq <= cur {
			return false
		}
		if cm.notifiedResponseSeq.CompareAndSwap(cur, seq) {
			return true
		}
	}
}

// finishSubagentWait ends a synchronous wait registered by
// registerSubagentWaiter. delivered reports whether the caller is returning
// the subagent's final response to the parent (true) or is giving up without
// it — e.g. a timeout that returns only a progress summary (false).
//
// It returns notifyOwed=true when the subagent already finished (a
// working→idle transition was suppressed because this waiter held a slot) but
// the caller is NOT delivering that result. In that case the caller must
// trigger the async completion notification itself, since no further onDone
// will fire. The whole decision is made under cm.mu so it is atomic against a
// concurrent SetAgentWorking transition: given the single-waiter precondition
// documented on subagentWaitOwners, exactly one of the two paths (onDone or
// notifyOwed) ends up delivering, never both and never neither.
func (cm *ConversationManager) finishSubagentWait(delivered bool) (notifyOwed bool) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.subagentWaitOwners > 0 {
		cm.subagentWaitOwners--
	}
	suppressed := cm.subagentFinishSuppressed
	cm.subagentFinishSuppressed = false
	// If we delivered the response, the suppressed finish is accounted for.
	// Otherwise, a suppressed finish still needs an async notification.
	return !delivered && suppressed
}

// IsAgentWorking returns the current agent working state.
func (cm *ConversationManager) IsAgentWorking() bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.agentWorking
}

// SetDistilling marks the conversation as distilling. While true, queued
// messages will not be drained immediately — they wait for distillation to
// complete and the caller to invoke drainPendingMessages.
func (cm *ConversationManager) SetDistilling(distilling bool) {
	cm.mu.Lock()
	cm.distilling = distilling
	setupDone := cm.distillSetupDone
	if !distilling {
		cm.distillSetupDone = nil
	}
	cm.mu.Unlock()
	if !distilling && setupDone != nil {
		close(setupDone)
	}
}

// BeginDistillingSetup marks the conversation as distilling and reports
// whether it acquired the distilling state. It returns false when a
// distillation is already in flight, so callers can reject concurrent
// attempts (overlapping compactions would race on the generation counter).
func (cm *ConversationManager) BeginDistillingSetup() bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.distilling {
		return false
	}
	cm.distilling = true
	if cm.distillSetupDone == nil {
		cm.distillSetupDone = make(chan struct{})
	}
	return true
}

func (cm *ConversationManager) FinishDistillingSetup() {
	cm.mu.Lock()
	setupDone := cm.distillSetupDone
	cm.distillSetupDone = nil
	cm.mu.Unlock()
	if setupDone != nil {
		close(setupDone)
	}
}

func (cm *ConversationManager) IsDistilling() bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.distilling
}

func (cm *ConversationManager) waitDistillingSetup() {
	cm.mu.Lock()
	setupDone := cm.distillSetupDone
	cm.mu.Unlock()
	if setupDone != nil {
		<-setupDone
	}
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

	// Serialize Hydrate across concurrent callers. Without this, two goroutines
	// can both observe hydrated=false above, fall through, and race on the
	// non-cm.mu-guarded writes below (cwd, conversationOptions, toolSetConfig).
	// Re-check hydrated after acquiring so we don't redo work.
	cm.hydrateMu.Lock()
	defer cm.hydrateMu.Unlock()
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

	if conversation.Slug != nil {
		cm.slug = *conversation.Slug
	}

	// Load model from conversation if available
	var modelID string
	if conversation.Model != nil {
		modelID = *conversation.Model
	}
	cm.toolSetConfig.ModelID = modelID

	// Load conversation options
	cm.conversationOptions = db.ParseConversationOptions(conversation.ConversationOptions)

	// Set ParentConversationID on toolSetConfig so that subagent tool is included
	// in the display_data tools list when generating system prompt.
	// This is also set in ensureLoop, but must be set here for Hydrate's system prompt creation.
	cm.toolSetConfig.ParentConversationID = cm.conversationID

	// Generate system prompt if missing:
	// - For user-initiated conversations: full system prompt
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
			systemMsg, err = cm.createSubagentSystemPrompt(ctx, *conversation.ParentConversationID)
		} else if conversation.UserInitiated {
			systemMsg, err = cm.createSystemPrompt(ctx)
		}
		if err != nil {
			return err
		}
		_ = systemMsg // persisted to DB; ensureLoop will read it
	}

	// Parse the persisted queued_messages array up front (outside cm.mu).
	// We turn these into in-memory user batches below so messages queued
	// before a server restart survive and still drain.
	type restoredQueued struct {
		id    string
		msg   llm.Message
		mdl   string
		email string
	}
	var restored []restoredQueued
	for _, qm := range db.ParseQueuedMessages(conversation.QueuedMessages) {
		var msg llm.Message
		if err := json.Unmarshal(qm.Llm, &msg); err != nil {
			cm.logger.Error("Failed to parse persisted queued message; dropping", "queued_id", qm.ID, "error", err)
			continue
		}
		restored = append(restored, restoredQueued{id: qm.ID, msg: msg, mdl: qm.Model, email: qm.UserEmail})
	}

	cm.mu.Lock()
	cm.hasConversationEvents = hasNonSystemMessages(messages)
	cm.lastActivity = time.Now()
	cm.hydrated = true
	cm.modelID = modelID
	// Restore array entries as user batches, but DEDUPE against any user
	// batches already in cm.pendingBatches (keyed by QueuedMessage id). The
	// drainer calls Hydrate while its in-memory batches are still present, and
	// QueueMessage persists each message to BOTH the array and pendingBatches,
	// so the same id can appear in both. Restoring a duplicate would feed it to
	// the loop and insert a second immutable row. Prepend the survivors so they
	// drain before batches that arrived while Hydrate was running.
	existingQueuedIDs := make(map[string]bool)
	for _, b := range cm.pendingBatches {
		if b.Kind == pendingBatchUser {
			for _, id := range b.MessageIDs {
				existingQueuedIDs[id] = true
			}
		}
	}
	var restoredBatches []pendingBatch
	for _, r := range restored {
		if existingQueuedIDs[r.id] {
			continue
		}
		restoredBatches = append(restoredBatches, pendingBatch{
			Kind:       pendingBatchUser,
			Messages:   []llm.Message{r.msg},
			ModelID:    r.mdl,
			MessageIDs: []string{r.id},
			UserEmail:  r.email,
		})
	}
	if len(restoredBatches) > 0 {
		cm.pendingBatches = append(restoredBatches, cm.pendingBatches...)
	}
	// Seed agentWorking from the persisted column so a fresh manager (e.g.
	// after switching back to a conversation whose loop is still running) sees
	// the real state instead of the zero value.
	cm.agentWorking = conversation.AgentWorking
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

	cm.loopLifecycleMu.Lock()
	defer cm.loopLifecycleMu.Unlock()
	cm.waitForLoopTeardownLocked()

	if err := cm.Hydrate(ctx); err != nil {
		return false, err
	}

	if err := cm.ensureLoopLocked(service, modelID); err != nil {
		return false, err
	}

	cm.mu.Lock()
	isFirst := !cm.hasConversationEvents
	cm.hasConversationEvents = true
	loopInstance := cm.loop
	cm.lastActivity = time.Now()
	recordMessage := cm.recordMessage
	recordTurnStart := cm.recordTurnStartMessage
	cm.mu.Unlock()

	if loopInstance == nil {
		return false, fmt.Errorf("conversation loop not initialized")
	}

	// Flip the in-memory working flag and notify subscribers up front so the
	// thinking indicator shows immediately. The PERSISTED agent_working=true is
	// written in the same Tx as the user-message INSERT below (via
	// recordTurnStartMessage / MarkAgentStart), so the user-message commit's
	// list-patch already carries working=true — no stale working=false snapshot,
	// and no separate working-flip commit. syncAgentWorking does the in-memory
	// flip + broadcast without its own DB write.
	if recordTurnStart != nil {
		cm.syncAgentWorking(true)
		if err := recordTurnStart(ctx, message, llm.Usage{}, nil); err != nil {
			cm.logger.Error("failed to record user message immediately", "error", err)
			// Continue anyway - the loop will also try to record it.
		}
	} else {
		// No turn-start recorder wired (e.g. a manager built without one):
		// fall back to the two-Tx ordering — persist working=true first, then
		// the message — to preserve the no-flicker guarantee. Note recordMessage
		// does not stamp user_email (it also serves tool_result rows), so this
		// fallback drops author attribution; it's unreachable in production —
		// getOrCreate*ConversationManager always wire a non-nil recordTurnStart.
		cm.SetAgentWorking(true)
		if recordMessage != nil {
			if err := recordMessage(ctx, message, llm.Usage{}, nil); err != nil {
				cm.logger.Error("failed to record user message immediately", "error", err)
			}
		}
	}

	loopInstance.QueueUserMessage(message)

	return isFirst, nil
}

// errRetryNotApplicable is returned by RetryLastLLMRequest when the latest
// message isn't a retryable error; nothing to retry.
var errRetryNotApplicable = fmt.Errorf("latest message is not a retryable error; nothing to retry")

// RetryLastLLMRequest asks the loop to re-attempt the previous LLM request.
// The error message itself remains in the conversation log — messages are an
// append-only, immutable log; partitionMessages already strips error messages
// before sending history to the LLM, so the retried request body is
// byte-identical to the failed one.
//
// The error message row is never mutated. Once the retry kicks off a new turn,
// the error is no longer the bottom-most message, so the UI stops offering the
// Retry button on its own. retryMu serializes concurrent invocations.
func (cm *ConversationManager) RetryLastLLMRequest(ctx context.Context) error {
	// Take retryMu first to serialize across concurrent retries without
	// holding cm.mu (which would block unrelated message recording and
	// state changes for the duration of the DB update + broadcast).
	cm.retryMu.Lock()
	defer cm.retryMu.Unlock()
	cm.loopLifecycleMu.Lock()
	defer cm.loopLifecycleMu.Unlock()
	cm.waitForLoopTeardownLocked()

	cm.mu.Lock()
	loopInstance := cm.loop
	logger := cm.logger
	conversationID := cm.conversationID
	database := cm.db
	cm.mu.Unlock()

	if loopInstance == nil {
		return fmt.Errorf("no active loop to retry")
	}

	latest, err := database.GetLatestActionableMessage(ctx, conversationID)
	if err != nil {
		return fmt.Errorf("failed to load latest message: %w", err)
	}
	if latest.Type != string(db.MessageTypeError) {
		return errRetryNotApplicable
	}

	// Read (never write) user_data to honor the retryable gate. A
	// non-retryable error must not start a new turn.
	ud := map[string]any{}
	if latest.UserData != nil && *latest.UserData != "" {
		if err := json.Unmarshal([]byte(*latest.UserData), &ud); err != nil {
			return fmt.Errorf("failed to parse error message user_data: %w", err)
		}
	}
	if retryable, _ := ud["retryable"].(bool); !retryable {
		return errRetryNotApplicable
	}

	// Dedupe double-clicks: if we already kicked off a retry for THIS bottom
	// error message, don't fire a second loop.Retry(). retryMu serializes us,
	// but without this both POSTs would pass the bottom-retryable-error gate
	// (no new message has been appended yet) and call Retry() twice.
	cm.mu.Lock()
	if cm.lastRetriedErrorMessageID == latest.MessageID {
		cm.mu.Unlock()
		return errRetryNotApplicable
	}
	cm.lastRetriedErrorMessageID = latest.MessageID
	cm.mu.Unlock()

	logger.Info("retrying last LLM request", "message_id", latest.MessageID)

	cm.SetAgentWorking(true)
	loopInstance.Retry()
	return nil
}

// errNotRefusal is returned by ContinueAfterRefusal when the latest message is
// not a stop_reason=refusal error, so there is nothing to continue past.
var errNotRefusal = fmt.Errorf("latest message is not a refusal; nothing to continue")

// ContinueAfterRefusal handles the "switch to Opus and continue" affordance a
// refusal error offers in the UI. A refusal is deliberately non-retryable
// (re-running the identical request on the SAME model just refuses again), but
// switching to a more capable model and re-issuing the request usually
// succeeds. This applies the requested model/reasoning change (recording the
// usual modelchange marker) and then re-fires the LLM request against the new
// model. The refusal error row is never mutated; like a retry it is excluded
// from context, so the new model sees the same request that was refused.
//
// ch carries the model switch to apply before continuing; service/modelID name
// the model to build the loop with (must match ch.NewModel when a switch is
// requested). retryMu serializes this against concurrent retries/continues.
func (cm *ConversationManager) ContinueAfterRefusal(ctx context.Context, ch ModelSettingsChange, service llm.Service, modelID string) error {
	if service == nil {
		return fmt.Errorf("llm service is required")
	}

	cm.retryMu.Lock()
	defer cm.retryMu.Unlock()

	cm.mu.Lock()
	logger := cm.logger
	conversationID := cm.conversationID
	database := cm.db
	cm.mu.Unlock()

	latest, err := database.GetLatestActionableMessage(ctx, conversationID)
	if err != nil {
		return fmt.Errorf("failed to load latest message: %w", err)
	}
	if latest.Type != string(db.MessageTypeError) {
		return errNotRefusal
	}
	// Only refusal errors offer the continue-on-another-model affordance. A
	// generic (llm_request) error uses the ordinary Retry path instead.
	ud := map[string]any{}
	if latest.UserData != nil && *latest.UserData != "" {
		if err := json.Unmarshal([]byte(*latest.UserData), &ud); err != nil {
			return fmt.Errorf("failed to parse error message user_data: %w", err)
		}
	}
	if errType, _ := ud["error_type"].(string); errType != string(llm.ErrorTypeRefusal) {
		return errNotRefusal
	}

	// Dedupe double-clicks without committing the switch yet: if we've already
	// continued past THIS bottom refusal, bail. The stamp itself is written last
	// (just before Retry), after all fallible work, so a Hydrate/ensureLoop
	// failure doesn't permanently wedge a second attempt.
	cm.mu.Lock()
	alreadyContinued := cm.lastRetriedErrorMessageID == latest.MessageID
	cm.mu.Unlock()
	if alreadyContinued {
		return errNotRefusal
	}

	// Apply the model/reasoning switch first (records the modelchange marker and
	// resets the loop so the next build uses the new settings). Skip when the
	// change is a no-op so we don't record an empty marker.
	if ch.NewModel != "" || ch.ReasoningSet {
		if err := cm.ApplyModelSettings(ctx, ch); err != nil {
			return fmt.Errorf("failed to switch model before continuing: %w", err)
		}
	}

	// Rebuild the loop against the new model and re-fire the refused request.
	// This is a lifecycle operation: once the lock is held, cancellation/reset
	// cannot replace the loop between selecting it and Retry().
	cm.loopLifecycleMu.Lock()
	defer cm.loopLifecycleMu.Unlock()
	cm.waitForLoopTeardownLocked()
	if err := cm.Hydrate(ctx); err != nil {
		return fmt.Errorf("failed to hydrate before continuing: %w", err)
	}
	if err := cm.ensureLoopLocked(service, modelID); err != nil {
		return fmt.Errorf("failed to build loop before continuing: %w", err)
	}

	cm.mu.Lock()
	loopInstance := cm.loop
	// Stamp the dedup marker now that all fallible setup has succeeded, mirroring
	// RetryLastLLMRequest's ordering (stamp immediately before Retry).
	cm.lastRetriedErrorMessageID = latest.MessageID
	cm.mu.Unlock()
	if loopInstance == nil {
		return fmt.Errorf("conversation loop not initialized")
	}

	logger.Info("continuing after refusal on new model", "message_id", latest.MessageID, "model", modelID)
	cm.SetAgentWorking(true)
	loopInstance.Retry()
	return nil
}

// ResumeInterruptedTurn re-fires the LLM request for a turn that was cut short
// by the process exiting (the upgrade-with-restart path; see
// db.ConsumeResumeAfterUpgrade and Server.resumeInterruptedConversations). No
// user message is added and no history row is mutated: the persisted messages
// are the request, and loop.insertMissingToolResults patches any dangling
// tool_use block in memory while building it.
//
// Shares retryMu with the retry/continue affordances so a resume can't race a
// user-triggered retry of the same conversation.
func (cm *ConversationManager) ResumeInterruptedTurn(ctx context.Context, service llm.Service, modelID string) error {
	if service == nil {
		return fmt.Errorf("llm service is required")
	}

	cm.retryMu.Lock()
	defer cm.retryMu.Unlock()
	cm.loopLifecycleMu.Lock()
	defer cm.loopLifecycleMu.Unlock()
	cm.waitForLoopTeardownLocked()

	if err := cm.Hydrate(ctx); err != nil {
		return fmt.Errorf("failed to hydrate before resuming: %w", err)
	}
	if err := cm.ensureLoopLocked(service, modelID); err != nil {
		return fmt.Errorf("failed to build loop before resuming: %w", err)
	}

	cm.mu.Lock()
	loopInstance := cm.loop
	logger := cm.logger
	cm.mu.Unlock()
	if loopInstance == nil {
		return fmt.Errorf("conversation loop not initialized")
	}

	logger.Info("resuming interrupted turn", "model", modelID)
	cm.SetAgentWorking(true)
	loopInstance.Retry()
	return nil
}

// QueueMessage appends a user message to the conversation's queued_messages
// JSON array (the single source of truth for queued user input) and holds it
// for delivery after the current agent turn (or distillation) completes. It
// does NOT create a messages row — the message becomes a real, immutable row
// only when it drains. The append bumps updated_at, which re-sorts the
// conversation and fires the conversation-list patch + per-conversation
// broadcast so the new queued entry reaches subscribers via stream2 diffs.
func (cm *ConversationManager) QueueMessage(ctx context.Context, s *Server, modelID string, message llm.Message) error {
	cm.waitDistillingSetup()

	cm.loopLifecycleMu.Lock()
	defer cm.loopLifecycleMu.Unlock()
	cm.waitForLoopTeardownLocked()

	llmJSON, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal queued message: %w", err)
	}
	qm := db.QueuedMessage{
		ID:        uuid.New().String(),
		Llm:       llmJSON,
		CreatedAt: time.Now().UTC(),
		Model:     modelID,
		UserEmail: userEmailFromContext(ctx),
	}
	if _, err := s.db.AppendQueuedMessage(ctx, cm.conversationID, qm); err != nil {
		return fmt.Errorf("failed to append queued message: %w", err)
	}

	// Broadcast the updated conversation (with the new queued_messages array)
	// to per-conversation subscribers. The list-patch stream is refreshed
	// automatically by the Pool.OnCommit hook fired by the append's Tx.
	go s.notifySubscribers(context.WithoutCancel(ctx), cm.conversationID)

	cm.logger.Info("Queued user message", "queued_id", qm.ID)
	cm.enqueueBatch(s, pendingBatch{
		Kind:       pendingBatchUser,
		Messages:   []llm.Message{message},
		ModelID:    modelID,
		MessageIDs: []string{qm.ID},
		UserEmail:  qm.UserEmail,
	})
	return nil
}

// EnqueueSubagentDone appends a subagent-done batch (synthetic
// assistant tool_use + matching user tool_result) onto the pending-batch
// queue. If the agent is idle and not distilling, drains immediately.
// If the parent is MID-TURN, the batch does not wait for the turn to end:
// the running loop splices it in at its next LLM round via
// takeInjectableSubagentDone (loop.Config.InjectMessages), so the parent
// reacts to the completion within the same turn. Batches that miss the
// last round of a turn (or arrive during distillation) are picked up by
// drainPendingMessages as before. The synthetic messages are NOT persisted
// here — whichever consumer takes the batch records them at take time.
//
// Why persist at delivery instead of at enqueue (crash-durability seems to
// argue for enqueue): the messages table is not an event log — it IS the
// conversation history, replayed positionally on hydrate, and the LLM API
// requires each assistant tool_use row to be immediately followed by the
// user row carrying its tool_result. Writing this pair at enqueue time,
// mid-turn, would interleave it between the running turn's own tool_use and
// tool_result rows — an invalid history that a post-crash rehydrate would
// "repair" (insertMissingToolResults) into a corrupted turn, and whose DB
// order would diverge from the order the model actually saw. Persisting at
// take time — the moment the pair enters the model-visible history — is the
// only position where the log stays valid and rehydration is faithful.
// Durability-wise little is at stake: the subagent's response itself is
// already persisted in the subagent's own conversation; this batch is just a
// derived "go look" poke, and a crash inside the one-round enqueue-to-take
// window loses only the poke, never the response.
//
// modelID is used to start the parent's loop if it's currently idle; pass
// the empty string to fall back to the manager's last-known modelID.
//
// subagentConversationID identifies the child subagent this notification is
// about; enqueueBatch uses it to drop any still-queued (not-yet-drained)
// notification from an EARLIER turn of the same subagent, so a subagent that
// finishes repeatedly while the parent is busy never piles up stale
// completions.
func (cm *ConversationManager) EnqueueSubagentDone(s *Server, modelID, subagentConversationID string, assistant, toolResult llm.Message, isStale func() bool) {
	cm.enqueueBatch(s, pendingBatch{
		Kind:                   pendingBatchSubagentDone,
		Messages:               []llm.Message{assistant, toolResult},
		ModelID:                modelID,
		SubagentConversationID: subagentConversationID,
		isStale:                isStale,
	})
}

// DropPendingSubagentDone removes any queued (not-yet-injected/drained)
// subagent-done batches for the given subagent conversation from this
// (parent) manager's pending queue, returning how many were dropped. Used
// when a subagent tool call delivers or supersedes the subagent's result
// (see dropStaleParentNotification in subagent.go): a notification still
// queued at that point would be injected (or drained) as a stale duplicate.
//
// The in-place filter is safe because both consumers of pendingBatches
// (drainPendingMessages and takeInjectableSubagentDone) snapshot AND clear/
// compact under cm.mu before processing, so no live snapshot aliases the
// backing array we compact here.
func (cm *ConversationManager) DropPendingSubagentDone(subagentConversationID string) (dropped int) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	kept := cm.pendingBatches[:0]
	for _, b := range cm.pendingBatches {
		if b.Kind == pendingBatchSubagentDone && b.SubagentConversationID == subagentConversationID {
			dropped++
			continue
		}
		kept = append(kept, b)
	}
	cm.pendingBatches = kept
	return dropped
}

// takeInjectableSubagentDone extracts all queued subagent-done batches,
// persists their synthetic tool_use/tool_result pairs, and returns the
// messages for mid-turn splicing into the running loop (see
// loop.Config.InjectMessages). Returning nil leaves the turn untouched.
//
// Persisting BEFORE returning keeps DB sequence order identical to the
// in-memory splice point: the pair lands between the tool round that just
// finished and the assistant response that reacts to it. Each pair goes in
// one Tx (consecutive sequence ids) for the same reason processBatch does. A
// batch whose persist fails is dropped, not fed — a half-written pair would
// corrupt history — mirroring processBatch's no-retry policy.
//
// While distilling or cancelling, injection is skipped entirely (the
// conversation is being rewritten / the user is taking over); distillation
// leaves the batches queued for the post-distillation drain, cancellation
// clears them. The distilling/cancelling check and the persist below are not
// atomic — a distillation or cancel can start in between — but that
// check-then-act window is the same one drainPendingMessages/processBatch
// already has. Consequences: on cancel, a validly-paired notification lands
// moments after the cutoff; on distillation, the pair may be recorded into
// the OLD generation after the compaction snapshot was taken and thus be
// absent from the new generation's context (visible in the transcript, not
// re-fed) — an accepted, pre-existing loss mode of the drain path too.
func (cm *ConversationManager) takeInjectableSubagentDone(ctx context.Context, generation uint64) []llm.Message {
	// This callback runs inside a loop goroutine. Never wait for an in-progress
	// teardown here: cancellation is waiting for this goroutine to exit. Taking
	// the lifecycle lock only long enough to validate/persist makes the winner
	// deterministic—either injection commits before cancellation begins, or the
	// stale loop injects nothing.
	cm.loopLifecycleMu.Lock()
	defer cm.loopLifecycleMu.Unlock()

	cm.mu.Lock()
	if cm.loopTearingDown || cm.loop == nil || cm.loopGeneration != generation ||
		cm.distilling || cm.cancelling || len(cm.pendingBatches) == 0 || cm.recordMessageBatch == nil {
		cm.mu.Unlock()
		return nil
	}
	var taken []pendingBatch
	kept := cm.pendingBatches[:0]
	for _, b := range cm.pendingBatches {
		if b.Kind == pendingBatchSubagentDone {
			taken = append(taken, b)
		} else {
			kept = append(kept, b)
		}
	}
	cm.pendingBatches = kept
	recordBatch := cm.recordMessageBatch
	cm.mu.Unlock()

	var out []llm.Message
	for _, b := range taken {
		inputs := make([]recordMessageInput, 0, len(b.Messages))
		for _, msg := range b.Messages {
			inputs = append(inputs, recordMessageInput{message: msg})
		}
		// WithoutCancel: ctx is the loop's context; a concurrent cancellation
		// must not abort the insert halfway and silently eat the notification.
		// The recorded pair remains valid history either way — hydration picks
		// it up even if the turn dies before the next LLM round sends it.
		if err := recordBatch(context.WithoutCancel(ctx), inputs); err != nil {
			cm.logger.Error("Failed to record injected subagent-done messages", "error", err)
			continue
		}
		cm.logger.Info("Injected subagent-done notification mid-turn",
			"subagent", b.SubagentConversationID)
		out = append(out, b.Messages...)
	}
	return out
}

// enqueueBatch appends a batch to the pending queue and, if the agent is
// idle, kicks off a drain goroutine. drainPendingMessages itself acquires
// the draining flag under cm.mu, so concurrent enqueueBatch calls can both
// safely spawn drain goroutines — only the first will own the drain; the
// others will see draining=true and exit, having already appended their
// batches for the winning drainer to pick up.
func (cm *ConversationManager) enqueueBatch(s *Server, b pendingBatch) {
	cm.mu.Lock()
	if cm.cancelling {
		cm.mu.Unlock()
		cm.logger.Info("Dropping queued batch during cancellation", "kind", b.Kind)
		return
	}
	// Producer-side invalidation (see pendingBatch.isStale): a subagent-done
	// batch whose result has already reached the parent via a synchronous
	// wait=true tool call is discarded rather than enqueued. Evaluated under
	// cm.mu so it serializes against dropStaleParentNotification's scrub.
	if b.isStale != nil && b.isStale() {
		cm.mu.Unlock()
		cm.logger.Info("Skipping subagent-done notification: already delivered synchronously",
			"subagent", b.SubagentConversationID)
		return
	}
	// Coalesce stale subagent-done notifications: if this batch notifies the
	// parent that a subagent finished, drop any still-queued (not-yet-drained)
	// notification for the SAME subagent from an earlier turn. Those earlier
	// notifications echo turns the subagent has since superseded (typically
	// because the parent's wait=true call timed out, re-prompted the subagent,
	// and each turn produced its own onDone). Draining all of them would
	// surface as multiple stray "subagent finished" messages to the parent
	// after it already believed the work was done. Only the newest matters.
	if b.Kind == pendingBatchSubagentDone && b.SubagentConversationID != "" {
		kept := cm.pendingBatches[:0]
		for _, existing := range cm.pendingBatches {
			if existing.Kind == pendingBatchSubagentDone && existing.SubagentConversationID == b.SubagentConversationID {
				continue
			}
			kept = append(kept, existing)
		}
		cm.pendingBatches = kept
	}
	cm.pendingBatches = append(cm.pendingBatches, b)
	cm.lastActivity = time.Now()
	needsDrain := !cm.agentWorking && !cm.distilling
	cm.mu.Unlock()

	if needsDrain {
		go cm.drainPendingMessages(s)
	}
}

// CancelQueuedMessages removes all pending queued *user* messages: it drops
// the in-memory user batches and clears the conversation's queued_messages
// array. Subagent-done batches stay queued: they represent work the parent
// agent still needs to acknowledge, and they live only in memory (no array
// entry).
func (cm *ConversationManager) CancelQueuedMessages(ctx context.Context, s *Server) {
	cm.mu.Lock()
	var keep []pendingBatch
	cancelled := 0
	for _, b := range cm.pendingBatches {
		if b.Kind == pendingBatchUser {
			cancelled += len(b.MessageIDs)
		} else {
			keep = append(keep, b)
		}
	}
	cm.pendingBatches = keep
	cm.mu.Unlock()

	// Clear the persistent array regardless of the in-memory count so a
	// restart-orphaned queue (array populated but no in-memory batches) can
	// still be cleared by the user.
	if _, err := s.db.ClearQueuedMessages(ctx, cm.conversationID); err != nil {
		cm.logger.Error("Failed to clear queued messages", "error", err)
		return
	}
	cm.logger.Info("Cancelled queued messages", "count", cancelled)
	// Broadcast the updated (now-empty) queued_messages array. The list-patch
	// stream refreshes via the clear Tx's Pool.OnCommit hook.
	go s.notifySubscribers(context.WithoutCancel(ctx), cm.conversationID)
}

// CancelQueuedMessage removes a single queued user message by its QueuedMessage
// id, from both the in-memory drain queue and the persistent array. Used by the
// per-ghost cancel affordance in the UI.
func (cm *ConversationManager) CancelQueuedMessage(ctx context.Context, s *Server, queuedID string) {
	cm.mu.Lock()
	var keep []pendingBatch
	removed := false
	for _, b := range cm.pendingBatches {
		if b.Kind != pendingBatchUser {
			keep = append(keep, b)
			continue
		}
		// User batches carry exactly one message (QueueMessage appends one at
		// a time), so drop the whole batch when its id matches.
		if len(b.MessageIDs) == 1 && b.MessageIDs[0] == queuedID {
			removed = true
			continue
		}
		keep = append(keep, b)
	}
	cm.pendingBatches = keep
	cm.mu.Unlock()

	if _, err := s.db.RemoveQueuedMessages(ctx, cm.conversationID, queuedID); err != nil {
		cm.logger.Error("Failed to remove queued message", "queued_id", queuedID, "error", err)
		return
	}
	cm.logger.Info("Cancelled queued message", "queued_id", queuedID, "in_memory", removed)
	go s.notifySubscribers(context.WithoutCancel(ctx), cm.conversationID)
}

// processBatch feeds one pendingBatch into the loop and handles its
// batch-kind-specific persistence side effects. It returns false when a USER
// batch failed to persist (insert error): the caller re-enqueues it so it
// retries on the next drain rather than being silently dropped (it is still in
// the queued_messages array, and Hydrate already ran). Subagent-done failures
// return true — we do NOT unwind/retry those (a half-written tool_use/result
// pair would corrupt history).
func (cm *ConversationManager) processBatch(ctx context.Context, s *Server, loopInstance *loop.Loop, b pendingBatch) (ok bool) {
	switch b.Kind {
	case pendingBatchUser:
		// User batches: no DB row exists yet — the message lives only in the
		// conversation's queued_messages array. CREATE the real, immutable
		// user row AND remove its array entry in ONE Tx (RemoveQueuedID), then
		// feed it to the loop. The new row gets a fresh sequence_id at drain
		// time — exactly the immutability we want — and the atomic removal
		// means a crash can't leave an orphan array entry that Hydrate would
		// re-feed as a duplicate.
		for i, msg := range b.Messages {
			queuedID := ""
			if i < len(b.MessageIDs) {
				queuedID = b.MessageIDs[i]
			}
			if err := s.recordDrainedQueuedMessage(ctx, cm.conversationID, queuedID, msg, b.UserEmail); err != nil {
				cm.logger.Error("Failed to record drained queued message; will retry", "error", err)
				return false
			}
		}
		// notifySubscribersNewMessage (fired by recordDrainedQueuedMessage)
		// already carried the cleaned array, so the ghost clears live; no extra
		// broadcast needed.
		loopInstance.QueueMessages(b.Messages...)
		return true
	case pendingBatchSubagentDone:
		// Subagent-done batches: persist the synthetic tool_use/tool_result
		// pair in a SINGLE transaction so they receive consecutive sequence
		// ids and land adjacently in history. Recording them in separate
		// transactions let the parent's already-running loop (woken by an
		// earlier batch) commit its own assistant message BETWEEN the
		// tool_use and its tool_result, which corrupts history: LLM APIs
		// require a tool_use block be immediately followed by its matching
		// tool_result. One atomic insert closes that interleaving window.
		// A whole-batch failure skips feeding the loop — a half-written pair
		// would corrupt history — but we do NOT retry (a partial pair can't
		// be committed by CreateMessages' single Tx anyway).
		inputs := make([]recordMessageInput, 0, len(b.Messages))
		for _, msg := range b.Messages {
			inputs = append(inputs, recordMessageInput{message: msg})
		}
		if err := s.recordMessages(ctx, cm.conversationID, inputs); err != nil {
			cm.logger.Error("Failed to record synthetic subagent messages", "error", err)
			return true // do not retry subagent-done batches
		}
		loopInstance.QueueMessages(b.Messages...)
		return true
	}
	return true
}

// drainPendingMessages processes any queued batches after an agent turn ends.
// Must be called when agentWorking transitions to false (and after
// SetDistilling(false), via runDistillNewGeneration's defer).
//
// Each batch is fed atomically to the loop via loop.QueueMessages, so paired
// sequences (assistant tool_use + user tool_result) cannot interleave with
// other batches. Batches are processed in FIFO order.
func (cm *ConversationManager) drainPendingMessages(s *Server) {
	// Take exclusive draining ownership. Other callers (turn end,
	// post-distillation defer, concurrent enqueues) bail out and let the
	// in-flight drainer pick up their batches before exiting.
	cm.mu.Lock()
	if cm.draining {
		cm.mu.Unlock()
		return
	}
	if len(cm.pendingBatches) == 0 {
		cm.mu.Unlock()
		return
	}
	cm.draining = true
	cm.mu.Unlock()
	defer func() {
		cm.mu.Lock()
		cm.draining = false
		cm.mu.Unlock()
	}()

	ctx := context.Background()

	// Feeding a batch is a lifecycle publication: cancellation/reset must not
	// clear or replace the target loop between the DB insert and QueueMessages.
	cm.loopLifecycleMu.Lock()
	defer cm.loopLifecycleMu.Unlock()
	cm.waitForLoopTeardownLocked()

restart:
	cm.mu.Lock()
	// Bail if distillation started while we were draining (or between the
	// initial draining-ownership grab and now). The pending batches stay
	// queued; runDistillNewGeneration's defer will call back into this
	// function once SetDistilling(false) returns. This preserves the
	// invariant that no batch is fed to the loop while the conversation
	// is being rewritten by distillation.
	//
	// We do NOT defensively check loopCancel / a cancellation generation
	// here: CancelQueuedMessages and CancelConversation both clear the
	// queue first, so an in-flight drain that sees an empty queue exits
	// without further side effects. A drain that snapshotted batches
	// *before* the cancel cleared them is the long-standing pre-existing
	// race; the unified queue doesn't make it worse.
	if cm.distilling {
		cm.mu.Unlock()
		return
	}
	if len(cm.pendingBatches) == 0 {
		cm.mu.Unlock()
		return
	}
	// Snapshot+clear the batches we will feed this pass. We clear up front
	// (rather than after Hydrate) so subagent-done batches keep their atomic
	// ordering guarantee: a turn-end recordMessage that fires a re-entrant
	// drainPendingMessages must NOT see these batches half-processed. The
	// loop==nil/Hydrate dedup below handles the only resulting hazard (a queued
	// user id present in BOTH this snapshot and the array Hydrate restores).
	batches := cm.pendingBatches
	cm.pendingBatches = nil
	loopInstance := cm.loop
	defaultModelID := cm.modelID
	cm.mu.Unlock()

	cm.logger.Info("Draining pending batches", "count", len(batches))

	// Pick the model from the first batch that has one set, falling back to
	// the manager's current modelID. Subagent-done batches always populate
	// ModelID from the parent's modelID at enqueue time; user batches do the
	// same from the request.
	modelID := defaultModelID
	for _, b := range batches {
		if b.ModelID != "" {
			modelID = b.ModelID
			break
		}
	}

	svc, err := s.llmManager.GetService(modelID)
	if err != nil {
		cm.logger.Error("Failed to get LLM service for queued batch", "model", modelID, "error", err)
		return
	}

	// Make sure we have a loop. For the no-loop case (e.g. post-distillation
	// or post-cancel, where CancelConversation reset hydrated=false), Hydrate+
	// ensureLoop reads history from the DB. Queued user messages have NO
	// messages row yet (they live in queued_messages), so they can't
	// double-load. Hydrate repopulates user batches from the array and appends
	// them to cm.pendingBatches (for the goto-restart pass). But the ids in our
	// just-cleared `batches` snapshot are ALSO in the array, so Hydrate would
	// restore them again — feeding the same message twice and inserting a
	// duplicate immutable row. After Hydrate we therefore drop any restored
	// user batch whose id is in this snapshot. processBatch's atomic
	// insert+removal handles the array side.
	if loopInstance == nil {
		if err := cm.Hydrate(ctx); err != nil {
			cm.logger.Error("Failed to hydrate for queued batches", "error", err)
			return
		}
		if err := cm.ensureLoopLocked(svc, modelID); err != nil {
			cm.logger.Error("Failed to start loop for queued batches", "error", err)
			return
		}
		cm.mu.Lock()
		loopInstance = cm.loop
		cm.hasConversationEvents = true
		cm.dropRestoredDuplicatesLocked(batches)
		cm.mu.Unlock()
	}
	if loopInstance == nil {
		return
	}

	var failedUser []pendingBatch
	fedAny := false
	for _, b := range batches {
		if cm.processBatch(ctx, s, loopInstance, b) {
			fedAny = true
		} else {
			// User batch failed to persist (still in the queued_messages array).
			// Re-enqueue so it retries on a LATER drain instead of being lost
			// from memory while Hydrate (which already ran) won't re-read it.
			failedUser = append(failedUser, b)
		}
	}
	if len(failedUser) > 0 {
		cm.mu.Lock()
		// Prepend so failed batches drain before newer ones, preserving order.
		cm.pendingBatches = append(failedUser, cm.pendingBatches...)
		cm.mu.Unlock()
		// Return WITHOUT goto restart: re-looping immediately would hot-spin on
		// a persistent failure (e.g. DB down). Liveness of the re-enqueued (and
		// any newer) batches is still guaranteed by an external drain trigger:
		//   - fedAny=true: we fed the loop at least one message this pass, so the
		//     loop runs and its end-of-turn recordMessage calls drainPendingMessages
		//     again, which picks up failedUser + anything enqueued meanwhile. We
		//     flip agentWorking=true to reflect that a turn is now running.
		//   - fedAny=false (pure-failure pass): we leave agentWorking=false, so the
		//     next enqueueBatch (its `!agentWorking` gate) starts a fresh drain.
		// The only "stuck until next enqueue/restart" case is a pure-failure pass
		// with no subsequent activity — an acceptable DB-down degradation; the
		// messages survive in the queued_messages array either way.
		if fedAny {
			cm.SetAgentWorking(true)
		}
		return
	}

	cm.SetAgentWorking(true)

	// More batches may have been enqueued while we were draining. Loop
	// back to pick them up under the same draining ownership so we never
	// start a second concurrent drainer.
	goto restart
}

// dropRestoredDuplicatesLocked removes from cm.pendingBatches any user batch
// whose QueuedMessage id already appears (as a pendingBatchUser) in the given
// snapshot. Hydrate restores user batches from the queued_messages array; when
// the drainer has already snapshotted those same ids for the current pass,
// restoring them would feed the message twice and insert a duplicate immutable
// row. Caller must hold cm.mu.
func (cm *ConversationManager) dropRestoredDuplicatesLocked(snapshot []pendingBatch) {
	snapIDs := make(map[string]bool)
	for _, b := range snapshot {
		if b.Kind == pendingBatchUser {
			for _, id := range b.MessageIDs {
				snapIDs[id] = true
			}
		}
	}
	if len(snapIDs) == 0 {
		return
	}
	kept := cm.pendingBatches[:0]
	for _, b := range cm.pendingBatches {
		if b.Kind == pendingBatchUser && len(b.MessageIDs) == 1 && snapIDs[b.MessageIDs[0]] {
			continue
		}
		kept = append(kept, b)
	}
	cm.pendingBatches = kept
}

const maxConsecutiveWarnings = 3

func (cm *ConversationManager) recordWarning(ctx context.Context, text string) error {
	result, err := cm.db.CreateWarningMessage(ctx, cm.conversationID, text, maxConsecutiveWarnings, "Suppressing further warnings.")
	if err != nil {
		return err
	}
	cm.Touch()
	if result.Suppressed {
		return nil
	}
	// publishStream, not subpub.Publish: warnings carry a real sequence_id, so
	// reaching only the per-conversation subpub (legacy
	// /api/conversation/<id>/stream) would hide them from the web UI, which
	// listens on /api/stream2 (streamPub). That is not just a display bug: the
	// client would see seq N missing while N+1 arrives, and its message cache
	// correctly treats that skip as a hole and discards its cached history. Since
	// warnings fire on ordinary LLM retries, that would force full conversation
	// re-downloads on any flaky-LLM day.
	cm.publishStream(result.Message.SequenceID, StreamResponse{
		Messages:     toAPIMessages([]generated.Message{*result.Message}),
		Conversation: &result.Conversation,
	})
	return nil
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
		DisplayData:    cm.systemPromptDisplayData(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to store system prompt: %w", err)
	}

	// Intentionally do NOT bump conversation updated_at here: system prompt
	// generation is internal metadata triggered lazily by Hydrate, and bumping
	// the timestamp would reorder the conversation list every time a stream
	// connects to a brand-new conversation.

	cm.logger.Info("Stored system prompt", "length", len(systemPrompt))
	return created, nil
}

// toolDisplayData builds display data from a list of tools.
func toolDisplayData(tools []*llm.Tool) map[string]any {
	type toolDesc struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	}
	var descs []toolDesc
	for _, t := range tools {
		var params json.RawMessage
		if len(t.InputSchema) > 0 && string(t.InputSchema) != "null" {
			params = t.InputSchema
		}
		descs = append(descs, toolDesc{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  params,
		})
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

func (cm *ConversationManager) systemPromptDisplayData() map[string]any {
	cfg := cm.toolSetConfig
	cfg.ToolOverrides = cm.conversationOptions.ToolOverrides
	cfg.DisableAllTools = cm.conversationOptions.DisableAllTools
	return systemPromptDisplayData(cfg)
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
		DisplayData:    cm.systemPromptDisplayData(),
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
		// Skip gitinfo messages - they are user-visible only, not sent to LLM
		if msg.Type == string(db.MessageTypeGitInfo) {
			continue
		}

		// Skip modelchange markers - user-visible only, not sent to LLM.
		if msg.Type == string(db.MessageTypeModelChange) {
			continue
		}

		// Skip slug markers - they carry only the slug call's usage, have no
		// content, and are not part of the conversation.
		if msg.Type == string(db.MessageTypeSlug) {
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

		if msg.Type == string(db.MessageTypeUser) {
			cm.applyDistillationContentOverride(&llmMsg, msg)
		}

		history = append(history, llmMsg)
	}

	return history, system
}

func (cm *ConversationManager) applyDistillationContentOverride(llmMsg *llm.Message, msg generated.Message) {
	content, ok := resolveDistilledContent(cm.logger, msg)
	if !ok {
		return
	}
	for i := range llmMsg.Content {
		if llmMsg.Content[i].Type == llm.ContentTypeText {
			llmMsg.Content[i].Text = content
			return
		}
	}
	llmMsg.Content = append(llmMsg.Content, llm.Content{Type: llm.ContentTypeText, Text: content})
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

func (cm *ConversationManager) waitForLoopTeardownLocked() {
	for {
		cm.mu.Lock()
		tearingDown := cm.loopTearingDown
		done := cm.loopLifecycleDone
		cm.mu.Unlock()
		if !tearingDown {
			return
		}

		// The loop being torn down can need cm.mu while it exits. Do not hold
		// the lifecycle mutex while waiting, or cancellation/reset would turn
		// that ordinary exit into a lock inversion.
		cm.loopLifecycleMu.Unlock()
		<-done
		cm.loopLifecycleMu.Lock()
	}
}

func (cm *ConversationManager) isCurrentLoopGeneration(generation uint64) bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return !cm.loopTearingDown && cm.loop != nil && cm.loopGeneration == generation
}

// finishLoopTeardownLocked reopens the lifecycle boundary after its owner has
// cancelled/waited/recorded all required teardown state. expectedGeneration is
// captured before the owner releases loopLifecycleMu; checking it on return
// makes a stale teardown unable to reopen a newer lifecycle. The caller must
// hold loopLifecycleMu.
func (cm *ConversationManager) finishLoopTeardownLocked(expectedGeneration uint64) {
	cm.mu.Lock()
	if !cm.loopTearingDown || cm.loopGeneration != expectedGeneration {
		cm.mu.Unlock()
		return
	}
	done := cm.loopLifecycleDone
	cm.loopTearingDown = false
	cm.loopLifecycleDone = nil
	cm.cancelling = false
	cm.mu.Unlock()
	close(done)
}

// ensureLoop creates a loop only after any prior lifecycle transition has
// completed. Callers that already hold loopLifecycleMu use ensureLoopLocked.
func (cm *ConversationManager) ensureLoop(service llm.Service, modelID string) error {
	cm.loopLifecycleMu.Lock()
	defer cm.loopLifecycleMu.Unlock()
	cm.waitForLoopTeardownLocked()
	return cm.ensureLoopLocked(service, modelID)
}

func (cm *ConversationManager) ensureLoopLocked(service llm.Service, modelID string) error {
	cm.mu.Lock()
	if cm.loop != nil {
		existingModel := cm.modelID
		cm.mu.Unlock()
		if existingModel != "" && modelID != "" && existingModel != modelID {
			return fmt.Errorf("%w: conversation already uses model %s; requested %s", errConversationModelMismatch, existingModel, modelID)
		}
		return nil
	}

	// loopLifecycleMu is held, so this value remains reserved until we publish
	// the new loop below. Capturing it in callbacks lets them reject events from
	// this loop after a later cancellation or reset advances the generation.
	generation := cm.loopGeneration + 1
	recordMessage := cm.recordMessage
	logger := cm.logger
	cwd := cm.cwd
	toolSetConfig := cm.toolSetConfig
	conversationID := cm.conversationID
	conversationOpts := cm.conversationOptions
	database := cm.db
	toolSetConfig.Env = claudetool.ShelleyEnv{
		ConversationSlug: cm.slug,
		Model:            modelID,
		UserEmail:        cm.userEmail,
		Port:             cm.serverPort,
	}
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
		cm.broadcastStream(StreamResponse{
			Conversation: &conv,
		})
		// The list patch stream refreshes from the Pool commit hook.
	}

	// Create a context with the conversation ID for LLM request recording/prefix dedup
	baseCtx := llmhttp.WithConversationID(context.Background(), conversationID)
	processCtx, cancel := context.WithTimeout(baseCtx, 12*time.Hour)

	toolSetConfig.ToolOverrides = conversationOpts.ToolOverrides
	toolSetConfig.DisableAllTools = conversationOpts.DisableAllTools
	toolSetConfig.ReasoningLevel = conversationOpts.ThinkingLevel
	toolSet := claudetool.NewToolSet(processCtx, toolSetConfig)

	// streamFlusher batches LLM stream deltas and flushes them periodically
	// to avoid overwhelming the bounded subpub queue with hundreds
	// of individual deltas per second from the Anthropic SSE stream.
	sf := newStreamFlusher(cm, 50*time.Millisecond, func() bool {
		return cm.isCurrentLoopGeneration(generation)
	})

	loopInstance := loop.NewLoop(loop.Config{
		LLM:           service,
		History:       history,
		Tools:         toolSet.Tools(),
		ThinkingLevel: llm.ParseThinkingLevel(conversationOpts.ThinkingLevel),
		RecordMessage: recordMessage,
		RecordWarning: func(ctx context.Context, text string) error {
			return cm.recordWarning(ctx, text)
		},
		Logger:        logger,
		System:        system,
		WorkingDir:    cwd,
		GetWorkingDir: toolSet.WorkingDir().Get,
		OnGitStateChange: func(ctx context.Context, state *gitstate.GitState) {
			cm.recordGitStateChange(ctx, state)
		},
		OnToolProgress: func(progress llm.ToolProgress) {
			if !cm.isCurrentLoopGeneration(generation) {
				return
			}
			cm.broadcastStream(StreamResponse{
				ToolProgress: &progress,
			})
		},
		OnStreamDelta: sf.Push,
		OnStreamDone:  sf.Flush,
		InjectMessages: func(ctx context.Context) []llm.Message {
			return cm.takeInjectableSubagentDone(ctx, generation)
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
	cm.loopGeneration = generation
	cm.loop = loopInstance
	cm.loopCancel = cancel
	cm.loopCtx = processCtx
	loopDone := make(chan struct{})
	cm.loopDone = loopDone
	cm.modelID = modelID
	cm.toolSet = toolSet
	// The cwd was read at the top of this function and baked into the toolset,
	// but building one takes a while (DB reads, tool construction) and none of
	// that happens under cm.mu — a SetCwd could have landed in between. It would
	// have found cm.toolSet still nil and skipped the toolset update, leaving the
	// tools pinned to a directory the conversation has already left, behind an
	// HTTP 200. Re-read the authoritative value here, in the same critical
	// section that publishes the toolset, so whichever of the two runs last
	// agrees with the other. (The agentWorking guard does not cover this:
	// AcceptUserMessage calls ensureLoop before it marks the agent working.)
	if cm.cwd != "" && cm.cwd != cwd {
		toolSet.WorkingDir().Set(cm.cwd)
	}
	cm.mu.Unlock()

	// Persist model for legacy conversations
	if needsPersist {
		if err := database.UpdateConversationModel(context.Background(), conversationID, modelID); err != nil {
			logger.Error("failed to persist model for legacy conversation", "error", err)
		}
	}

	go func() {
		defer close(loopDone)
		err := loopInstance.Go(processCtx)
		if err == nil || err == context.DeadlineExceeded || err == context.Canceled {
			return
		}
		if logger != nil {
			logger.Error("Conversation loop stopped", "error", err)
		} else {
			slog.Default().Error("Conversation loop stopped", "error", err)
		}
		cm.handleFatalLoopExit(loopInstance, generation)
	}()

	return nil
}

func (cm *ConversationManager) handleFatalLoopExit(loopInstance *loop.Loop, generation uint64) {
	cm.loopLifecycleMu.Lock()
	defer cm.loopLifecycleMu.Unlock()

	// This runs inside the loop goroutine before its deferred close(loopDone).
	// Never wait for an existing teardown here: cancel/reset may be waiting for
	// that very loopDone. Their generation invalidation makes this exit stale,
	// so the identity check below must simply return and let the defer unblock
	// the owner.
	var teardownGeneration uint64
	cm.mu.Lock()
	if cm.loop != loopInstance || cm.loopGeneration != generation {
		cm.mu.Unlock()
		return
	}
	cancel := cm.loopCancel
	toolSet := cm.toolSet
	cm.loopGeneration++ // invalidate callbacks from the failed loop
	teardownGeneration = cm.loopGeneration
	cm.loopTearingDown = true
	cm.loopLifecycleDone = make(chan struct{})
	cm.loopCancel = nil
	cm.loopCtx = nil
	cm.loopDone = nil
	cm.loop = nil
	cm.modelID = ""
	cm.toolSet = nil
	cm.hydrated = false
	cm.hasConversationEvents = false
	cm.cancelling = true // suppress an onDone notification for a failed turn
	cm.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if toolSet != nil {
		toolSet.Cleanup()
	}
	cm.SetAgentWorking(false)
	cm.finishLoopTeardownLocked(teardownGeneration)
}

func (cm *ConversationManager) stopLoop() {
	cm.resetLoop(false)
}

// ResetLoop drops the in-memory LLM loop so the next turn hydrates from the DB.
func (cm *ConversationManager) ResetLoop() {
	cm.resetLoop(true)
}

// resetLoop establishes a lifecycle boundary before cancelling the old loop.
// It releases loopLifecycleMu while waiting so the dying loop can finish, but
// loopTearingDown prevents any replacement from being installed in that gap.
func (cm *ConversationManager) resetLoop(markUnhydrated bool) {
	cm.loopLifecycleMu.Lock()
	cm.waitForLoopTeardownLocked()

	var teardownGeneration uint64
	cm.mu.Lock()
	loopInstance := cm.loop
	loopDone := cm.loopDone
	cancel := cm.loopCancel
	toolSet := cm.toolSet
	cm.loopGeneration++ // invalidate delayed callbacks before cancellation
	teardownGeneration = cm.loopGeneration
	if loopInstance == nil {
		if markUnhydrated {
			cm.hydrated = false
			cm.hasConversationEvents = false
		}
		cm.mu.Unlock()
		cm.loopLifecycleMu.Unlock()
		return
	}
	cm.loopTearingDown = true
	cm.loopLifecycleDone = make(chan struct{})
	cm.loopCancel = nil
	cm.loopCtx = nil
	cm.loopDone = nil
	cm.loop = nil
	cm.modelID = ""
	cm.toolSet = nil
	if markUnhydrated {
		cm.hydrated = false
		cm.hasConversationEvents = false
	}
	cm.mu.Unlock()
	cm.loopLifecycleMu.Unlock()

	if cancel != nil {
		cancel()
	}
	if loopDone != nil {
		<-loopDone
	}
	if toolSet != nil {
		toolSet.Cleanup()
	}

	cm.loopLifecycleMu.Lock()
	cm.finishLoopTeardownLocked(teardownGeneration)
	cm.loopLifecycleMu.Unlock()
}

// CancelConversation cancels the active loop and synchronously ends its turn.
// The loop records the complete tool-result batch, including partial output from
// cancelled tools, before this method writes the end-of-turn marker.
func (cm *ConversationManager) CancelConversation(ctx context.Context) error {
	cm.loopLifecycleMu.Lock()
	cm.waitForLoopTeardownLocked()

	var teardownGeneration uint64
	cm.mu.Lock()
	loopInstance := cm.loop
	loopDone := cm.loopDone
	cancel := cm.loopCancel
	toolSet := cm.toolSet
	if loopInstance == nil {
		cm.mu.Unlock()
		cm.loopLifecycleMu.Unlock()
		cm.logger.Info("No active loop to cancel")
		return nil
	}
	cm.loopGeneration++ // stale queues/callbacks cannot target this loop
	teardownGeneration = cm.loopGeneration
	cm.loopTearingDown = true
	cm.loopLifecycleDone = make(chan struct{})
	cm.cancelling = true
	cm.pendingBatches = nil
	cm.loopCancel = nil
	cm.loopCtx = nil
	cm.loopDone = nil
	cm.loop = nil
	cm.modelID = ""
	cm.toolSet = nil
	cm.hydrated = false
	cm.hasConversationEvents = false
	cm.mu.Unlock()
	cm.loopLifecycleMu.Unlock()

	cm.logger.Info("Cancelling conversation")
	persistCtx := context.WithoutCancel(ctx)
	if conv, err := cm.db.GetConversationByID(persistCtx, cm.conversationID); err != nil {
		cm.logger.Error("Failed to read queued messages on cancel", "error", err)
	} else if conv.QueuedMessages != "" && conv.QueuedMessages != "[]" {
		if _, err := cm.db.ClearQueuedMessages(persistCtx, cm.conversationID); err != nil {
			cm.logger.Error("Failed to clear queued messages on cancel", "error", err)
		}
	}

	if cancel != nil {
		cancel()
	}
	if loopDone != nil {
		<-loopDone
	}
	if toolSet != nil {
		toolSet.Cleanup()
	}

	// No replacement can have been installed while loopTearingDown was true.
	// Reacquire the lifecycle lock before publishing the end marker and reopen
	// the boundary only after that marker has committed.
	cm.loopLifecycleMu.Lock()
	defer cm.loopLifecycleMu.Unlock()
	defer cm.finishLoopTeardownLocked(teardownGeneration)

	endTurnMessage := llm.Message{
		Role:      llm.MessageRoleAssistant,
		Content:   []llm.Content{{Type: llm.ContentTypeText, Text: "[Operation cancelled]"}},
		EndOfTurn: true,
	}
	if err := cm.recordMessage(persistCtx, endTurnMessage, llm.Usage{}, nil); err != nil {
		cm.logger.Error("Failed to record end turn message", "error", err)
		return fmt.Errorf("failed to record end turn message: %w", err)
	}

	cm.SetAgentWorking(false)
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

// ModelChangeUserData is the structured data stored in user_data for
// modelchange marker messages recorded when a conversation switches models
// and/or reasoning level. The Reasoning* fields carry user-facing level names
// ("off", "low", ..., or "default" for the service default); they are empty
// when reasoning didn't change.
type ModelChangeUserData struct {
	From          string `json:"from,omitempty"`
	To            string `json:"to,omitempty"`
	ReasoningFrom string `json:"reasoning_from,omitempty"`
	ReasoningTo   string `json:"reasoning_to,omitempty"`
	// FromDisplay/ToDisplay are the human-friendly model names (e.g. "Claude
	// Opus 4.8") the UI shows instead of raw ids. Empty when unknown or when
	// the model didn't change; the UI falls back to From/To.
	FromDisplay string `json:"from_display,omitempty"`
	ToDisplay   string `json:"to_display,omitempty"`
	Text        string `json:"text"`
}

// ModelSettingsChange describes a requested change to a conversation's model
// and/or reasoning level. An empty NewModel leaves the model unchanged;
// ReasoningSet gates the reasoning change (NewReasoning may legitimately be ""
// to mean "use the service default").
type ModelSettingsChange struct {
	OldModel string
	NewModel string // "" = model unchanged
	// OldModelDisplay/NewModelDisplay are optional human-friendly model names
	// (e.g. "Claude Opus 4.8") recorded into the marker for display. Empty is
	// fine; the marker then shows the raw id.
	OldModelDisplay string
	NewModelDisplay string

	ReasoningSet bool   // whether reasoning is being changed
	OldReasoning string // user-facing name ("" means service default)
	NewReasoning string // user-facing name ("" means service default)
}

// GetThinkingLevel returns the conversation's current user-facing reasoning
// level name ("" means the service default).
func (cm *ConversationManager) GetThinkingLevel() string {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.conversationOptions.ThinkingLevel
}

// ApplyModelSettings changes the model and/or reasoning level the conversation
// uses for subsequent turns. It persists the new settings, drops the in-memory
// loop so the next turn rehydrates from the DB with the new model's service and
// thinking level, and records a user-visible modelchange marker so the log
// shows exactly where the change happened. Both the model and the reasoning
// level are baked into the loop at build time, so any change requires a loop
// rebuild.
func (cm *ConversationManager) ApplyModelSettings(ctx context.Context, ch ModelSettingsChange) error {
	// Persist the reasoning level into the conversation options and mirror it
	// in memory. The loop reset below marks the manager unhydrated, so the next
	// turn re-reads options from the DB anyway; the in-memory update keeps state
	// consistent for any reader that runs before rehydration.
	if ch.ReasoningSet {
		cm.mu.Lock()
		opts := cm.conversationOptions
		opts.ThinkingLevel = ch.NewReasoning
		cm.conversationOptions = opts
		cm.mu.Unlock()
		if err := cm.db.UpdateConversationOptions(ctx, cm.conversationID, opts); err != nil {
			return fmt.Errorf("failed to persist reasoning level: %w", err)
		}
	}

	// Persist the new model. ForceUpdateConversationModel overwrites the
	// existing value (unlike UpdateConversationModel, which only sets a NULL
	// model).
	if ch.NewModel != "" {
		if err := cm.db.ForceUpdateConversationModel(ctx, cm.conversationID, ch.NewModel); err != nil {
			return fmt.Errorf("failed to persist model switch: %w", err)
		}
	}

	// Drop the loop pinned to the old settings so the next user message rebuilds
	// it via ensureLoop. When a turn is active we must go through
	// CancelConversation, not a bare ResetLoop: cancelling records the
	// end-of-turn marker and clears the (persisted) agent_working flag, so the
	// thinking indicator doesn't get stuck on. ResetLoop alone would leave
	// agent_working=true until the next completed turn.
	if cm.IsAgentWorking() {
		if err := cm.CancelConversation(ctx); err != nil {
			return fmt.Errorf("failed to cancel active turn before model change: %w", err)
		}
		// CancelConversation early-returns without clearing the flag when there
		// is no in-memory loop (e.g. a hydrated manager with a stale persisted
		// agent_working=true). Clear it defensively so the change never leaves
		// the thinking indicator stuck on.
		if cm.IsAgentWorking() {
			cm.SetAgentWorking(false)
		}
	} else {
		cm.ResetLoop()
	}
	return cm.recordModelChangeMarker(ctx, buildModelChangeUserData(ch))
}

// buildModelChangeUserData assembles the marker payload (structured fields plus
// a human-readable one-line summary) for an applied model/reasoning change.
func buildModelChangeUserData(ch ModelSettingsChange) ModelChangeUserData {
	ud := ModelChangeUserData{
		From:        ch.OldModel,
		To:          ch.NewModel,
		FromDisplay: ch.OldModelDisplay,
		ToDisplay:   ch.NewModelDisplay,
	}

	// Prefer the human-friendly name in the summary sentence, falling back to
	// the raw id when no display name is known.
	oldName := ch.OldModel
	if ch.OldModelDisplay != "" {
		oldName = ch.OldModelDisplay
	}
	newName := ch.NewModel
	if ch.NewModelDisplay != "" {
		newName = ch.NewModelDisplay
	}

	var parts []string
	if ch.NewModel != "" {
		if ch.OldModel == "" {
			parts = append(parts, fmt.Sprintf("Model set to %s", newName))
		} else {
			parts = append(parts, fmt.Sprintf("model changed from %s to %s", oldName, newName))
		}
	}
	if ch.ReasoningSet {
		ud.ReasoningFrom = reasoningDisplayName(ch.OldReasoning)
		ud.ReasoningTo = reasoningDisplayName(ch.NewReasoning)
		parts = append(parts, fmt.Sprintf("reasoning changed from %s to %s", ud.ReasoningFrom, ud.ReasoningTo))
	}

	summary := strings.Join(parts, "; ")
	if summary != "" {
		// Capitalize the first letter for a clean sentence when the model part
		// (which is already capitalized) is absent.
		summary = strings.ToUpper(summary[:1]) + summary[1:] + "."
	}
	ud.Text = summary
	return ud
}

// reasoningDisplayName maps a stored reasoning level to a user-facing name,
// rendering the empty (service-default) value as "default".
func reasoningDisplayName(level string) string {
	if level == "" {
		return "default"
	}
	return level
}

// Cwd returns the conversation's current working directory.
func (cm *ConversationManager) Cwd() string {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.cwd
}

// errAgentWorking is returned by SetCwd when a turn is in flight. Moving the
// working directory then would change the ground under a running bash command.
var errAgentWorking = errors.New("agent is working")

// SetCwd moves the conversation to dir: the directory the agent's tools run in
// and the one the status bar shows. It is the user-driven counterpart of the
// agent's own change_dir tool, and has to reach the same three places that tool
// does, or the user and the agent end up disagreeing about where they are:
//
//  1. the conversation row, which the UI reads and which a future loop is
//     hydrated from;
//  2. the live toolset's working directory, so the very next bash command lands
//     in the new directory rather than the one baked in at loop build time
//     (this is the part a plain DB write would miss);
//  3. the message log, as an in-context user message — the agent has the old
//     directory in its history (the system prompt's "Initial pwd", and every
//     tool result so far) and will keep resolving relative paths against it
//     unless it is told. A marker excluded from context would move the tools
//     out from under an agent that still believes it is elsewhere.
//
// Returns errAgentWorking if a turn is in flight. Callers check this too, for a
// better error message, but the authoritative check is the one here: it shares
// a critical section with the move, so a turn that starts between the caller's
// check and this one cannot slip through.
func (cm *ConversationManager) SetCwd(ctx context.Context, dir string) error {
	cm.cwdMu.Lock()
	defer cm.cwdMu.Unlock()

	cm.mu.Lock()
	if cm.agentWorking {
		cm.mu.Unlock()
		return errAgentWorking
	}
	old := cm.cwd
	toolSet := cm.toolSet
	if old == dir {
		cm.mu.Unlock()
		return nil
	}
	// Move the in-memory cwd inside the same critical section as the check, so
	// a turn starting concurrently either sees the new directory or is refused.
	// The DB write below can fail, in which case we roll this back.
	cm.cwd = dir
	cm.mu.Unlock()

	if err := cm.db.UpdateConversationCwd(ctx, cm.conversationID, dir); err != nil {
		cm.mu.Lock()
		cm.cwd = old
		cm.mu.Unlock()
		return fmt.Errorf("failed to persist working directory: %w", err)
	}

	// The toolset only exists once a loop has been built. Until then there is
	// nothing to update: ensureLoop reads cm.cwd when it builds one, and
	// cwdMu keeps a concurrent SetCwd from interleaving with this one.
	if toolSet != nil {
		toolSet.WorkingDir().Set(dir)
	}

	// The move has happened by this point: the row and the tools are both in the
	// new directory, and neither can be taken back (the tools may already have
	// run something there). A failed notice therefore must not be reported as a
	// failed move — answering 500 here would invite a retry of a change that has
	// already applied. Log it instead: the cost is an agent that has to work the
	// new directory out from its next tool result, not a wrong directory.
	if err := cm.recordCwdChangeNotice(ctx, old, dir); err != nil {
		cm.logger.Error("working directory moved, but the agent was not told",
			"conversationID", cm.conversationID, "from", old, "to", dir, "error", err)
	}
	return nil
}

// recordCwdChangeNotice tells the agent, in context, that the user moved the
// conversation. User-role because it is the user's action and the agent has to
// act on it; consecutive user messages are already ordinary here (queued turns
// and compaction summaries both produce them).
func (cm *ConversationManager) recordCwdChangeNotice(ctx context.Context, from, to string) error {
	text := fmt.Sprintf("[The user changed the working directory to %s. Use it for subsequent commands and relative paths.]", to)
	if from != "" {
		text = fmt.Sprintf("[The user changed the working directory from %s to %s. Use the new one for subsequent commands and relative paths.]", from, to)
	}
	message := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: text}},
	}
	createdMsg, err := cm.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: cm.conversationID,
		Type:           db.MessageTypeUser,
		LLMData:        message,
		UserData:       map[string]any{"cwd_change": true, "from": from, "to": to},
		UsageData:      llm.Usage{},
	})
	if err != nil {
		return fmt.Errorf("failed to record working directory change: %w", err)
	}
	cm.Touch()

	// Persisting is not enough. A live loop holds its own in-memory history and
	// only rebuilds it from the DB when it is constructed; between turns it
	// survives, so a row written here would stay invisible to the model until
	// something dropped the loop (a model switch, a compaction, eviction). Splice
	// it in directly — the message must be seen by the NEXT request without
	// provoking a turn of its own, which rules out QueueUserMessage.
	//
	// Safe against a concurrent turn because the only caller, SetCwd, holds
	// cwdMu and has already established the agent is idle under cm.mu.
	cm.mu.Lock()
	liveLoop := cm.loop
	cm.mu.Unlock()
	if liveLoop != nil {
		liveLoop.AppendHistory(message)
	}

	var conversation generated.Conversation
	err = cm.db.Queries(ctx, func(q *generated.Queries) error {
		var qerr error
		conversation, qerr = q.GetConversation(ctx, cm.conversationID)
		return qerr
	})
	if err != nil {
		return fmt.Errorf("failed to get conversation for cwd change notification: %w", err)
	}
	cm.publishStream(createdMsg.SequenceID, StreamResponse{
		Messages:     toAPIMessages([]generated.Message{*createdMsg}),
		Conversation: &conversation,
	})
	return nil
}

// recordModelCommandInfo records an informational modelchange marker (bare
// /model output, already-using notice, or an error) that does not switch the
// model. From and To are left empty.
func (cm *ConversationManager) recordModelCommandInfo(ctx context.Context, text string) error {
	return cm.recordModelChangeMarker(ctx, ModelChangeUserData{Text: text})
}

// recordModelChangeMarker persists and broadcasts a modelchange marker.
func (cm *ConversationManager) recordModelChangeMarker(ctx context.Context, userData ModelChangeUserData) error {
	message := llm.Message{
		Role:    llm.MessageRoleAssistant,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: userData.Text}},
	}

	createdMsg, err := cm.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID:      cm.conversationID,
		Type:                db.MessageTypeModelChange,
		LLMData:             message,
		UserData:            userData,
		UsageData:           llm.Usage{},
		ExcludedFromContext: true,
	})
	if err != nil {
		return fmt.Errorf("failed to record model change: %w", err)
	}
	cm.Touch()

	var conversation generated.Conversation
	err = cm.db.Queries(ctx, func(q *generated.Queries) error {
		var qerr error
		conversation, qerr = q.GetConversation(ctx, cm.conversationID)
		return qerr
	})
	if err != nil {
		return fmt.Errorf("failed to get conversation for model change notification: %w", err)
	}
	cm.publishStream(createdMsg.SequenceID, StreamResponse{
		Messages:     toAPIMessages([]generated.Message{*createdMsg}),
		Conversation: &conversation,
	})
	return nil
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
		Conversation: &conversation,
	}
	cm.publishStream(msg.SequenceID, streamData)
}
