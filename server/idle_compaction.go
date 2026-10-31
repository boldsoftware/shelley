package server

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"math"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

// defaultIdleCompactionMinTokens is the estimated context size below which an
// idle compaction turn is not worth its cost.
const defaultIdleCompactionMinTokens = 50_000

func (s *Server) idleCompactionMinTokensOrDefault() int {
	if s.idleCompactionMinTokens > 0 {
		return s.idleCompactionMinTokens
	}
	return defaultIdleCompactionMinTokens
}

// maxIdleCompactionMinutes keeps the minute settings within time.Duration.
const maxIdleCompactionMinutes = int(math.MaxInt64 / time.Minute)

// idleNudgePrompt starts an idle compaction turn; %d is the idle minutes.
//
//go:embed idle_nudge_prompt.txt
var idleNudgePrompt string

// cancelIdleCompactionLocked cancels all idle work: compaction and cache
// warming. Activity, settings changes, and compaction all go through here.
func (cm *ConversationManager) cancelIdleCompactionLocked() {
	cm.idleCompactionVersion++
	cm.idleCompacting = false
	if cm.idleCompactionTimer != nil {
		cm.idleCompactionTimer.Stop()
		cm.idleCompactionTimer = nil
	}
	if cm.cacheWarmTimer != nil {
		cm.cacheWarmTimer.Stop()
		cm.cacheWarmTimer = nil
	}
}

func (cm *ConversationManager) cancelIdleCompaction() {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cancelIdleCompactionLocked()
}

// The copied profile policy applies only to top-level conversations with
// compact_in_place, which -no-tools can turn off. Children never schedule
// their own background compaction.
func (cm *ConversationManager) idleCompactionEnabledLocked() bool {
	opts := cm.conversationOptions
	return cm.role == roleTopLevel && opts.IdleCompactionMinutes > 0 &&
		claudetool.IsToolEnabled(claudetool.CompactInPlaceName, opts.ToolOverrides, opts.DisableAllTools)
}

func (cm *ConversationManager) idleWorkBlockedLocked() bool {
	return cm.agentWorking || cm.cancelling || cm.loopTearingDown || cm.changingSettings
}

// scheduleIdleWork arms the idle policies after a successful turn: one
// compaction attempt, and cache refreshes until then or the keep-warm window
// ends, whichever is first: compaction replaces the context the cache holds.
func (cm *ConversationManager) scheduleIdleWork(s *Server) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.role != roleTopLevel || cm.idleWorkBlockedLocked() {
		return
	}
	if cm.idleCompacting {
		cm.idleCompacting = false
		return
	}
	cm.cancelIdleCompactionLocked()
	version := cm.idleCompactionVersion
	opts := cm.conversationOptions
	warmFor := time.Duration(opts.CacheKeepWarmMinutes) * time.Minute
	if cm.idleCompactionEnabledLocked() {
		compactAfter := time.Duration(opts.IdleCompactionMinutes) * time.Minute
		if compactAfter < warmFor {
			warmFor = compactAfter
		}
		cm.idleCompactionTimer = time.AfterFunc(compactAfter, func() {
			if err := s.compactIdleConversation(cm, version); err != nil {
				cm.logger.Error("idle compaction failed", "error", err)
			}
		})
	}
	now := time.Now()
	cm.scheduleCacheWarmLocked(s, version, now.Add(warmFor), now)
}

// scheduleCacheWarmLocked arms a refresh shortly before the cache last
// refreshed at refreshed expires, if that happens before warmUntil. A provider
// whose cache outlives the window, or that cannot warm it, is never called.
func (cm *ConversationManager) scheduleCacheWarmLocked(s *Server, version uint64, warmUntil, refreshed time.Time) {
	ttl := llm.CacheTTL(cm.cacheWarmService)
	if ttl == 0 || cm.cacheWarmRequest == nil || !refreshed.Add(ttl).Before(warmUntil) {
		return
	}
	cm.cacheWarmTimer = time.AfterFunc(time.Until(refreshed.Add(ttl-ttl/5)), func() {
		if err := cm.warmCache(s, version, warmUntil); err != nil {
			cm.logger.Error("prompt cache warm failed", "error", err)
		}
	})
}

// warmCache replays the last successful request without generating output,
// records its usage, then arms the next refresh. A failed or unsupported
// refresh ends the sequence.
func (cm *ConversationManager) warmCache(s *Server, version uint64, warmUntil time.Time) error {
	cm.mu.Lock()
	if cm.idleCompactionVersion != version || cm.idleWorkBlockedLocked() {
		cm.mu.Unlock()
		return nil
	}
	req, service := cm.cacheWarmRequest, cm.cacheWarmService
	modelID, revision := cm.cacheWarmModelID, cm.cacheWarmRevision
	cm.mu.Unlock()
	if current, err := cm.cacheWarmConnectionCurrent(modelID, revision); err != nil || !current {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	refreshed := time.Now()
	usage, err := llm.WarmCache(ctx, service, req)
	if errors.Is(err, llm.ErrCacheWarmUnsupported) {
		cm.logger.Info("prompt cache warming skipped", "reason", err)
		return nil
	}
	if err != nil {
		return err
	}
	marker, err := s.db.CreateUsageMessage(ctx, cm.conversationID, []llm.PurposedUsage{{Purpose: "cache_warm", Usage: usage}})
	if err != nil {
		return err
	}
	s.notifySubscribers(ctx, cm.conversationID, *marker)
	if current, err := cm.cacheWarmConnectionCurrent(modelID, revision); err != nil || !current {
		return err
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.idleCompactionVersion == version {
		cm.scheduleCacheWarmLocked(s, version, warmUntil, refreshed)
	}
	return nil
}

func (cm *ConversationManager) cacheWarmConnectionCurrent(modelID string, revision uint64) (bool, error) {
	if cm.resolveModelService == nil {
		return true, nil
	}
	_, _, current, err := cm.resolveModelService(modelID)
	return current == revision, err
}

// compactIdleConversation makes a one-shot attempt: a turn, started by an
// idle nudge, in which the agent compacts its own context in place with
// compact_in_place. The turn's end does not re-arm idle work (see
// idleCompacting); only a later turn does.
func (s *Server) compactIdleConversation(cm *ConversationManager, version uint64) error {
	ctx := context.Background()
	cm.loopLifecycleMu.Lock()
	defer cm.loopLifecycleMu.Unlock()
	claimed, err := s.claimIdleCompaction(ctx, cm, version)
	if err != nil || !claimed {
		return err
	}
	modelID := cm.GetModel()
	svc, err := s.llmManager.GetService(modelID)
	if err != nil {
		return fmt.Errorf("get LLM service %q: %w", modelID, err)
	}
	if err := cm.ensureLoopLocked(svc, modelID); err != nil {
		return err
	}
	cm.syncAgentWorking(true)
	nudge, err := cm.recordIdleNudge(ctx)
	if err != nil {
		cm.SetAgentWorking(false)
		return err
	}
	cm.mu.Lock()
	cm.idleCompacting = true
	cm.profileTurn = false
	cm.hasConversationEvents = true
	l := cm.loop
	cm.mu.Unlock()
	l.QueueMessages(nudge)
	return nil
}

// recordIdleNudge records the message that starts an idle compaction turn: the
// guidance and the compact_in_place index. Like a context nudge, compacting
// hides it.
func (cm *ConversationManager) recordIdleNudge(ctx context.Context) (llm.Message, error) {
	index, err := cm.contextIndex(ctx)
	if err != nil {
		return llm.Message{}, fmt.Errorf("index for idle nudge: %w", err)
	}
	cm.mu.Lock()
	opts := cm.conversationOptions
	cm.mu.Unlock()
	message := llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{
		{Type: llm.ContentTypeText, Text: idleNudgeText(opts)},
		{Type: llm.ContentTypeText, Text: index},
	}}
	created, err := cm.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: cm.conversationID,
		Type:           db.MessageTypeUser,
		LLMData:        message,
		UserData:       map[string]any{"context_nudge": true, "idle": true},
		UsageData:      llm.Usage{},
		MarkAgentStart: true,
	})
	if err != nil {
		return message, fmt.Errorf("record idle nudge: %w", err)
	}
	return message, cm.publishCreated(ctx, *created)
}

// idleNudgeText is the text of an idle nudge: the mechanics, then the
// conversation's guidance on what to keep.
func idleNudgeText(opts db.ConversationOptions) string {
	text := fmt.Sprintf(idleNudgePrompt, opts.IdleCompactionMinutes)
	if opts.IdleCompactionPrompt != "" {
		text += "\n\n" + opts.IdleCompactionPrompt
	}
	return text
}

// claimIdleCompaction reports whether the conversation is still idle, has a
// context the agent can compact, and nothing else is going on: no queued,
// delegated or live-call work.
func (s *Server) claimIdleCompaction(ctx context.Context, cm *ConversationManager, version uint64) (bool, error) {
	s.mu.Lock()
	current := s.activeConversations[cm.conversationID] == cm && !s.deletingConversations[cm.conversationID]
	s.mu.Unlock()
	if !current {
		return false, nil
	}
	cm.mu.Lock()
	eligible := cm.idleCompactionClaimableLocked(version)
	opts := cm.conversationOptions
	cm.mu.Unlock()
	if !eligible {
		return false, nil
	}
	if !claudetool.IsToolEnabled(claudetool.CompactInPlaceName, opts.ToolOverrides, opts.DisableAllTools) {
		return false, fmt.Errorf("idle compaction needs %s, which is off", claudetool.CompactInPlaceName)
	}
	conversation, err := s.db.GetConversationByID(ctx, cm.conversationID)
	if err != nil {
		return false, err
	}
	if conversation.Archived || conversation.IsDraft || conversation.TurnInterrupted || conversation.AgentWorking ||
		len(db.ParseQueuedMessages(conversation.QueuedMessages)) > 0 {
		return false, nil
	}
	delegated, err := s.hasActiveDelegatedWork(ctx, cm.conversationID)
	if err != nil || delegated {
		return false, err
	}
	s.liveCalls.mu.Lock()
	live := false
	for _, call := range s.liveCalls.calls {
		if call.conversationID == cm.conversationID {
			select {
			case <-call.done:
			default:
				live = true
			}
		}
	}
	s.liveCalls.mu.Unlock()
	if live {
		return false, nil
	}
	// A small context is not worth a turn, and everything in the recent part
	// is kept as is: nothing to compact.
	items, _, err := cm.loadContext(ctx)
	if err != nil {
		return false, err
	}
	view, err := compactionView(items)
	if err != nil {
		return false, err
	}
	if tc := cm.tokenCounter(); tc.items(view) < cm.idleCompactionMinTokens || recentStart(view, cm.keepRecentTokens, tc) == 0 {
		return false, nil
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if !cm.idleCompactionClaimableLocked(version) {
		return false, nil
	}
	cm.cancelIdleCompactionLocked()
	return true, nil
}

// idleCompactionClaimableLocked reports whether the idle compaction armed as
// version is still wanted and nothing else is going on in the manager.
func (cm *ConversationManager) idleCompactionClaimableLocked(version uint64) bool {
	return cm.idleCompactionEnabledLocked() && cm.idleCompactionVersion == version && !cm.idleWorkBlockedLocked() && !cm.draining
}
