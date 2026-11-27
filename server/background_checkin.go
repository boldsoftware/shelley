package server

import (
	"context"
	"encoding/json"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
)

func (cm *ConversationManager) backgroundCheckInAllowed(ctx context.Context, jobID string) (bool, error) {
	id := cm.conversationID
	seen := make(map[string]bool)
	for id != "" {
		if seen[id] {
			return false, nil
		}
		seen[id] = true
		cm.server.mu.Lock()
		stopped := cm.server.stoppedParents[id]
		cm.server.mu.Unlock()
		if stopped {
			return false, nil
		}
		conversation, err := cm.db.GetConversationByID(ctx, id)
		if err != nil {
			return false, err
		}
		if conversation.Archived || conversation.TurnInterrupted {
			return false, nil
		}
		if conversation.ParentConversationID == nil {
			break
		}
		id = *conversation.ParentConversationID
	}
	var jobs []generated.BackgroundJob
	err := cm.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		jobs, err = q.ListRunningBackgroundJobs(ctx, cm.conversationID)
		return err
	})
	if err != nil {
		return false, err
	}
	for _, job := range jobs {
		if job.JobID == jobID {
			return true, nil
		}
	}
	return false, nil
}

// filterBackgroundCheckIns discards stale check-ins without affecting exit notices.
// The caller holds loopLifecycleMu.
func (cm *ConversationManager) filterBackgroundCheckIns(ctx context.Context, queued []db.QueuedMessage) ([]db.QueuedMessage, error) {
	var kept []db.QueuedMessage
	var discard []string
	for _, qm := range queued {
		var data backgroundJobUserData
		if json.Unmarshal(qm.UserData, &data) == nil && data.CheckIn {
			allowed, err := cm.backgroundCheckInAllowed(ctx, data.BackgroundJobID)
			if err != nil {
				return nil, err
			}
			if !allowed {
				discard = append(discard, qm.ID)
				continue
			}
		}
		kept = append(kept, qm)
	}
	if len(discard) != 0 {
		if _, err := cm.db.RemoveQueuedMessages(ctx, cm.conversationID, discard...); err != nil {
			return nil, err
		}
		cm.refreshUserWaiting(ctx)
		go cm.server.notifySubscribers(context.WithoutCancel(ctx), cm.conversationID)
	}
	return kept, nil
}
