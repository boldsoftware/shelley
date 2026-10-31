package server

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

// maxConcurrentResumes bounds how many interrupted turns we re-fire at once
// after an upgrade restart. Each resume hydrates a conversation and builds a
// tool set (browser, terminals), so a burst of them is expensive.
const maxConcurrentResumes = 4

// resumeWarningText is written to every resumed conversation. Resuming re-sends
// the request the old process was in the middle of, so a tool call whose result
// was never persisted runs a second time; the user has to be able to see that.
const resumeWarningText = "Shelley restarted to install a new binary while this turn was in flight. The turn has been resumed; any tool call whose result was not saved before the restart may run again."

// resumeInterruptedConversations re-fires the LLM request for each conversation
// (top-level or delegated subagent) that db.ConsumeResumeAfterUpgrade reported
// as mid-turn when the process exited to install an upgrade. Called once, after
// the server's listeners are up, so resumed loops see a usable server (port,
// subagent runner, streams).
func (s *Server) resumeInterruptedConversations(ctx context.Context, resumes []db.UpgradeResume) {
	if len(resumes) == 0 {
		return
	}
	conversationIDs := make([]string, len(resumes))
	for i, resume := range resumes {
		conversationIDs[i] = resume.ConversationID
	}
	s.logger.Info("resuming conversations interrupted by upgrade restart", "conversation_ids", conversationIDs)

	sem := make(chan struct{}, maxConcurrentResumes)
	var wg sync.WaitGroup
	for _, resume := range resumes {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := s.resumeConversation(ctx, resume); err != nil {
				s.logger.Error("Failed to resume conversation after upgrade restart", "conversationID", resume.ConversationID, "error", err)
			}
		}()
	}
	wg.Wait()
}

// resumeConversation claims and resumes one startup-versioned turn. A fresh
// user turn invalidates the token in its turn-start transaction, so a late
// worker becomes a no-op without touching newer work.
func (s *Server) resumeConversation(ctx context.Context, resume db.UpgradeResume) error {
	manager, err := s.getOrCreateConversationManager(ctx, resume.ConversationID, "")
	if err != nil {
		if _, recoverErr := s.db.MarkUpgradeResumeInterrupted(ctx, resume); recoverErr != nil {
			return errors.Join(fmt.Errorf("get conversation manager: %w", err), fmt.Errorf("preserve interrupted turn: %w", recoverErr))
		}
		return fmt.Errorf("get conversation manager: %w", err)
	}

	modelList := s.getModelList()
	defaultModelID := s.effectiveDefaultModel(modelList)
	serviceForModel := func(modelID string) (llm.Service, error) {
		service, err := s.llmManager.GetService(modelID)
		if err != nil {
			return nil, fmt.Errorf("get llm service for %s: %w", modelID, err)
		}
		return service, nil
	}
	if err := manager.ResumeInterruptedTurnAfterUpgrade(ctx, resume, defaultModelID, serviceForModel, resumeWarningText); err != nil {
		if errors.Is(err, errInterruptedTurnNotApplicable) || errors.Is(err, errProfileTurnStopped) {
			return nil
		}
		return err
	}
	return nil
}
