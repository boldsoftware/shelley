package gitstate

import (
	"testing"
	"time"
)

func TestPRCacheKey(t *testing.T) {
	key := prCacheKey("/repo", "main")
	if key != "/repo\x00main" {
		t.Fatalf("unexpected key: %q", key)
	}
}

func TestPRCacheGetPRInfo(t *testing.T) {
	cache := &PRCache{
		entries:       make(map[string]prCacheEntry),
		repoFetchedAt: make(map[string]time.Time),
		ttl:           50 * time.Millisecond,
	}

	// Manually insert a cache entry
	info := &PRInfo{Number: 42, Title: "test", State: "OPEN", URL: "https://example.com"}
	key := prCacheKey("/repo", "test-branch")
	cache.mu.Lock()
	cache.entries[key] = prCacheEntry{info: info, fetchedAt: time.Now()}
	cache.mu.Unlock()

	// Should return cached value
	result := cache.getCached("/repo", "test-branch")
	if result == nil || result.Number != 42 {
		t.Fatal("expected cached PR info")
	}

	// Data persists regardless of TTL (staleness is the refresh goroutine's concern)
	time.Sleep(60 * time.Millisecond)
	result = cache.getCached("/repo", "test-branch")
	if result == nil || result.Number != 42 {
		t.Fatal("expected cached PR info to persist")
	}

	// Empty repo/branch returns nil
	if cache.GetPRInfo("", "branch") != nil {
		t.Fatal("expected nil for empty repo")
	}
	if cache.GetPRInfo("/repo", "") != nil {
		t.Fatal("expected nil for empty branch")
	}
}

func TestPRCacheInvalidate(t *testing.T) {
	cache := &PRCache{
		entries:       make(map[string]prCacheEntry),
		repoFetchedAt: make(map[string]time.Time),
		ttl:           time.Hour,
	}

	key := prCacheKey("/repo", "branch")
	cache.mu.Lock()
	cache.entries[key] = prCacheEntry{info: &PRInfo{Number: 1}, fetchedAt: time.Now()}
	cache.mu.Unlock()

	cache.Invalidate("/repo", "branch")

	result := cache.getCached("/repo", "branch")
	if result != nil {
		t.Fatal("expected nil after invalidation")
	}
}

func TestPRStateLabel(t *testing.T) {
	tests := []struct {
		pr   PRInfo
		want string
	}{
		{PRInfo{State: "MERGED"}, "merged"},
		{PRInfo{State: "CLOSED"}, "closed"},
		{PRInfo{State: "OPEN", IsDraft: true}, "draft"},
		{PRInfo{State: "OPEN", InMergeQueue: true}, "merge queue"},
		{PRInfo{State: "OPEN", ReviewDecision: "APPROVED"}, "approved"},
		{PRInfo{State: "OPEN", ReviewDecision: "CHANGES_REQUESTED"}, "changes requested"},
		{PRInfo{State: "OPEN"}, "open"},
	}
	for _, tt := range tests {
		got := tt.pr.StateLabel()
		if got != tt.want {
			t.Errorf("StateLabel(%+v) = %q, want %q", tt.pr, got, tt.want)
		}
	}
}
