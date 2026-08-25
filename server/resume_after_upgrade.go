package server

import (
	"context"
	"fmt"
	"sync"
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
// that db.ConsumeResumeAfterUpgrade reported as mid-turn when the process exited
// to install an upgrade. Called once, after the server's listeners are up, so
// resumed loops see a usable server (port, subagent runner, streams).
func (s *Server) resumeInterruptedConversations(ctx context.Context, conversationIDs []string) {
	if len(conversationIDs) == 0 {
		return
	}
	s.logger.Info("resuming conversations interrupted by upgrade restart", "conversation_ids", conversationIDs)

	sem := make(chan struct{}, maxConcurrentResumes)
	var wg sync.WaitGroup
	for _, id := range conversationIDs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := s.resumeConversation(ctx, id); err != nil {
				s.logger.Error("Failed to resume conversation after upgrade restart", "conversationID", id, "error", err)
			}
		}()
	}
	wg.Wait()
}

// resumeConversation resumes one interrupted conversation, or skips it if it
// isn't safe to resume (see shouldResumeConversation).
func (s *Server) resumeConversation(ctx context.Context, conversationID string) error {
	resume, err := s.shouldResumeConversation(ctx, conversationID)
	if err != nil {
		return err
	}
	if !resume {
		// Not resuming: the flag would otherwise stay TRUE forever, since the
		// consume transaction deliberately left it alone.
		if err := s.db.SetConversationAgentWorking(ctx, conversationID, false); err != nil {
			return fmt.Errorf("clear agent_working: %w", err)
		}
		return nil
	}

	manager, err := s.getOrCreateConversationManager(ctx, conversationID, "")
	if err != nil {
		return fmt.Errorf("get conversation manager: %w", err)
	}

	modelID := manager.GetModel()
	if modelID == "" {
		modelID = s.effectiveDefaultModel(s.getModelList())
	}
	service, err := s.llmManager.GetService(modelID)
	if err != nil {
		return fmt.Errorf("get llm service for %s: %w", modelID, err)
	}

	// Record the warning before re-firing so it can't land in the middle of the
	// resumed turn's output.
	if err := manager.recordWarning(ctx, resumeWarningText); err != nil {
		return fmt.Errorf("record resume warning: %w", err)
	}

	return manager.ResumeInterruptedTurn(ctx, service, modelID)
}

// shouldResumeConversation gates the resume on the conversation's persisted
// state. We skip:
//
//   - Subagent conversations (parent_conversation_id set). The resumed parent
//     re-creates its subagents; resuming a subagent directly does not
//     re-register the parent's waiter (subagentWaitOwners), so the two runs
//     would diverge with one of them orphaned.
//   - Conversations whose latest actionable message is an assistant
//     end-of-turn. The turn actually finished and only the agent_working=false
//     write was lost; Retry() there would send a history ending in an assistant
//     message.
func (s *Server) shouldResumeConversation(ctx context.Context, conversationID string) (bool, error) {
	conv, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		return false, fmt.Errorf("load conversation: %w", err)
	}
	if !conv.AgentWorking {
		return false, nil
	}
	if conv.ParentConversationID != nil {
		s.logger.Info("Not resuming subagent conversation after upgrade restart", "conversationID", conversationID)
		return false, nil
	}
	latest, err := s.db.GetLatestActionableMessage(ctx, conversationID)
	if err != nil {
		return false, fmt.Errorf("load latest message: %w", err)
	}
	if isAgentEndOfTurn(latest) {
		s.logger.Info("Not resuming conversation whose turn already finished", "conversationID", conversationID)
		return false, nil
	}
	return true, nil
}
