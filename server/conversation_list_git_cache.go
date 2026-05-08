package server

import (
	"sync"
	"time"

	"shelley.exe.dev/gitstate"
)

const conversationListGitCacheTTL = 5 * time.Minute

type conversationListGitCacheEntry struct {
	state     *gitstate.GitState
	worktree  string
	expiresAt time.Time
}

type conversationListGitCache struct {
	mu      sync.Mutex
	entries map[string]conversationListGitCacheEntry
}

func newConversationListGitCache() *conversationListGitCache {
	return &conversationListGitCache{entries: make(map[string]conversationListGitCacheEntry)}
}

func (c *conversationListGitCache) get(cwd string, now time.Time) (conversationListGitCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[cwd]
	if !ok {
		return conversationListGitCacheEntry{}, false
	}
	if !now.Before(entry.expiresAt) {
		delete(c.entries, cwd)
		return conversationListGitCacheEntry{}, false
	}
	return entry, true
}

func (c *conversationListGitCache) set(cwd string, entry conversationListGitCacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[cwd] = entry
}

func (c *conversationListGitCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]conversationListGitCacheEntry)
}
