package server

import (
	"context"
	"encoding/json"
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

// pendingBatchKind discriminates the two sources of queued work.
type pendingBatchKind int

const (
	// pendingBatchUser is a user-typed message queued during a busy turn or
	// distillation. The DB row already exists (excluded_from_context=true,
	// userData={queued:true}); drain feeds it to the loop and un-excludes.
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
	// MessageIDs is non-empty only for Kind=pendingBatchUser (one DB row
	// per message); used for cancel and to clear excluded_from_context
	// after delivery. Indexed parallel to Messages.
	MessageIDs []string
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
	// streamPub mirrors per-conversation events to the server-wide /api/stream2
	// subscribers. Each event is tagged with the manager's ConversationID by
	// the publish helpers below before fan-out.
	streamPub *subpub.SubPub[StreamResponse]

	// hydrateMu serializes Hydrate so concurrent callers don't race on the
	// fields it populates (cwd, modelID, conversationOptions, toolSetConfig,
	// hasConversationEvents, agentWorking) between the initial unlocked
	// hydrated-check and the final write under cm.mu.
	hydrateMu             sync.Mutex
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

	// pendingBatches holds batches of messages queued to be sent after the
	// current turn ends (or after distillation completes). One queue serves
	// both user messages and subagent-done notifications, so distillation
	// and turn-end serialization — which already gate drainPendingMessages —
	// gate both sources uniformly.
	pendingBatches []pendingBatch

	// draining is true while a drainPendingMessages goroutine is in flight
	// for this conversation. It ensures at most one drainer runs at a time
	// so concurrent enqueues don't race to start parallel drainers (which
	// would interleave each other's batches into the loop and history).
	draining bool

	// retryMu serializes RetryLastLLMRequest so concurrent retry POSTs don't
	// produce duplicate LLM calls or double-broadcast user_data updates.
	retryMu sync.Mutex

	// onStateChange is called when the conversation state changes.
	// This allows the server to broadcast state changes to all subscribers.
	onStateChange func(state ConversationState)

	// onDone is called when the agent finishes working (transitions to not working).
	// Used by subagents to notify their parent conversation.
	onDone func()
}

// NewConversationManager constructs a manager with dependencies but defers hydration until needed.
func NewConversationManager(conversationID string, database *db.DB, baseLogger *slog.Logger, toolSetConfig claudetool.ToolSetConfig, recordMessage loop.MessageRecordFunc, onStateChange func(ConversationState), streamPub *subpub.SubPub[StreamResponse]) *ConversationManager {
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
		streamPub:      streamPub,
		onStateChange:  onStateChange,
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

// SetAgentWorking updates the agent working state and notifies the server to broadcast.
// The new value is also persisted to the conversations table so the
// conversation list patch stream picks it up via the standard Pool.OnCommit
// hook (no explicit notify required).
func (cm *ConversationManager) SetAgentWorking(working bool) {
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
	cm.mu.Unlock()

	cm.logger.Debug("agent working state changed", "working", working)
	if err := cm.db.SetConversationAgentWorking(context.Background(), convID, working); err != nil {
		cm.logger.Error("failed to persist agent working state", "error", err, "working", working)
	}
	if onStateChange != nil {
		onStateChange(ConversationState{
			ConversationID: convID,
			Working:        working,
			Model:          modelID,
		})
	}
	if !working && onDone != nil {
		onDone()
	}
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

func (cm *ConversationManager) BeginDistillingSetup() {
	cm.mu.Lock()
	if !cm.distilling {
		cm.distilling = true
	}
	if cm.distillSetupDone == nil {
		cm.distillSetupDone = make(chan struct{})
	}
	cm.mu.Unlock()
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

	// Mark agent as working BEFORE persisting the user message. The
	// conversation_list_patch stream fires off the Pool.OnCommit hook on
	// every Tx commit and snapshots conversations.agent_working at that
	// moment, so we must commit the agent_working=true flip first.
	// Otherwise the user-message commit's list-patch carries the stale
	// agent_working=false row that pre-dated this turn, the client applies
	// it as authoritative, and the working/thinking indicator flickers off
	// until the agent_working=true commit's patch lands a moment later.
	cm.SetAgentWorking(true)

	// Record the user message to the database immediately so it appears in the UI,
	// even if the loop is busy processing a previous request
	if recordMessage != nil {
		if err := recordMessage(ctx, message, llm.Usage{}); err != nil {
			cm.logger.Error("failed to record user message immediately", "error", err)
			// Continue anyway - the loop will also try to record it
		}
	}

	loopInstance.QueueUserMessage(message)

	return isFirst, nil
}

// errRetryNotApplicable is returned by RetryLastLLMRequest when the latest
// message isn't a fresh retryable error — covers idempotency for duplicate
// clicks after the first one already flagged the error as retried.
var errRetryNotApplicable = fmt.Errorf("latest message is not a retryable error; nothing to retry")

// RetryLastLLMRequest flags the most recent error message as retried (so the
// UI hides its Retry button) and asks the loop to re-attempt the previous
// LLM request. The error message itself remains in the conversation log —
// messages are an append-only log; partitionMessages already strips error
// messages before sending history to the LLM, so the retried request body
// is byte-identical to the failed one.
//
// Multiple rapid clicks on the Retry button must not produce multiple extra
// LLM calls; retryMu serializes invocations and the retried-flag check
// makes the operation idempotent.
func (cm *ConversationManager) RetryLastLLMRequest(ctx context.Context) error {
	// Take retryMu first to serialize across concurrent retries without
	// holding cm.mu (which would block unrelated message recording and
	// state changes for the duration of the DB update + broadcast).
	cm.retryMu.Lock()
	defer cm.retryMu.Unlock()
	cm.mu.Lock()
	loopInstance := cm.loop
	logger := cm.logger
	conversationID := cm.conversationID
	database := cm.db
	cm.mu.Unlock()

	if loopInstance == nil {
		return fmt.Errorf("no active loop to retry")
	}

	latest, err := database.GetLatestMessage(ctx, conversationID)
	if err != nil {
		return fmt.Errorf("failed to load latest message: %w", err)
	}
	if latest.Type != string(db.MessageTypeError) {
		return errRetryNotApplicable
	}

	// Parse existing user_data and merge in retried=true. If the message has
	// already been retried (e.g. duplicate click), bail out without firing a
	// second loop attempt.
	ud := map[string]any{}
	if latest.UserData != nil && *latest.UserData != "" {
		if err := json.Unmarshal([]byte(*latest.UserData), &ud); err != nil {
			return fmt.Errorf("failed to parse error message user_data: %w", err)
		}
	}
	if retried, _ := ud["retried"].(bool); retried {
		return errRetryNotApplicable
	}
	if retryable, _ := ud["retryable"].(bool); !retryable {
		return errRetryNotApplicable
	}
	ud["retried"] = true
	udBytes, err := json.Marshal(ud)
	if err != nil {
		return fmt.Errorf("failed to marshal updated user_data: %w", err)
	}
	udStr := string(udBytes)
	if err := database.UpdateMessageUserData(ctx, latest.MessageID, &udStr); err != nil {
		return fmt.Errorf("failed to mark error message retried: %w", err)
	}
	logger.Info("retrying after marking error message retried", "message_id", latest.MessageID)

	// Re-load the updated row and broadcast it as a normal message upsert so
	// subscribed UIs refresh the error's user_data and drop the Retry button.
	updated, err := database.GetMessageByID(ctx, latest.MessageID)
	if err != nil {
		return fmt.Errorf("failed to reload updated error message: %w", err)
	}
	cm.broadcastStream(StreamResponse{
		Messages: toAPIMessages([]generated.Message{*updated}),
	})

	cm.SetAgentWorking(true)
	loopInstance.Retry()
	return nil
}

// QueueMessage records a user message to the database as "queued" and holds it
// for delivery after the current agent turn (or distillation) completes.
// The message is visible in the UI immediately (with queued status).
func (cm *ConversationManager) QueueMessage(ctx context.Context, s *Server, modelID string, message llm.Message) error {
	cm.waitDistillingSetup()

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

	cm.logger.Info("Queued user message", "message_id", createdMsg.MessageID)
	cm.enqueueBatch(s, pendingBatch{
		Kind:       pendingBatchUser,
		Messages:   []llm.Message{message},
		ModelID:    modelID,
		MessageIDs: []string{createdMsg.MessageID},
	})
	return nil
}

// EnqueueSubagentDone appends a subagent-done batch (synthetic
// assistant tool_use + matching user tool_result) onto the pending-batch
// queue. If the agent is idle and not distilling, drains immediately;
// otherwise the batch waits for the current turn or distillation to
// finish, at which point drainPendingMessages picks it up. The synthetic
// messages are NOT persisted here — drainPendingMessages records them in
// order so they can't be reordered relative to other queued work.
//
// modelID is used to start the parent's loop if it's currently idle; pass
// the empty string to fall back to the manager's last-known modelID.
func (cm *ConversationManager) EnqueueSubagentDone(s *Server, modelID string, assistant, toolResult llm.Message) {
	cm.enqueueBatch(s, pendingBatch{
		Kind:     pendingBatchSubagentDone,
		Messages: []llm.Message{assistant, toolResult},
		ModelID:  modelID,
	})
}

// enqueueBatch appends a batch to the pending queue and, if the agent is
// idle, kicks off a drain goroutine. drainPendingMessages itself acquires
// the draining flag under cm.mu, so concurrent enqueueBatch calls can both
// safely spawn drain goroutines — only the first will own the drain; the
// others will see draining=true and exit, having already appended their
// batches for the winning drainer to pick up.
func (cm *ConversationManager) enqueueBatch(s *Server, b pendingBatch) {
	cm.mu.Lock()
	cm.pendingBatches = append(cm.pendingBatches, b)
	cm.lastActivity = time.Now()
	needsDrain := !cm.agentWorking && !cm.distilling
	cm.mu.Unlock()

	if needsDrain {
		go cm.drainPendingMessages(s)
	}
}

// CancelQueuedMessages removes all pending queued *user* messages and deletes
// them from the DB. Subagent-done batches stay queued: they represent work
// the parent agent still needs to acknowledge, and they have no DB rows to
// delete (those are recorded at drain time).
func (cm *ConversationManager) CancelQueuedMessages(ctx context.Context, s *Server) {
	cm.mu.Lock()
	var cancelled []pendingBatch
	var keep []pendingBatch
	for _, b := range cm.pendingBatches {
		if b.Kind == pendingBatchUser {
			cancelled = append(cancelled, b)
		} else {
			keep = append(keep, b)
		}
	}
	cm.pendingBatches = keep
	cm.mu.Unlock()

	total := 0
	for _, b := range cancelled {
		for _, id := range b.MessageIDs {
			if err := s.db.QueriesTx(ctx, func(q *generated.Queries) error {
				return q.DeleteMessage(ctx, id)
			}); err != nil {
				cm.logger.Error("Failed to delete queued message", "message_id", id, "error", err)
			}
			total++
		}
	}

	if total > 0 {
		cm.logger.Info("Cancelled queued messages", "count", total)
		// Notify subscribers so the UI removes the cancelled messages
		go s.notifySubscribers(context.WithoutCancel(ctx), cm.conversationID)
	}
}

// processBatch feeds one pendingBatch into the loop and handles its
// batch-kind-specific persistence side effects. Errors are logged; we do
// not unwind earlier successful batches.
func (cm *ConversationManager) processBatch(ctx context.Context, s *Server, loopInstance *loop.Loop, b pendingBatch) {
	switch b.Kind {
	case pendingBatchUser:
		// User batches: DB rows already exist (excluded). Feed to loop,
		// then un-exclude and broadcast.
		loopInstance.QueueMessages(b.Messages...)
		for _, id := range b.MessageIDs {
			if err := s.db.QueriesTx(ctx, func(q *generated.Queries) error {
				if err := q.UpdateMessageExcludedFromContext(ctx, generated.UpdateMessageExcludedFromContextParams{
					ExcludedFromContext: false,
					MessageID:           id,
				}); err != nil {
					return err
				}
				newData := `{}`
				return q.UpdateMessageUserData(ctx, generated.UpdateMessageUserDataParams{
					UserData:  &newData,
					MessageID: id,
				})
			}); err != nil {
				cm.logger.Error("Failed to update queued message", "message_id", id, "error", err)
			}
			if updatedMsg, err := s.db.GetMessageByID(ctx, id); err == nil {
				go s.broadcastMessageUpdate(ctx, cm.conversationID, updatedMsg)
			}
		}
	case pendingBatchSubagentDone:
		// Subagent-done batches: persist the synthetic pair now (in batch
		// order so a future Hydrate reads them back correctly), then feed
		// atomically to the loop. If the first record fails we skip the
		// second — a half-written tool_use without a tool_result would
		// corrupt history.
		for _, msg := range b.Messages {
			if err := cm.recordMessage(ctx, msg, llm.Usage{}); err != nil {
				cm.logger.Error("Failed to record synthetic subagent message", "error", err)
				return
			}
		}
		loopInstance.QueueMessages(b.Messages...)
	}
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
	// or post-cancel), Hydrate+ensureLoop reads history from the DB; user
	// batches are still excluded_from_context so they won't double-load, and
	// subagent-done batches have no DB rows yet.
	if loopInstance == nil {
		if err := cm.Hydrate(ctx); err != nil {
			cm.logger.Error("Failed to hydrate for queued batches", "error", err)
			return
		}
		if err := cm.ensureLoop(svc, modelID); err != nil {
			cm.logger.Error("Failed to start loop for queued batches", "error", err)
			return
		}
		cm.mu.Lock()
		loopInstance = cm.loop
		cm.hasConversationEvents = true
		cm.mu.Unlock()
	}
	if loopInstance == nil {
		return
	}

	for _, b := range batches {
		cm.processBatch(ctx, s, loopInstance, b)
	}

	cm.SetAgentWorking(true)

	// More batches may have been enqueued while we were draining. Loop
	// back to pick them up under the same draining ownership so we never
	// start a second concurrent drainer.
	goto restart
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
	cm.subpub.Publish(result.Message.SequenceID, StreamResponse{
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
		BuildAvailableModels: cm.toolSetConfig.BuildAvailableModels,
		ModelID:              cm.toolSetConfig.ModelID,
		CLIAgent:             cm.conversationOptions.SubagentBackend,
		ToolOverrides:        cm.conversationOptions.ToolOverrides,
		DisableAllTools:      cm.conversationOptions.DisableAllTools,
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
		DisplayData:    cm.systemPromptDisplayData(),
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

		if msg.Type == string(db.MessageTypeUser) {
			cm.applyDistillationContentOverride(&llmMsg, msg)
		}

		history = append(history, llmMsg)
	}

	return history, system
}

func (cm *ConversationManager) applyDistillationContentOverride(llmMsg *llm.Message, msg generated.Message) {
	if msg.UserData == nil {
		return
	}

	var userData map[string]string
	if err := json.Unmarshal([]byte(*msg.UserData), &userData); err != nil {
		cm.logger.Warn("Failed to parse message user_data", "messageID", msg.MessageID, "error", err)
		return
	}
	if userData["distilled"] != "true" {
		return
	}

	content := userData["distillation_content"]
	if filePath := userData["distillation_file"]; filePath != "" {
		if !isDistillationTempFile(filePath) {
			cm.logger.Warn("Distillation file path validation failed", "messageID", msg.MessageID, "path", filePath)
		} else if fileContent, err := os.ReadFile(filePath); err == nil {
			content = string(fileContent)
		} else {
			cm.logger.Warn("Failed to read editable distillation file; using stored content", "messageID", msg.MessageID, "path", filePath, "error", err)
		}
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
			BuildAvailableModels: toolSetConfig.BuildAvailableModels,
			WorkingDir:           cwd,
			OnWorkingDirChange:   toolSetConfig.OnWorkingDirChange,
			EnableBrowser:        toolSetConfig.EnableBrowser,
			CLIAgent:             conversationOpts.SubagentBackend,
			ToolOverrides:        conversationOpts.ToolOverrides,
			DisableAllTools:      conversationOpts.DisableAllTools,
		})
	} else {
		toolSetConfig.ToolOverrides = conversationOpts.ToolOverrides
		toolSetConfig.DisableAllTools = conversationOpts.DisableAllTools
		toolSet = claudetool.NewToolSet(processCtx, toolSetConfig)
	}

	// streamFlusher batches LLM stream deltas and flushes them periodically
	// to avoid overwhelming the subpub channel (buffer=10) with hundreds
	// of individual deltas per second from the Anthropic SSE stream.
	sf := newStreamFlusher(cm, 50*time.Millisecond)

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
			cm.broadcastStream(StreamResponse{
				ToolProgress: &progress,
			})
		},
		OnStreamDelta: sf.Push,
		OnStreamDone:  sf.Flush,
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
	cm.resetLoop(false)
}

// ResetLoop drops the in-memory LLM loop so the next turn hydrates from the DB.
func (cm *ConversationManager) ResetLoop() {
	cm.resetLoop(true)
}

func (cm *ConversationManager) resetLoop(markUnhydrated bool) {
	cm.mu.Lock()
	cancel := cm.loopCancel
	toolSet := cm.toolSet
	cm.loopCancel = nil
	cm.loopCtx = nil
	cm.loop = nil
	cm.modelID = ""
	cm.toolSet = nil
	if markUnhydrated {
		cm.hydrated = false
		cm.hasConversationEvents = false
	}
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

	// Clear pending queued batches BEFORE recording the end-of-turn message.
	// The end-of-turn message triggers drainPendingMessages via
	// notifySubscribers; clearing first ensures the drain finds nothing to
	// process. We DROP everything on cancel — including pending
	// subagent-done notifications — because a cancelled turn means the user
	// is taking over; any followups they want to ask about subagents will
	// arrive as new user messages.
	cm.mu.Lock()
	pendingToDelete := cm.pendingBatches
	cm.pendingBatches = nil
	cm.mu.Unlock()

	// Delete orphaned queued user-message DB rows. Subagent-done batches
	// have no DB rows yet (they would have been recorded at drain time),
	// so nothing to delete for those.
	for _, b := range pendingToDelete {
		for _, id := range b.MessageIDs {
			if err := cm.db.QueriesTx(ctx, func(q *generated.Queries) error {
				return q.DeleteMessage(ctx, id)
			}); err != nil {
				cm.logger.Error("Failed to delete queued message on cancel", "message_id", id, "error", err)
			}
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
		Conversation: &conversation,
	}
	cm.publishStream(msg.SequenceID, streamData)
}
