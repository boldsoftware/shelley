package gitstate

import (
	"encoding/json"
	"os/exec"
	"sync"
	"time"
)

// PRInfo holds GitHub pull request information for a branch.
type PRInfo struct {
	Number          int    `json:"number"`
	Title           string `json:"title"`
	State           string `json:"state"` // OPEN, CLOSED, MERGED
	URL             string `json:"url"`
	IsDraft         bool   `json:"is_draft"`
	MergeStateStatus string `json:"merge_state_status"` // BEHIND, BLOCKED, CLEAN, DIRTY, DRAFT, HAS_HOOKS, UNKNOWN, UNSTABLE
	AutoMerge       bool   `json:"auto_merge"`
	ReviewDecision  string `json:"review_decision"` // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED, ""
}

type prCacheEntry struct {
	info      *PRInfo
	fetchedAt time.Time
}

// PRCache caches GitHub PR lookups to avoid excessive `gh` calls.
type PRCache struct {
	mu      sync.Mutex
	entries map[string]prCacheEntry // key: "repoRoot\x00branch"
	ttl     time.Duration
}

var (
	globalPRCache     *PRCache
	globalPRCacheOnce sync.Once
)

// GetPRCache returns the singleton PR cache.
func GetPRCache() *PRCache {
	globalPRCacheOnce.Do(func() {
		globalPRCache = &PRCache{
			entries: make(map[string]prCacheEntry),
			ttl:     60 * time.Second,
		}
	})
	return globalPRCache
}

func prCacheKey(repoRoot, branch string) string {
	return repoRoot + "\x00" + branch
}

// GetPRInfo returns cached PR info for the given repo/branch, fetching if stale.
// Returns nil if no PR exists, gh is not installed, or an error occurs.
func (c *PRCache) GetPRInfo(repoRoot, branch string) *PRInfo {
	if repoRoot == "" || branch == "" {
		return nil
	}

	key := prCacheKey(repoRoot, branch)

	c.mu.Lock()
	entry, ok := c.entries[key]
	c.mu.Unlock()

	if ok && time.Since(entry.fetchedAt) < c.ttl {
		return entry.info
	}

	// Fetch in the foreground (callers should consider doing this async)
	info := fetchPRInfo(repoRoot, branch)

	c.mu.Lock()
	c.entries[key] = prCacheEntry{info: info, fetchedAt: time.Now()}
	c.mu.Unlock()

	return info
}

// getCached returns the cached PR info if it exists and is not expired, nil otherwise.
func (c *PRCache) getCached(repoRoot, branch string) *PRInfo {
	c.mu.Lock()
	entry, ok := c.entries[prCacheKey(repoRoot, branch)]
	c.mu.Unlock()
	if ok && time.Since(entry.fetchedAt) < c.ttl {
		return entry.info
	}
	return nil
}

// Invalidate removes the cache entry for a repo/branch.
func (c *PRCache) Invalidate(repoRoot, branch string) {
	c.mu.Lock()
	delete(c.entries, prCacheKey(repoRoot, branch))
	c.mu.Unlock()
}

// StateLabel returns a human-readable label for the PR state.
func (p *PRInfo) StateLabel() string {
	if p.State == "MERGED" {
		return "merged"
	}
	if p.State == "CLOSED" {
		return "closed"
	}
	if p.IsDraft {
		return "draft"
	}
	if p.AutoMerge {
		return "merge queue"
	}
	if p.ReviewDecision == "APPROVED" {
		return "approved"
	}
	if p.ReviewDecision == "CHANGES_REQUESTED" {
		return "changes requested"
	}
	return "open"
}

type ghPRResponse struct {
	Number           int              `json:"number"`
	Title            string           `json:"title"`
	State            string           `json:"state"`
	URL              string           `json:"url"`
	IsDraft          bool             `json:"isDraft"`
	MergeStateStatus string           `json:"mergeStateStatus"`
	AutoMergeRequest *json.RawMessage `json:"autoMergeRequest"`
	ReviewDecision   string           `json:"reviewDecision"`
}

func fetchPRInfo(repoRoot, branch string) *PRInfo {
	cmd := exec.Command("gh", "pr", "list",
		"--head", branch,
		"--state", "all",
		"--json", "number,title,state,url,isDraft,mergeStateStatus,autoMergeRequest,reviewDecision",
		"--limit", "1")
	cmd.Dir = repoRoot

	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	var results []ghPRResponse
	if err := json.Unmarshal(output, &results); err != nil || len(results) == 0 {
		return nil
	}
	resp := results[0]

	return &PRInfo{
		Number:           resp.Number,
		Title:            resp.Title,
		State:            resp.State,
		URL:              resp.URL,
		IsDraft:          resp.IsDraft,
		MergeStateStatus: resp.MergeStateStatus,
		AutoMerge:        resp.AutoMergeRequest != nil && string(*resp.AutoMergeRequest) != "null",
		ReviewDecision:   resp.ReviewDecision,
	}
}
