package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
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
	"shelley.exe.dev/skills"
	"shelley.exe.dev/slug"
	"shelley.exe.dev/subpub"
)

var (
	errConversationModelMismatch = errors.New("conversation model mismatch")
	errQueuedMessagesPending     = errors.New("queued messages pending")
)

// conversationRole is what a conversation is for, as recorded on its row.
type conversationRole int

const (
	roleTopLevel conversationRole = iota
	// roleSubagent is a child delegated by the subagent tool.
	roleSubagent
	// roleBtwReader is a /btw side discussion of its parent.
	roleBtwReader
	// roleWorker is an internal child such as a commit tour.
	roleWorker
)

func conversationRoleOf(conv generated.Conversation) conversationRole {
	switch {
	case !isManagedChild(conv):
		return roleTopLevel
	case isBtwReader(conv):
		return roleBtwReader
	case db.ParseConversationOptions(conv.ConversationOptions).Kind != "":
		return roleWorker
	default:
		return roleSubagent
	}
}

// ConversationManager manages a single active conversation
type ConversationManager struct {
	conversationID      string
	conversationOptions db.ConversationOptions
	role                conversationRole
	decorateService     func(llm.Service) (llm.Service, error)
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
	// recordTurnStartMessage records the user message that begins a turn,
	// folding the agent_working=true flip and timestamp bump into the INSERT Tx
	// (see Server.recordTurnStartMessage). The turn must not run unless this
	// succeeds: otherwise the model sees a user message the transcript never
	// records.
	recordTurnStartMessage turnStartRecordFunc
	logger                 *slog.Logger
	toolSetConfig          claudetool.ToolSetConfig
	integrationSkills      *integrationSkillCache
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
	serverSocket          string // Unix socket the shelley server listens on, for SHELLEY_SOCKET
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

	// draining is true while a drainPendingMessages owner is in flight. A
	// concurrent caller records drainRequested and returns the owner's drainDone
	// token; the owner makes another pass before publishing completion. This
	// prevents a ready transcription or turn-end wakeup from being lost while an
	// older drainer is returning after observing a blocked queue.
	draining       bool
	drainRequested bool
	drainDone      chan struct{}

	// modelSettingsMu serializes model/reasoning changes with manual resume.
	// Both operations rebuild the loop, and model changes persist before that
	// rebuild, so sharing this lock prevents resume from selecting the old model
	// while a concurrent /model request is between those two steps.
	modelSettingsMu sync.Mutex
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

	// onTurnStartRejected restarts queue draining after a failed turn-start
	// write rolls the manager back to idle.
	onTurnStartRejected func()
	// recordDrainedQueued records a queued item's messages and removes the item
	// from the queue in one transaction.
	recordDrainedQueued func(ctx context.Context, qm db.QueuedMessage, messages []llm.Message) (int64, error)
	// userChat, set for a top-level conversation bound to an external chat,
	// is where message_user's messages also go.
	userChat claudetool.UserChat

	// idleWaiters are closed at the next working→idle transition; see idle.
	idleWaiters []chan struct{}

	// cancelling is true while CancelConversation is tearing down the current
	// turn; batches arriving meanwhile are dropped and nothing is injected.
	// Guarded by cm.mu.
	cancelling bool

	// compactedGeneration is the loop generation whose turn recorded an
	// in-place compaction (compact_in_place) that its history does not yet
	// reflect; takeInjectable swaps in the rebuilt history. Guarded by cm.mu.
	compactedGeneration uint64
	// keepRecentTokens is the budget of the recent part compact_in_place
	// leaves as it is.
	keepRecentTokens int
}

// NewConversationManager constructs a manager with dependencies but defers hydration until needed.
type turnStartRecordFunc func(context.Context, llm.Message, llm.Usage, []llm.PurposedUsage) (*generated.Message, error)

func NewConversationManager(conversationID string, database *db.DB, baseLogger *slog.Logger, toolSetConfig claudetool.ToolSetConfig, integrationSkills *integrationSkillCache, recordMessage loop.MessageRecordFunc, recordTurnStartMessage turnStartRecordFunc, onStateChange func(ConversationState), streamPub *subpub.SubPub[StreamResponse]) *ConversationManager {
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
		logger:                 logger,
		toolSetConfig:          toolSetConfig,
		integrationSkills:      integrationSkills,
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

func (cm *ConversationManager) broadcastStreamDeltas(deltas []llm.StreamDelta) {
	if len(deltas) == 0 {
		return
	}
	for i := range deltas {
		cm.subpub.Broadcast(StreamResponse{
			ConversationID: cm.conversationID,
			StreamDelta:    &deltas[i],
		})
	}
	if cm.streamPub != nil {
		cm.streamPub.Broadcast(StreamResponse{
			ConversationID: cm.conversationID,
			streamDeltas:   deltas,
		})
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
	convID := cm.conversationID
	modelID := cm.modelID
	var idleWaiters []chan struct{}
	if !working {
		idleWaiters, cm.idleWaiters = cm.idleWaiters, nil
	}
	cm.mu.Unlock()

	for _, ch := range idleWaiters {
		close(ch)
	}
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
}

// idle returns a channel that is closed once the agent is not working:
// immediately if it is idle now, otherwise at the next working→idle
// transition (completion or cancellation).
func (cm *ConversationManager) idle() <-chan struct{} {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	ch := make(chan struct{})
	if !cm.agentWorking {
		close(ch)
		return ch
	}
	cm.idleWaiters = append(cm.idleWaiters, ch)
	return ch
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
		if cm.role == roleBtwReader {
			systemMsg, err = cm.recreateBtwReaderSystemPrompt(ctx)
		} else if cm.role != roleTopLevel {
			systemMsg, err = cm.createSubagentSystemPrompt(ctx)
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
	first, _, err := cm.acceptUserMessage(ctx, service, modelID, message)
	return first, err
}

// AcceptUserMessageWithID returns the exact turn-start row ID while preserving
// AcceptUserMessage's behavior for ordinary callers.
func (cm *ConversationManager) AcceptUserMessageWithID(ctx context.Context, service llm.Service, modelID string, message llm.Message) (bool, string, error) {
	return cm.acceptUserMessage(ctx, service, modelID, message)
}

func (cm *ConversationManager) acceptUserMessage(ctx context.Context, service llm.Service, modelID string, message llm.Message) (bool, string, error) {
	if service == nil {
		return false, "", fmt.Errorf("llm service is required")
	}

	cm.loopLifecycleMu.Lock()
	defer cm.loopLifecycleMu.Unlock()
	cm.waitForLoopTeardownLocked()

	if err := cm.Hydrate(ctx); err != nil {
		return false, "", err
	}

	if cm.role == roleTopLevel {
		queued, err := cm.HasQueuedMessages(ctx)
		if err != nil {
			return false, "", err
		}
		if queued {
			return false, "", errQueuedMessagesPending
		}
	}

	cm.mu.Lock()
	hadLoop := cm.loop != nil
	cm.mu.Unlock()
	if _, err := cm.recordInterruptedToolResults(ctx); err != nil {
		return false, "", fmt.Errorf("failed to record interrupted tool results: %w", err)
	}
	if err := cm.ensureLoopLocked(service, modelID); err != nil {
		return false, "", err
	}

	cm.mu.Lock()
	isFirst := !cm.hasConversationEvents
	wasWorking := cm.agentWorking
	loopInstance := cm.loop
	recordTurnStart := cm.recordTurnStartMessage
	cm.mu.Unlock()

	if loopInstance == nil {
		return false, "", fmt.Errorf("conversation loop not initialized")
	}
	if recordTurnStart == nil {
		if !hadLoop {
			cm.discardUnstartedLoopLocked(loopInstance)
		}
		return false, "", fmt.Errorf("turn-start recorder not configured")
	}

	modelMessage, err := messageWithContextSenderProvenance(ctx, message)
	if err != nil {
		if !hadLoop {
			cm.discardUnstartedLoopLocked(loopInstance)
		}
		return false, "", fmt.Errorf("prepare user message provenance: %w", err)
	}

	// Reserve the working state before the fallible write. Other request paths
	// use it to decide whether they can start immediately, so leaving it false
	// here lets a concurrent user/subagent send overtake this turn in the DB.
	cm.syncAgentWorking(true)

	// The loop must never receive a user message that was not committed to the
	// transcript. In particular, an HTTP disconnect can cancel this insert while
	// the conversation loop's independent context remains alive. Treat that as
	// a rejected send instead of running an invisible turn from memory.
	created, err := recordTurnStart(ctx, message, llm.Usage{}, nil)
	if err != nil {
		cm.rejectTurnStart(wasWorking && hadLoop)
		if !hadLoop {
			cm.discardUnstartedLoopLocked(loopInstance)
		}
		return false, "", fmt.Errorf("record user message: %w", err)
	}
	if created == nil {
		cm.rejectTurnStart(wasWorking && hadLoop)
		if !hadLoop {
			cm.discardUnstartedLoopLocked(loopInstance)
		}
		return false, "", fmt.Errorf("turn-start recorder returned no message")
	}
	if turnUserDataFromContext(ctx) == nil && cm.showsSequenceIDs() && typedUserForLLM(message, nil) {
		modelMessage, err = messageWithSequenceID(modelMessage, derefString(created.ExternalMessageID), created.SequenceID)
		if err != nil {
			return false, created.MessageID, fmt.Errorf("render user message: %w", err)
		}
	}

	cm.mu.Lock()
	cm.hasConversationEvents = true
	cm.lastActivity = time.Now()
	cm.mu.Unlock()
	loopInstance.QueueUserMessage(modelMessage)

	return isFirst, created.MessageID, nil
}

// rejectTurnStart restores an idle manager after a turn-start write fails.
// SetAgentWorking(false) repairs the persisted bit too, covering a recorder
// that failed after an ambiguous commit.
func (cm *ConversationManager) rejectTurnStart(keepWorking bool) {
	if keepWorking {
		return
	}
	cm.SetAgentWorking(false)
	cm.mu.Lock()
	needsDrain := !cm.distilling
	onRejected := cm.onTurnStartRejected
	cm.mu.Unlock()
	if needsDrain && onRejected != nil {
		onRejected()
	}
}

// discardUnstartedLoopLocked removes a loop created for a turn whose user row
// could not be persisted. Leaving it installed makes CancelConversation treat
// the rejected turn as active and append a bogus cancellation marker. The
// caller holds loopLifecycleMu; this returns with it held again.
func (cm *ConversationManager) discardUnstartedLoopLocked(expected *loop.Loop) {
	cm.mu.Lock()
	if cm.loop != expected {
		cm.mu.Unlock()
		return
	}
	modelID := cm.modelID
	detached := cm.detachLoopLocked()
	cm.modelID = modelID // the conversation's model outlives a rejected turn
	cm.mu.Unlock()

	cm.loopLifecycleMu.Unlock()
	detached.stop()
	cm.loopLifecycleMu.Lock()
	cm.finishLoopTeardownLocked(detached.generation)
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

// ContinueAfterRefusal handles the choose-another-model affordance a refusal
// error offers in the UI. A refusal is deliberately non-retryable
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

// ResumeInterruptedTurn re-fires the persisted request for a newly created BTW
// reader. The caller has just chosen and stored its model, so there is no
// pre-existing model-setting request to reconcile.
func (cm *ConversationManager) ResumeInterruptedTurn(ctx context.Context, service llm.Service, modelID string) error {
	if service == nil {
		return fmt.Errorf("llm service is required")
	}
	return cm.resumeInterruptedTurn(ctx, interruptedResumeTrusted, nil, func() (llm.Service, string, error) {
		return service, modelID, nil
	}, nil)
}

var errInterruptedTurnNotApplicable = errors.New("conversation has no interrupted turn to resume")

type interruptedResumeValidation uint8

const (
	interruptedResumeTrusted interruptedResumeValidation = iota
	interruptedResumeWorking
	interruptedResumeMarked
)

func (cm *ConversationManager) currentModelResolver(defaultModelID string, serviceForModel func(string) (llm.Service, error)) func() (llm.Service, string, error) {
	return func() (llm.Service, string, error) {
		modelID := cm.GetModel()
		if modelID == "" {
			modelID = defaultModelID
		}
		service, err := serviceForModel(modelID)
		return service, modelID, err
	}
}

// ResumeInterruptedTurnAfterUpgrade claims the durable startup version under
// model/lifecycle serialization. A fresh user turn changes that version before
// this worker can claim it; a concurrent model switch either happens afterward
// and cancels this turn, or happens first and makes the claim a no-op. The
// warning is recorded immediately before Retry.
func (cm *ConversationManager) ResumeInterruptedTurnAfterUpgrade(ctx context.Context, resume db.UpgradeResume, defaultModelID string, serviceForModel func(string) (llm.Service, error), warning string) error {
	if resume.ConversationID != cm.conversationID {
		return fmt.Errorf("upgrade resume token is for conversation %s, not %s", resume.ConversationID, cm.conversationID)
	}
	if serviceForModel == nil {
		return fmt.Errorf("llm service resolver is required")
	}
	return cm.resumeInterruptedTurn(ctx, interruptedResumeWorking, &resume, cm.currentModelResolver(defaultModelID, serviceForModel), func() error {
		return cm.recordWarning(ctx, warning)
	})
}

// ContinueInterruptedTurn is the user-triggered counterpart. It validates
// under the loop lifecycle lock that the conversation is idle, top-level, and
// still carries an unresolved startup interruption bit, making repeated clicks
// and races with a normal send harmless. Model selection also happens under
// modelSettingsMu, after Hydrate, so a concurrent /model request either finishes
// first or cancels this resumed turn afterward; it cannot leave the DB and loop
// on different models.
func (cm *ConversationManager) ContinueInterruptedTurn(ctx context.Context, defaultModelID string, serviceForModel func(string) (llm.Service, error)) error {
	if serviceForModel == nil {
		return fmt.Errorf("llm service resolver is required")
	}
	return cm.resumeInterruptedTurn(ctx, interruptedResumeMarked, nil, cm.currentModelResolver(defaultModelID, serviceForModel), nil)
}

// resumeInterruptedTurn persists results for orphaned tool calls before loading
// the request. retryMu serializes it against retry/continue affordances.
func (cm *ConversationManager) resumeInterruptedTurn(ctx context.Context, validation interruptedResumeValidation, upgradeResume *db.UpgradeResume, resolveService func() (llm.Service, string, error), beforeResume func() error) (returnErr error) {
	if validation == interruptedResumeWorking && upgradeResume == nil {
		return fmt.Errorf("upgrade resume token is required")
	}
	cm.retryMu.Lock()
	defer cm.retryMu.Unlock()
	cm.modelSettingsMu.Lock()
	defer cm.modelSettingsMu.Unlock()
	cm.loopLifecycleMu.Lock()
	defer cm.loopLifecycleMu.Unlock()
	cm.waitForLoopTeardownLocked()
	if validation == interruptedResumeWorking {
		defer func() {
			if returnErr == nil || errors.Is(returnErr, errInterruptedTurnNotApplicable) {
				return
			}
			if err := cm.recoverFailedUpgradeResume(ctx, *upgradeResume); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("failed to preserve interrupted turn for manual recovery: %w", err))
			}
		}()
	}

	var resumedErrorMessageID string
	switch validation {
	case interruptedResumeWorking:
		latest, err := cm.db.GetLatestActionableMessage(ctx, cm.conversationID)
		if err != nil {
			return fmt.Errorf("failed to load latest message before upgrade resume: %w", err)
		}
		if latest.Type == string(db.MessageTypeError) {
			cm.mu.Lock()
			alreadyRetried := cm.lastRetriedErrorMessageID == latest.MessageID
			cm.mu.Unlock()
			if alreadyRetried {
				return errInterruptedTurnNotApplicable
			}
			resumedErrorMessageID = latest.MessageID
		}
		claimed, err := cm.db.ClaimUpgradeInterruptedTurn(ctx, *upgradeResume)
		if err != nil {
			return fmt.Errorf("failed to claim upgrade-interrupted turn: %w", err)
		}
		if !claimed {
			if err := cm.syncPersistedAgentWorking(ctx); err != nil {
				return fmt.Errorf("failed to synchronize rejected upgrade resume: %w", err)
			}
			return errInterruptedTurnNotApplicable
		}
	case interruptedResumeMarked:
		conversation, err := cm.db.GetConversationByID(ctx, cm.conversationID)
		if err != nil {
			return fmt.Errorf("failed to load conversation before resuming: %w", err)
		}
		if conversation.AgentWorking || conversation.ParentConversationID != nil || !conversation.TurnInterrupted {
			return errInterruptedTurnNotApplicable
		}
		latest, err := cm.db.GetLatestActionableMessage(ctx, cm.conversationID)
		if err != nil {
			return fmt.Errorf("failed to load latest message before resuming: %w", err)
		}
		if latest.Type == string(db.MessageTypeError) {
			resumedErrorMessageID = latest.MessageID
		}
	}

	if err := cm.Hydrate(ctx); err != nil {
		return fmt.Errorf("failed to hydrate before resuming: %w", err)
	}
	service, modelID, err := resolveService()
	if err != nil {
		return fmt.Errorf("failed to resolve model %q before resuming: %w", modelID, err)
	}
	if service == nil {
		return fmt.Errorf("llm service is required")
	}
	if validation != interruptedResumeTrusted {
		result, err := cm.recordInterruptedToolResults(ctx)
		if err != nil {
			return fmt.Errorf("failed to record interrupted tool results: %w", err)
		}
		if validation == interruptedResumeWorking && result != nil {
			// Our result advances the turn version; later user input still
			// invalidates recovery because it gets a different sequence.
			upgradeResume.MaxUserSequenceID = result.SequenceID
		}
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

	if validation == interruptedResumeWorking {
		finished, err := cm.db.FinishUpgradeInterruptedTurn(ctx, cm.conversationID)
		if err != nil {
			return fmt.Errorf("failed to finish upgrade-interrupted turn claim: %w", err)
		}
		if !finished {
			if err := cm.syncPersistedAgentWorking(ctx); err != nil {
				return fmt.Errorf("failed to synchronize lost upgrade resume claim: %w", err)
			}
			return errInterruptedTurnNotApplicable
		}
	}
	if beforeResume != nil {
		if err := beforeResume(); err != nil {
			return fmt.Errorf("failed to prepare interrupted turn resume: %w", err)
		}
	}
	if validation == interruptedResumeMarked {
		claimed, err := cm.db.ClaimInterruptedTurn(ctx, cm.conversationID)
		if err != nil {
			return fmt.Errorf("failed to claim interrupted turn: %w", err)
		}
		if !claimed {
			return errInterruptedTurnNotApplicable
		}
		cm.syncAgentWorking(true)
	} else if validation == interruptedResumeTrusted {
		cm.SetAgentWorking(true)
	}
	if resumedErrorMessageID != "" {
		cm.mu.Lock()
		cm.lastRetriedErrorMessageID = resumedErrorMessageID
		cm.mu.Unlock()
	}

	logger.Info("resuming interrupted turn", "model", modelID)
	loopInstance.Retry()
	return nil
}

func (cm *ConversationManager) recordInterruptedToolResults(ctx context.Context) (*generated.Message, error) {
	result, err := cm.db.RecordInterruptedToolResults(ctx, cm.conversationID)
	if err != nil {
		return nil, err
	}
	if result != nil {
		cm.publishStream(result.SequenceID, StreamResponse{
			Messages: toAPIMessages([]generated.Message{*result}),
		})
	}
	return result, nil
}

// HasQueuedMessages reports whether durable queued user work already reserves
// a position ahead of a new immediate send.
func (cm *ConversationManager) HasQueuedMessages(ctx context.Context) (bool, error) {
	queued, err := cm.db.GetQueuedMessages(ctx, cm.conversationID)
	return len(queued) > 0, err
}

// QueueTranscription persists a transcription item. Unlike QueueMessage it
// never drains immediately: until the item is ready it blocks every later
// queued message, and only its ready transition (see drainQueueIfIdle) makes
// it deliverable.
func (cm *ConversationManager) QueueTranscription(ctx context.Context, s *Server, qm db.QueuedMessage) (db.QueuedMessage, error) {
	cm.waitDistillingSetup()
	cm.loopLifecycleMu.Lock()
	defer cm.loopLifecycleMu.Unlock()
	cm.waitForLoopTeardownLocked()

	_, queued, err := s.db.CreateQueuedTranscription(ctx, cm.conversationID, qm)
	if err != nil {
		return db.QueuedMessage{}, err
	}
	cm.Touch()
	go s.notifySubscribers(context.WithoutCancel(ctx), cm.conversationID)
	return queued, nil
}

// drainQueueIfIdle starts the queue drainer unless a running turn or
// distillation will start it when it ends.
func (cm *ConversationManager) drainQueueIfIdle(s *Server) {
	cm.mu.Lock()
	needsDrain := !cm.agentWorking && !cm.distilling
	cm.mu.Unlock()
	if needsDrain {
		go cm.drainPendingMessages(s)
	}
}

// syncPersistedAgentWorking repairs a manager whose startup hydration raced a
// cancellation or fresh turn before its automatic-resume claim. The transition
// is bookkeeping, not completion, so suppress subagent done callbacks.
func (cm *ConversationManager) syncPersistedAgentWorking(ctx context.Context) error {
	conversation, err := cm.db.GetConversationByID(ctx, cm.conversationID)
	if err != nil {
		return err
	}
	cm.mu.Lock()
	wasCancelling := cm.cancelling
	cm.cancelling = true
	cm.mu.Unlock()
	cm.syncAgentWorking(conversation.AgentWorking)
	cm.mu.Lock()
	cm.cancelling = wasCancelling
	cm.mu.Unlock()
	return nil
}

// recoverFailedUpgradeResume runs under retryMu, modelSettingsMu, and
// loopLifecycleMu. It converts the stale working row to the same durable manual
// recovery state as an ordinary restart and removes any loop built but not
// started before setup failed.
func (cm *ConversationManager) recoverFailedUpgradeResume(ctx context.Context, resume db.UpgradeResume) error {
	marked, err := cm.db.MarkUpgradeResumeInterrupted(ctx, resume)
	if err != nil {
		return err
	}
	if !marked {
		return cm.syncPersistedAgentWorking(ctx)
	}

	cm.mu.Lock()
	wasCancelling := cm.cancelling
	cm.cancelling = true
	loopInstance := cm.loop
	cm.mu.Unlock()
	cm.syncAgentWorking(false)
	cm.mu.Lock()
	cm.cancelling = wasCancelling
	cm.mu.Unlock()

	if loopInstance != nil {
		cm.discardUnstartedLoopLocked(loopInstance)
	}
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
	return cm.queueMessage(ctx, s, modelID, message, false)
}

// InjectMessage queues message like QueueMessage, but a running turn takes
// it at its next LLM round instead of after the turn ends.
func (cm *ConversationManager) InjectMessage(ctx context.Context, s *Server, modelID string, message llm.Message) error {
	return cm.queueMessage(ctx, s, modelID, message, true)
}

func (cm *ConversationManager) queueMessage(ctx context.Context, s *Server, modelID string, message llm.Message, midTurn bool) error {
	cm.waitDistillingSetup()

	cm.loopLifecycleMu.Lock()
	defer cm.loopLifecycleMu.Unlock()
	cm.waitForLoopTeardownLocked()
	// A subagent report must not slip between Stop's initial check and its
	// queue clear. Stop raises this fence before taking the same lifecycle
	// lock; whichever obtains it first determines whether the report lands
	// before Stop (and gets cleared) or is rejected afterward.
	if source, ok := turnUserDataFromContext(ctx).(senderMessageUserData); ok && source.SenderRelationship == senderRelationshipSubagent {
		s.mu.Lock()
		stopped := s.stoppedParents[cm.conversationID]
		s.mu.Unlock()
		if stopped {
			return fmt.Errorf("parent conversation %s was stopped", cm.conversationID)
		}
	}

	llmJSON, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal queued message: %w", err)
	}
	userData, err := marshalTurnUserData(ctx)
	if err != nil {
		return fmt.Errorf("failed to marshal queued user data: %w", err)
	}
	qm := db.QueuedMessage{
		ID:                uuid.New().String(),
		Llm:               llmJSON,
		CreatedAt:         time.Now().UTC(),
		Model:             modelID,
		UserEmail:         userEmailFromContext(ctx),
		UserData:          userData,
		Inject:            midTurn,
		ExternalMessageID: externalMessageIDFromContext(ctx),
	}
	if _, err := s.db.AppendQueuedMessage(ctx, cm.conversationID, qm); err != nil {
		return fmt.Errorf("failed to append queued message: %w", err)
	}
	if turnUserDataFromContext(ctx) == nil {
		s.resumeParentCompletions(cm.conversationID)
	}
	cm.Touch()

	// Broadcast the updated conversation (with the new queued_messages array)
	// to per-conversation subscribers. The list-patch stream is refreshed
	// automatically by the Pool.OnCommit hook fired by the append's Tx.
	go s.notifySubscribers(context.WithoutCancel(ctx), cm.conversationID)

	cm.logger.Info("Queued user message", "queued_id", qm.ID, "midTurn", midTurn)
	cm.drainQueueIfIdle(s)
	return nil
}

// takeInjectable records the queued messages a running turn accepts — those
// queued by InjectMessage — and returns them, in queue order, for mid-turn
// splicing into the running loop (see loop.Config.InjectMessages). Returning
// nil leaves the turn untouched.
//
// Recording BEFORE returning keeps DB sequence order identical to the
// in-memory splice point: the messages land between the tool round that just
// finished and the assistant response that reacts to it. A message whose
// record fails stays queued for the turn-end drain; one the user removed from
// the queue meanwhile is skipped.
//
// While distilling or cancelling, injection is skipped entirely (the
// conversation is being rewritten / the user is taking over); distillation
// leaves the messages queued for the post-distillation drain, cancellation
// clears them. The distilling/cancelling check and the record below are not
// atomic — a distillation or cancel can start in between — but that
// check-then-act window is the same one drainPendingMessages already has.
// Consequences: on cancel, a validly-paired notification lands moments after
// the cutoff; on distillation, the messages may be recorded into the OLD
// generation after the compaction snapshot was taken and thus be absent from
// the new generation's context (visible in the transcript, not re-fed) — an
// accepted, pre-existing loss mode of the drain path too.
func (cm *ConversationManager) takeInjectable(ctx context.Context, generation uint64, nudger *contextNudger) (loop.Injection, error) {
	// This callback runs inside a loop goroutine. Never wait for an in-progress
	// teardown here: cancellation is waiting for this goroutine to exit. Taking
	// the lifecycle lock only long enough to validate/record makes the winner
	// deterministic—either injection commits before cancellation begins, or the
	// stale loop injects nothing.
	cm.loopLifecycleMu.Lock()
	defer cm.loopLifecycleMu.Unlock()

	cm.mu.Lock()
	stale := cm.loopTearingDown || cm.loop == nil || cm.loopGeneration != generation ||
		cm.distilling || cm.cancelling
	compacted := !stale && cm.compactedGeneration == generation
	if compacted {
		cm.compactedGeneration = 0
	}
	cm.mu.Unlock()
	if stale {
		return loop.Injection{}, nil
	}

	// WithoutCancel: ctx is the loop's context; a concurrent cancellation
	// must not abort an insert halfway and silently eat the message. The
	// recorded rows remain valid history either way — hydration picks them
	// up even if the turn dies before the next LLM round sends them.
	ctx = context.WithoutCancel(ctx)
	var inj loop.Injection
	if compacted {
		// compact_in_place recorded a compaction during this turn. The record
		// and every row so far are persisted, so rebuild from the DB.
		var rows []generated.Message
		if err := cm.db.Queries(ctx, func(q *generated.Queries) error {
			var err error
			rows, err = q.ListMessagesForContext(ctx, cm.conversationID)
			return err
		}); err != nil {
			return inj, fmt.Errorf("reload compacted history: %w", err)
		}
		history, _, err := cm.partitionMessages(rows)
		if err != nil {
			return inj, fmt.Errorf("rebuild compacted history: %w", err)
		}
		inj.History = history
	}
	queued, err := cm.db.GetQueuedMessages(ctx, cm.conversationID)
	if err != nil {
		cm.logger.Error("Failed to read queued messages for injection", "error", err)
		queued = nil
	}
	for _, qm := range queued {
		if !qm.Inject {
			continue
		}
		_, fed, err := cm.recordQueued(ctx, qm)
		switch {
		case errors.Is(err, db.ErrQueuedMessageNotFound):
			cm.logger.Info("Skipping cancelled queued message", "queued_id", qm.ID)
		case err != nil:
			cm.logger.Error("Failed to record injected user message; leaving it queued", "queued_id", qm.ID, "error", err)
		default:
			cm.logger.Info("Injected user message mid-turn", "queued_id", qm.ID)
			inj.Messages = append(inj.Messages, fed)
		}
	}
	// Right after a compaction the nudger only knows the size from before
	// it; the next response reports the new one.
	if nudger != nil && !compacted {
		if size, ok := nudger.take(); ok {
			nudge, err := cm.recordContextNudge(ctx, size)
			if err != nil {
				return inj, err
			}
			inj.Messages = append(inj.Messages, nudge)
		}
	}
	return inj, nil
}

// CancelQueuedMessages clears the conversation's queued_messages array.
func (cm *ConversationManager) CancelQueuedMessages(ctx context.Context, s *Server) (*generated.Conversation, error) {
	cm.loopLifecycleMu.Lock()
	defer cm.loopLifecycleMu.Unlock()
	cm.waitForLoopTeardownLocked()
	conv, err := s.db.ClearQueuedMessages(ctx, cm.conversationID)
	if err != nil {
		return nil, err
	}
	cm.logger.Info("Cancelled queued messages")
	go s.notifySubscribers(context.WithoutCancel(ctx), cm.conversationID)
	return conv, nil
}

// CancelQueuedMessage removes a single queued message by its QueuedMessage id.
// Used by the per-ghost cancel affordance in the UI. Removing a pending
// transcription can unblock the messages queued behind it.
func (cm *ConversationManager) CancelQueuedMessage(ctx context.Context, s *Server, queuedID string) (*generated.Conversation, error) {
	cm.loopLifecycleMu.Lock()
	defer cm.loopLifecycleMu.Unlock()
	cm.waitForLoopTeardownLocked()
	conv, err := s.db.RemoveQueuedMessages(ctx, cm.conversationID, queuedID)
	if err != nil {
		return nil, err
	}
	cm.logger.Info("Cancelled queued message", "queued_id", queuedID)
	go s.notifySubscribers(context.WithoutCancel(ctx), cm.conversationID)
	cm.drainQueueIfIdle(s)
	return conv, nil
}

// recordQueued turns a queued item into immutable message rows and removes it
// from the queue in one transaction, so a crash cannot leave an array entry
// that a later drain would feed twice. A ready transcription records its audit
// pair too, but only the transcript reaches the model. It returns the user
// message as recorded and as fed to the model, with sender provenance.
func (cm *ConversationManager) recordQueued(ctx context.Context, qm db.QueuedMessage) (user, fed llm.Message, err error) {
	var messages []llm.Message
	if qm.Kind == db.QueuedMessageKindTranscription {
		if messages, err = readyTranscriptionMessages(qm); err != nil {
			return user, fed, fmt.Errorf("failed to decode queued transcription %s: %w", qm.ID, err)
		}
	} else {
		if err := json.Unmarshal(qm.Llm, &user); err != nil {
			return user, fed, fmt.Errorf("failed to decode queued message %s: %w", qm.ID, err)
		}
		messages = []llm.Message{user}
	}
	user = messages[len(messages)-1]
	if fed, err = messageWithSenderProvenance(user, qm.UserData); err != nil {
		return user, fed, err
	}
	sequenceID, err := cm.recordDrainedQueued(ctx, qm, messages)
	if err != nil {
		return user, fed, err
	}
	if cm.showsSequenceIDs() && typedUserForLLM(user, qm.UserData) {
		fed, err = messageWithSequenceID(fed, qm.ExternalMessageID, sequenceID)
	}
	return user, fed, err
}

// messageText concatenates a message's text blocks.
func messageText(message llm.Message) string {
	var b strings.Builder
	for _, content := range message.Content {
		if content.Type == llm.ContentTypeText {
			b.WriteString(content.Text)
		}
	}
	return b.String()
}

// generateSlugAsync names the conversation after its first user text in the
// background. The usage marker is published first: it owns a real
// sequence_id, so a client that never sees it observes a hole and throws away
// its cached history. It is published even when slug assignment failed, since
// the row exists regardless. Empty source text (a message with no text blocks)
// is skipped: there is nothing to name the conversation after.
func (s *Server) generateSlugAsync(conversationID, source, modelID string) {
	if source == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, marker, err := slug.GenerateSlug(ctx, s.llmManager, s.db, s.logger, conversationID, source, modelID)
		if marker != nil {
			s.notifySubscribers(ctx, conversationID, *marker)
		}
		if err != nil {
			s.logger.Warn("Failed to generate slug for conversation", "conversationID", conversationID, "error", err)
			return
		}
		go s.notifySubscribers(context.Background(), conversationID)
	}()
}

// drainPendingMessages feeds the conversation's durable queued_messages to
// the loop in FIFO order. Must be called when agentWorking transitions to false
// (and after SetDistilling(false), via runDistillNewGeneration's defer).
func (cm *ConversationManager) drainPendingMessages(s *Server) <-chan struct{} {
	owner, done := cm.beginPendingDrain()
	if !owner {
		return done
	}
	for {
		cm.drainPendingMessagesOwned(s)
		if !cm.finishPendingDrainPass(done) {
			return done
		}
	}
}

// beginPendingDrain claims draining ownership or coalesces a wakeup onto the
// active owner. The returned channel closes after all coalesced passes finish.
func (cm *ConversationManager) beginPendingDrain() (bool, chan struct{}) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.draining {
		cm.drainRequested = true
		return false, cm.drainDone
	}
	cm.draining = true
	cm.drainDone = make(chan struct{})
	return true, cm.drainDone
}

// finishPendingDrainPass returns true when a concurrent wakeup requested
// another pass. Completion is published only after no coalesced wakeup remains.
func (cm *ConversationManager) finishPendingDrainPass(done chan struct{}) bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.drainRequested {
		cm.drainRequested = false
		return true
	}
	cm.draining = false
	cm.drainDone = nil
	close(done)
	return false
}

// drainPendingMessagesOwned performs one drain pass. The caller owns draining.
func (cm *ConversationManager) drainPendingMessagesOwned(s *Server) {
	ctx := context.Background()

	// Feeding a message is a lifecycle publication: cancellation/reset must not
	// clear or replace the target loop between the DB insert and QueueMessages.
	cm.loopLifecycleMu.Lock()
	defer cm.loopLifecycleMu.Unlock()
	cm.waitForLoopTeardownLocked()

	for {
		// Never feed the loop while distillation rewrites the conversation;
		// runDistillNewGeneration drains again once SetDistilling(false)
		// returns.
		cm.mu.Lock()
		distilling := cm.distilling
		cm.mu.Unlock()
		if distilling {
			return
		}
		queued, err := cm.db.GetQueuedMessages(ctx, cm.conversationID)
		if err != nil {
			cm.logger.Error("Failed to read queued messages", "error", err)
			return
		}
		// A transcription that is not ready yet blocks everything behind it,
		// preserving FIFO order.
		if i := slices.IndexFunc(queued, func(qm db.QueuedMessage) bool {
			return qm.Kind == db.QueuedMessageKindTranscription && qm.State != db.QueuedMessageStateReady
		}); i >= 0 {
			queued = queued[:i]
		}
		if len(queued) == 0 {
			return
		}
		fed, err := cm.feedQueued(ctx, s, queued)
		// Feeding started a turn; its end drains whatever remains, including
		// an item that failed to record here. Without a fed message, the next
		// queued message drains again. Retrying immediately would hot-spin on
		// a persistent failure such as a DB outage.
		if fed {
			cm.SetAgentWorking(true)
		}
		if err != nil {
			cm.logger.Error("Failed to drain queued messages", "error", err)
			return
		}
	}
}

// feedQueued records queued items in order and feeds them to the loop,
// starting one from the DB if none is running. It stops at the first item that
// fails so later items never overtake it, and reports whether anything was
// fed. The caller holds loopLifecycleMu.
func (cm *ConversationManager) feedQueued(ctx context.Context, s *Server, queued []db.QueuedMessage) (bool, error) {
	cm.mu.Lock()
	loopInstance := cm.loop
	cm.mu.Unlock()

	cm.logger.Info("Draining queued messages", "count", len(queued))
	if loopInstance == nil {
		// Hydrate before reading the model: a stopped loop forgets it, and
		// messages queued by subagents and background jobs carry none.
		// Queued messages have no messages rows until they are recorded
		// below, so hydrating history cannot load them twice.
		if err := cm.Hydrate(ctx); err != nil {
			return false, fmt.Errorf("failed to hydrate: %w", err)
		}
		modelID := cm.GetModel()
		if i := slices.IndexFunc(queued, func(qm db.QueuedMessage) bool { return qm.Model != "" }); i >= 0 {
			modelID = queued[i].Model
		}
		svc, err := s.llmManager.GetService(modelID)
		if err != nil {
			return false, fmt.Errorf("failed to get LLM service %q: %w", modelID, err)
		}
		if err := cm.ensureLoopLocked(svc, modelID); err != nil {
			return false, fmt.Errorf("failed to start loop: %w", err)
		}
		cm.mu.Lock()
		loopInstance = cm.loop
		cm.hasConversationEvents = true
		cm.mu.Unlock()
	}

	fed := false
	for _, qm := range queued {
		user, message, err := cm.recordQueued(ctx, qm)
		if errors.Is(err, db.ErrQueuedMessageNotFound) {
			cm.logger.Info("Skipping cancelled queued message", "queued_id", qm.ID)
			continue
		}
		if err != nil {
			return fed, err
		}
		if qm.Kind == db.QueuedMessageKindTranscription {
			s.generateSlugAsync(cm.conversationID, messageText(user), qm.Model)
		}
		// notifySubscribers (fired by the record) already carried the cleaned
		// array, so the ghost clears live.
		loopInstance.QueueMessages(message)
		fed = true
	}
	return fed, nil
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

func (cm *ConversationManager) recreateBtwReaderSystemPrompt(ctx context.Context) (*generated.Message, error) {
	messages, err := cm.db.ListMessages(ctx, cm.conversationID)
	if err != nil {
		return nil, fmt.Errorf("failed to load prior BTW reader system prompt: %w", err)
	}

	systemMessage := llm.UserStringMessage(btwReaderRestrictionPrompt + "\n")
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Type != string(db.MessageTypeSystem) {
			continue
		}
		systemMessage, err = convertToLLMMessage(messages[i])
		if err != nil {
			return nil, fmt.Errorf("failed to decode prior BTW reader system prompt: %w", err)
		}
		break
	}

	created, err := cm.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: cm.conversationID,
		Type:           db.MessageTypeSystem,
		LLMData:        systemMessage,
		UsageData:      llm.Usage{},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to recreate BTW reader system prompt: %w", err)
	}
	return created, nil
}

// mcpServerInfos lists the registered MCP servers for the system prompt.
func (cm *ConversationManager) mcpServerInfos(ctx context.Context) ([]MCPServerInfo, error) {
	var rows []generated.McpServer
	err := cm.db.Queries(ctx, func(q *generated.Queries) (err error) {
		rows, err = q.ListMCPServers(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	servers := make([]MCPServerInfo, len(rows))
	for i, row := range rows {
		servers[i] = MCPServerInfo{Name: row.Name, Description: row.Description}
	}
	return servers, nil
}

func (cm *ConversationManager) createSystemPrompt(ctx context.Context) (*generated.Message, error) {
	systemPrompt := cm.conversationOptions.SystemPrompt
	var promptSkills []skills.Skill
	if systemPrompt == "" {
		var opts []SystemPromptOption
		if cm.userEmail != "" {
			opts = append(opts, WithUserEmail(cm.userEmail))
		}
		if servers, err := cm.mcpServerInfos(ctx); err != nil {
			cm.logger.Warn("failed to list MCP servers for system prompt", "error", err)
		} else if len(servers) > 0 {
			opts = append(opts, WithMCPServers(servers))
		}
		var err error
		systemPrompt, promptSkills, err = generateSystemPromptWithIntegrationSkills(cm.cwd, cm.integrationSkills.Skills(ctx), opts...)
		if err != nil {
			return nil, fmt.Errorf("failed to generate system prompt: %w", err)
		}
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
		DisplayData:    cm.systemPromptDisplayData(promptSkills),
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

// systemPromptDisplayData builds the tool and skill metadata shown alongside a
// persisted system prompt. Skill bodies stay out of display data; the card only
// needs discovery metadata and the activation command.
func systemPromptDisplayData(cfg claudetool.ToolSetConfig, promptSkills []skills.Skill) map[string]any {
	type toolDesc struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
		SourcePath  string          `json:"source_path,omitempty"`
		Origin      string          `json:"origin"`
		Type        string          `json:"type,omitempty"`
		ServerSide  bool            `json:"server_side,omitempty"`
	}
	type skillDesc struct {
		Name          string            `json:"name"`
		Description   string            `json:"description"`
		Activate      string            `json:"activate"`
		SourcePath    string            `json:"source_path"`
		Origin        string            `json:"origin"`
		License       string            `json:"license,omitempty"`
		Compatibility string            `json:"compatibility,omitempty"`
		When          string            `json:"when,omitempty"`
		AllowedTools  string            `json:"allowed_tools,omitempty"`
		Metadata      map[string]string `json:"metadata,omitempty"`
	}

	ts := claudetool.NewToolSet(context.Background(), cfg)
	defer ts.Cleanup()

	toolDescs := make([]toolDesc, 0, len(ts.Tools()))
	for _, tool := range ts.Tools() {
		var params json.RawMessage
		if len(tool.InputSchema) > 0 && string(tool.InputSchema) != "null" {
			params = tool.InputSchema
		}
		origin := "Shelley"
		sourcePath := ""
		if tool.ServerSide {
			origin = "Provider"
		} else if info, ok := claudetool.ToolInfoByName(tool.Name); ok {
			sourcePath = info.SourcePath
		}
		toolDescs = append(toolDescs, toolDesc{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  params,
			SourcePath:  sourcePath,
			Origin:      origin,
			Type:        tool.Type,
			ServerSide:  tool.ServerSide,
		})
	}

	skillDescs := make([]skillDesc, 0, len(promptSkills))
	for _, skill := range promptSkills {
		sourcePath := skill.SourceLocation()
		origin := skill.Origin
		if origin == "" {
			if skill.Path != "" {
				origin = "File"
			} else {
				origin = "Built into Shelley"
			}
		}
		skillDescs = append(skillDescs, skillDesc{
			Name:          skill.Name,
			Description:   skill.Description,
			Activate:      skill.ActivationCommand(),
			SourcePath:    sourcePath,
			Origin:        origin,
			License:       skill.License,
			Compatibility: skill.Compatibility,
			When:          skill.When,
			AllowedTools:  skill.AllowedTools,
			Metadata:      skill.Metadata,
		})
	}

	return map[string]any{
		"tools":  toolDescs,
		"skills": skillDescs,
	}
}

func (cm *ConversationManager) systemPromptDisplayData(promptSkills []skills.Skill) map[string]any {
	cfg := cm.toolSetConfig
	cfg.ToolOverrides = cm.conversationOptions.ToolOverrides
	cfg.DisableAllTools = cm.conversationOptions.DisableAllTools
	cfg.InPlaceCompactor = inPlaceCompactor{cm: cm}
	cfg.UserMessageFinder = cm.userMessageFinder()
	cfg.UserChat = cm.userChat
	return systemPromptDisplayData(cfg, promptSkills)
}

func (cm *ConversationManager) createSubagentSystemPrompt(ctx context.Context) (*generated.Message, error) {
	// Only subagents use MCP tools; internal workers (commit tours,
	// transcriptions) don't, and notifyMCPServersChanged skips them too.
	var mcpServers []MCPServerInfo
	if cm.role == roleSubagent {
		if servers, err := cm.mcpServerInfos(ctx); err != nil {
			cm.logger.Warn("failed to list MCP servers for subagent system prompt", "error", err)
		} else {
			mcpServers = servers
		}
	}
	systemPrompt, promptSkills, err := generateSubagentSystemPromptWithIntegrationSkills(cm.cwd, cm.role == roleSubagent, cm.integrationSkills.Skills(ctx), mcpServers)
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
		DisplayData:    cm.systemPromptDisplayData(promptSkills),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to store subagent system prompt: %w", err)
	}

	cm.logger.Info("Stored subagent system prompt", "length", len(systemPrompt))
	return created, nil
}

func subagentPromptCacheKey(system []llm.SystemContent, modelID string) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "%s\x00", modelID)
	for _, item := range system {
		fmt.Fprintf(hash, "%s\x00%s\x00", item.Type, item.Text)
	}
	return fmt.Sprintf("subagent-%x", hash.Sum(nil)[:16])
}

func (cm *ConversationManager) partitionMessages(messages []generated.Message) ([]llm.Message, []llm.SystemContent, error) {
	items, system, err := cm.contextItems(messages)
	if err != nil {
		return nil, nil, err
	}
	history := make([]llm.Message, len(items))
	for i, it := range items {
		history[i] = it.message
	}
	return history, system, nil
}

// contextItems builds the LLM's view of messages (one generation's context
// rows, in sequence order): the system prompt, and the compacted history with
// distillation overrides and sender provenance applied to user messages.
func (cm *ConversationManager) contextItems(messages []generated.Message) ([]contextItem, []llm.SystemContent, error) {
	history, systemMessages, err := compactedContext(cm.logger, messages)
	if err != nil {
		return nil, nil, err
	}
	var system []llm.SystemContent
	for _, m := range systemMessages {
		for _, content := range m.Content {
			if content.Type == llm.ContentTypeText && content.Text != "" {
				system = append(system, llm.SystemContent{Type: "text", Text: content.Text})
			}
		}
	}
	showSequenceIDs := cm.showsSequenceIDs()
	for i := range history {
		src := history[i].source
		if src == nil || src.Type != string(db.MessageTypeUser) {
			continue
		}
		cm.applyDistillationContentOverride(&history[i].message, *src)
		var userData []byte
		if src.UserData != nil {
			userData = []byte(*src.UserData)
		}
		wrapped, err := messageWithSenderProvenance(history[i].message, userData)
		if err != nil {
			return nil, nil, fmt.Errorf("apply sender provenance to message %s: %w", src.MessageID, err)
		}
		if showSequenceIDs && typedUserForLLM(history[i].message, userData) {
			origin, err := originSequenceID(*src)
			if err != nil {
				return nil, nil, fmt.Errorf("origin sequence of message %s: %w", src.MessageID, err)
			}
			wrapped, err = messageWithSequenceID(wrapped, derefString(src.ExternalMessageID), origin)
			if err != nil {
				return nil, nil, fmt.Errorf("render user message %s: %w", src.MessageID, err)
			}
		}
		history[i].message = wrapped
	}
	return history, system, nil
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

// detachedLoop is a loop removed from the manager while its teardown boundary
// is still open.
type detachedLoop struct {
	cancel     context.CancelFunc
	done       <-chan struct{}
	toolSet    *claudetool.ToolSet
	generation uint64
}

// detachLoopLocked uninstalls the current loop and opens a teardown boundary:
// advancing the generation invalidates the loop's delayed callbacks, and
// loopTearingDown blocks any replacement until finishLoopTeardownLocked closes
// the boundary with the returned generation. The caller holds loopLifecycleMu
// and cm.mu.
func (cm *ConversationManager) detachLoopLocked() detachedLoop {
	cm.loopGeneration++
	detached := detachedLoop{
		cancel:     cm.loopCancel,
		done:       cm.loopDone,
		toolSet:    cm.toolSet,
		generation: cm.loopGeneration,
	}
	cm.loopTearingDown = true
	cm.loopLifecycleDone = make(chan struct{})
	cm.loopCancel = nil
	cm.loopCtx = nil
	cm.loopDone = nil
	cm.loop = nil
	cm.modelID = ""
	cm.toolSet = nil
	return detached
}

// stop cancels a detached loop, waits for it to exit unless done is nil, and
// releases its tools. A waiting caller must not hold cm.mu or loopLifecycleMu:
// the exiting loop may need them.
func (d detachedLoop) stop() {
	if d.cancel != nil {
		d.cancel()
	}
	if d.done != nil {
		<-d.done
	}
	if d.toolSet != nil {
		d.toolSet.Cleanup()
	}
}

// ensureLoop creates a loop only after any prior lifecycle transition has
// completed. Callers that already hold loopLifecycleMu use ensureLoopLocked.
func (cm *ConversationManager) ensureLoop(service llm.Service, modelID string) error {
	cm.loopLifecycleMu.Lock()
	defer cm.loopLifecycleMu.Unlock()
	cm.waitForLoopTeardownLocked()
	return cm.ensureLoopLocked(service, modelID)
}

func (cm *ConversationManager) serviceForLoop(service llm.Service) (llm.Service, error) {
	if cm.decorateService == nil {
		return service, nil
	}
	return cm.decorateService(service)
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
	role := cm.role
	database := cm.db
	toolSetConfig.Env = claudetool.ShelleyEnv{
		ConversationSlug: cm.slug,
		Model:            modelID,
		UserEmail:        cm.userEmail,
		Port:             cm.serverPort,
		Socket:           cm.serverSocket,
	}
	cm.mu.Unlock()

	// Load conversation history fresh from the database. This is the canonical
	// read — Hydrate only handles metadata and system prompt generation.
	// Reading here ensures we always see messages added asynchronously
	// (e.g. distillation results).
	var dbMessages []generated.Message
	err := database.Queries(context.Background(), func(q *generated.Queries) error {
		var err error
		dbMessages, err = q.ListMessagesForContext(context.Background(), conversationID)
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to load conversation history: %w", err)
	}
	history, system, err := cm.partitionMessages(dbMessages)
	if err != nil {
		return fmt.Errorf("failed to prepare conversation history: %w", err)
	}
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
	if role == roleBtwReader {
		toolSetConfig.EnableJITInstall = false
		toolSetConfig.EnableBrowser = true
		toolSetConfig.DisableAllTools = true
		toolSetConfig.ToolOverrides = map[string]string{
			"bash":       "on",
			"read_image": "on",
		}
	}
	toolSetConfig.InPlaceCompactor = inPlaceCompactor{cm: cm, generation: generation}
	toolSetConfig.UserMessageFinder = cm.userMessageFinder()
	toolSetConfig.UserChat = cm.userChat
	toolSet := claudetool.NewToolSet(processCtx, toolSetConfig)
	var nudger *contextNudger
	if claudetool.IsToolEnabled(claudetool.CompactInPlaceName, toolSetConfig.ToolOverrides, toolSetConfig.DisableAllTools) &&
		!conversationOpts.DisableCompactNudges {
		nudger = newContextNudger(conversationOpts.CompactNudgeTokens, lastContextWindowSize(dbMessages))
		record := recordMessage
		recordMessage = func(ctx context.Context, message llm.Message, usage llm.Usage, otherUsage []llm.PurposedUsage) error {
			nudger.observe(usage)
			return record(ctx, message, usage, otherUsage)
		}
	}

	// streamFlusher batches LLM stream deltas and flushes them periodically
	// to avoid overwhelming the bounded subpub queue with hundreds
	// of individual deltas per second from the Anthropic SSE stream.
	sf := newStreamFlusher(cm, streamFlushInterval, func() bool {
		return cm.isCurrentLoopGeneration(generation)
	})

	service, err = cm.serviceForLoop(service)
	if err != nil {
		cancel()
		toolSet.Cleanup()
		return fmt.Errorf("decorate LLM service: %w", err)
	}
	promptCacheKey := ""
	if role == roleSubagent || role == roleWorker {
		promptCacheKey = subagentPromptCacheKey(system, modelID)
	}
	loopInstance := loop.NewLoop(loop.Config{
		LLM:            service,
		ModelID:        modelID,
		History:        history,
		Tools:          toolSet.Tools(),
		ThinkingLevel:  llm.ParseThinkingLevel(conversationOpts.ThinkingLevel),
		PromptCacheKey: promptCacheKey,
		RecordMessage:  recordMessage,
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
		InjectMessages: func(ctx context.Context) (loop.Injection, error) {
			return cm.takeInjectable(ctx, generation, nudger)
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
	cm.mu.Lock()
	if cm.loop != loopInstance || cm.loopGeneration != generation {
		cm.mu.Unlock()
		return
	}
	detached := cm.detachLoopLocked()
	detached.done = nil // this is the loop goroutine; loopDone closes after we return
	cm.hydrated = false
	cm.hasConversationEvents = false
	cm.mu.Unlock()

	detached.stop()
	cm.SetAgentWorking(false)
	cm.finishLoopTeardownLocked(detached.generation)
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
	_ = cm.resetLoopAfter(markUnhydrated, func() error { return nil })
}

// resetLoopAfter is resetLoop preceded by mutate, which runs under
// loopLifecycleMu so no turn can start while it runs. If mutate fails, the
// loop is left alone and the error returned.
func (cm *ConversationManager) resetLoopAfter(markUnhydrated bool, mutate func() error) error {
	cm.loopLifecycleMu.Lock()
	cm.waitForLoopTeardownLocked()
	if err := mutate(); err != nil {
		cm.loopLifecycleMu.Unlock()
		return err
	}

	cm.mu.Lock()
	if markUnhydrated {
		cm.hydrated = false
		cm.hasConversationEvents = false
	}
	if cm.loop == nil {
		cm.mu.Unlock()
		cm.loopLifecycleMu.Unlock()
		return nil
	}
	detached := cm.detachLoopLocked()
	cm.mu.Unlock()
	cm.loopLifecycleMu.Unlock()

	detached.stop()
	cm.loopLifecycleMu.Lock()
	cm.finishLoopTeardownLocked(detached.generation)
	cm.loopLifecycleMu.Unlock()
	return nil
}

// CancelConversation cancels the active loop, clears queued user work, and
// synchronously ends its turn.
func (cm *ConversationManager) CancelConversation(ctx context.Context) error {
	return cm.cancelConversation(ctx, true, "")
}

// SendQueuedNow interrupts the active turn without clearing its durable queue,
// then starts draining from the FIFO head.
func (cm *ConversationManager) SendQueuedNow(ctx context.Context, s *Server, queuedID string) error {
	cm.mu.Lock()
	distilling := cm.distilling
	cm.mu.Unlock()
	if distilling {
		return fmt.Errorf("cannot send queued message while compacting")
	}
	if err := cm.cancelConversation(ctx, false, queuedID); err != nil {
		return err
	}
	go cm.drainPendingMessages(s)
	return nil
}

// cancelConversation cancels the active loop and synchronously ends its turn.
// When clearQueued is false, queued_messages survive
// the interruption so SendQueuedNow can feed them immediately afterward.
// The loop records the complete tool-result batch, including partial output from
// cancelled tools, before this method writes the end-of-turn marker.
func (cm *ConversationManager) cancelConversation(ctx context.Context, clearQueued bool, sendQueuedID string) error {
	cm.loopLifecycleMu.Lock()
	cm.waitForLoopTeardownLocked()
	if sendQueuedID != "" {
		conversation, err := cm.db.GetConversationByID(ctx, cm.conversationID)
		if err != nil {
			cm.loopLifecycleMu.Unlock()
			return err
		}
		queued, err := db.ParseQueuedMessagesStrict(conversation.QueuedMessages)
		if err != nil {
			cm.loopLifecycleMu.Unlock()
			return err
		}
		if len(queued) == 0 || queued[0].ID != sendQueuedID {
			cm.loopLifecycleMu.Unlock()
			return fmt.Errorf("queued message is no longer first")
		}
		if queued[0].Kind == db.QueuedMessageKindTranscription && queued[0].State != db.QueuedMessageStateReady {
			cm.loopLifecycleMu.Unlock()
			return fmt.Errorf("transcription is not ready")
		}
	}

	cm.mu.Lock()
	if sendQueuedID != "" && !cm.agentWorking {
		cm.mu.Unlock()
		cm.loopLifecycleMu.Unlock()
		return nil
	}
	if cm.loop == nil {
		wasCancelling := cm.cancelling
		if clearQueued {
			cm.hydrated = false
			cm.hasConversationEvents = false
		}
		cm.mu.Unlock()

		persistCtx := context.WithoutCancel(ctx)
		if err := cm.db.ClearConversationRuntimeState(persistCtx, cm.conversationID); err != nil {
			cm.loopLifecycleMu.Unlock()
			return fmt.Errorf("failed to clear idle conversation state: %w", err)
		}
		cm.mu.Lock()
		cm.cancelling = true
		cm.mu.Unlock()
		cm.syncAgentWorking(false)
		cm.mu.Lock()
		cm.cancelling = wasCancelling
		cm.mu.Unlock()
		if clearQueued {
			if _, err := cm.db.ClearQueuedMessages(persistCtx, cm.conversationID); err != nil {
				cm.loopLifecycleMu.Unlock()
				return fmt.Errorf("failed to clear queued messages: %w", err)
			}
		}
		cm.loopLifecycleMu.Unlock()
		cm.logger.Info("No active loop to cancel", "clear_queued", clearQueued)
		return nil
	}
	detached := cm.detachLoopLocked()
	cm.cancelling = true
	cm.hydrated = false
	cm.hasConversationEvents = false
	cm.mu.Unlock()
	cm.loopLifecycleMu.Unlock()

	cm.logger.Info("Cancelling conversation", "clear_queued", clearQueued)
	persistCtx := context.WithoutCancel(ctx)
	if clearQueued {
		if conv, err := cm.db.GetConversationByID(persistCtx, cm.conversationID); err != nil {
			cm.logger.Error("Failed to read queued messages on cancel", "error", err)
		} else if conv.QueuedMessages != "" && conv.QueuedMessages != "[]" {
			if _, err := cm.db.ClearQueuedMessages(persistCtx, cm.conversationID); err != nil {
				cm.logger.Error("Failed to clear queued messages on cancel", "error", err)
			}
		}
	}

	detached.stop()

	// No replacement can have been installed while loopTearingDown was true.
	// Reacquire the lifecycle lock before publishing the end marker and reopen
	// the boundary only after that marker has committed.
	cm.loopLifecycleMu.Lock()
	defer cm.loopLifecycleMu.Unlock()
	defer cm.finishLoopTeardownLocked(detached.generation)

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
// and/or reasoning level, or enables a tool (ToolEnabled). The Reasoning*
// fields carry user-facing level names ("off", "low", ..., or "default" for
// the service default); they are empty when reasoning didn't change.
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
	// ToolEnabled names a tool enabled mid-conversation (see
	// EnableCompactInPlace); the model and reasoning are unchanged then.
	ToolEnabled string `json:"tool_enabled,omitempty"`
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
	cm.modelSettingsMu.Lock()
	defer cm.modelSettingsMu.Unlock()

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
