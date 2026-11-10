package server

import (
	"context"
	"sync"
)

// replyTimesCache keeps each conversation's recent reply times for the
// sidebar's unread counts, keyed by the reply mark they were read at, so the
// list scans messages only for conversations with new replies.
type replyTimesCache struct {
	mu      sync.Mutex
	entries map[string]replyTimesEntry
}

type replyTimesEntry struct {
	mark  int64
	times []int64
}

// recentReplyTimes returns the recent reply times of each conversation in marks
// (conversation ID to its reply mark).
func (s *Server) recentReplyTimes(ctx context.Context, marks map[string]int64) (map[string][]int64, error) {
	c := &s.replyTimes
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]replyTimesEntry)
	}
	var stale []string
	for id, mark := range marks {
		if c.entries[id].mark != mark {
			stale = append(stale, id)
		}
	}
	if len(stale) > 0 {
		fresh, err := s.db.CustomConversationReplyTimes(ctx, stale)
		if err != nil {
			return nil, err
		}
		for _, id := range stale {
			c.entries[id] = replyTimesEntry{mark: marks[id], times: fresh[id]}
		}
	}
	result := make(map[string][]int64, len(marks))
	for id := range marks {
		result[id] = c.entries[id].times
	}
	return result, nil
}
