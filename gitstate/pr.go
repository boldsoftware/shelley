package gitstate

import (
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// PRInfo holds GitHub pull request information for a branch.
type PRInfo struct {
	Number           int    `json:"number"`
	Title            string `json:"title"`
	State            string `json:"state"` // OPEN, CLOSED, MERGED
	URL              string `json:"url"`
	IsDraft          bool   `json:"is_draft"`
	MergeStateStatus string `json:"merge_state_status"` // BEHIND, BLOCKED, CLEAN, DIRTY, DRAFT, HAS_HOOKS, QUEUED, UNKNOWN, UNSTABLE
	AutoMerge        bool   `json:"auto_merge"`
	ReviewDecision   string `json:"review_decision"` // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED, ""
	InMergeQueue     bool   `json:"in_merge_queue"`
}

type prCacheEntry struct {
	info      *PRInfo
	fetchedAt time.Time
}

// PRCache caches GitHub PR lookups to avoid excessive `gh` calls.
// The cache is populated by batch RefreshRepos calls and read by GetPRInfo.
type PRCache struct {
	mu      sync.Mutex
	entries map[string]prCacheEntry // key: "repoRoot\x00branch"
	// repoFetchedAt tracks when we last fetched all PRs for a repo.
	repoFetchedAt map[string]time.Time
	ttl           time.Duration
}

var (
	globalPRCache     *PRCache
	globalPRCacheOnce sync.Once
)

// GetPRCache returns the singleton PR cache.
func GetPRCache() *PRCache {
	globalPRCacheOnce.Do(func() {
		globalPRCache = &PRCache{
			entries:       make(map[string]prCacheEntry),
			repoFetchedAt: make(map[string]time.Time),
			ttl:           60 * time.Second,
		}
	})
	return globalPRCache
}

func prCacheKey(repoRoot, branch string) string {
	return repoRoot + "\x00" + branch
}

// GetPRInfo returns cached PR info. Never blocks on network calls.
// Returns nil if no cached data or if the branch has no PR.
func (c *PRCache) GetPRInfo(repoRoot, branch string) *PRInfo {
	if repoRoot == "" || branch == "" {
		return nil
	}
	c.mu.Lock()
	entry, ok := c.entries[prCacheKey(repoRoot, branch)]
	c.mu.Unlock()
	if ok {
		return entry.info
	}
	return nil
}

// getCached is an alias for GetPRInfo (used in tests).
func (c *PRCache) getCached(repoRoot, branch string) *PRInfo {
	return c.GetPRInfo(repoRoot, branch)
}

// Invalidate removes the cache entry for a repo/branch.
func (c *PRCache) Invalidate(repoRoot, branch string) {
	c.mu.Lock()
	delete(c.entries, prCacheKey(repoRoot, branch))
	c.mu.Unlock()
}

// InvalidateAll clears all repo fetch timestamps, forcing the next
// RefreshRepos call to actually fetch.
func (c *PRCache) InvalidateAll() {
	c.mu.Lock()
	clear(c.repoFetchedAt)
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
	if p.InMergeQueue {
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

// repoRemoteURL returns the origin remote URL for a repo/worktree path.
// Used to deduplicate fetches across worktrees of the same repo.
func repoRemoteURL(repoRoot string) string {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return repoRoot // fall back to path
	}
	return strings.TrimSpace(string(out))
}

// RefreshRepos fetches all PRs for the given repos in the background (one gh
// call per unique remote) and populates the cache. Only fetches repos whose
// cache has expired. When done is non-nil it is called once all fetches
// complete. The call never blocks the caller.
func (c *PRCache) RefreshRepos(branches map[string]map[string]bool, done func()) {
	// Find which repos need refreshing.
	c.mu.Lock()
	var stale []string
	for repo := range branches {
		if t, ok := c.repoFetchedAt[repo]; !ok || time.Since(t) >= c.ttl {
			stale = append(stale, repo)
		}
	}
	c.mu.Unlock()

	if len(stale) == 0 {
		if done != nil {
			done()
		}
		return
	}

	go func() {
		// Group worktrees by remote URL so we fetch once per actual repo.
		type remoteGroup struct {
			worktree string            // any worktree path (used to run gh commands)
			repos    []string            // all worktree paths sharing this remote
		}
		byRemote := make(map[string]*remoteGroup)
		for _, repo := range stale {
			url := repoRemoteURL(repo)
			if g, ok := byRemote[url]; ok {
				g.repos = append(g.repos, repo)
			} else {
				byRemote[url] = &remoteGroup{worktree: repo, repos: []string{repo}}
			}
		}

		// Fetch in parallel, one goroutine per unique remote.
		type result struct {
			group *remoteGroup
			prs   map[string]*PRInfo // branch -> PRInfo
		}
		ch := make(chan result, len(byRemote))
		for _, g := range byRemote {
			go func(g *remoteGroup) {
				ch <- result{group: g, prs: fetchRepoPRs(g.worktree)}
			}(g)
		}

		now := time.Now()
		for range byRemote {
			r := <-ch
			c.mu.Lock()
			// Apply results to all worktrees sharing this remote.
			for _, repo := range r.group.repos {
				c.repoFetchedAt[repo] = now
				for branch := range branches[repo] {
					key := prCacheKey(repo, branch)
					c.entries[key] = prCacheEntry{info: r.prs[branch], fetchedAt: now}
				}
			}
			c.mu.Unlock()
		}

		if done != nil {
			done()
		}
	}()
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
	HeadRefName      string           `json:"headRefName"`
}

func toPRInfo(resp ghPRResponse) *PRInfo {
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

const prListFields = "number,title,state,url,isDraft,mergeStateStatus,autoMergeRequest,reviewDecision,headRefName"

// fetchRepoPRs fetches PR info for a repo: all open PRs plus recently
// closed/merged (last 7 days). Returns a map of branch name -> PRInfo.
func fetchRepoPRs(repoRoot string) map[string]*PRInfo {
	cutoff := time.Now().AddDate(0, 0, -7).Format("2006-01-02")

	// Fetch open and recently closed in parallel.
	type batch struct {
		results []ghPRResponse
	}
	ch := make(chan batch, 2)
	go func() { ch <- batch{ghPRList(repoRoot, "--state", "open", "--limit", "500")} }()
	go func() {
		ch <- batch{ghPRList(repoRoot, "--state", "closed", "--search", "closed:>"+cutoff, "--limit", "500")}
	}()

	prs := make(map[string]*PRInfo)
	for range 2 {
		b := <-ch
		for _, resp := range b.results {
			if _, exists := prs[resp.HeadRefName]; !exists {
				prs[resp.HeadRefName] = toPRInfo(resp)
			}
		}
	}

	// Check merge queue.
	queued := fetchMergeQueue(repoRoot)
	for branch := range queued {
		if pr, ok := prs[branch]; ok {
			pr.InMergeQueue = true
		}
	}

	return prs
}

func ghPRList(repoRoot string, extra ...string) []ghPRResponse {
	args := []string{"pr", "list", "--json", prListFields}
	args = append(args, extra...)
	cmd := exec.Command("gh", args...)
	cmd.Dir = repoRoot
	output, err := cmd.Output()
	if err != nil {
		return nil
	}
	var results []ghPRResponse
	if err := json.Unmarshal(output, &results); err != nil {
		return nil
	}
	return results
}

// fetchMergeQueue returns the set of branch names currently in the repo's merge queue.
func fetchMergeQueue(repoRoot string) map[string]bool {
	cmd := exec.Command("gh", "api", "graphql",
		"-F", "owner={owner}", "-F", "name={repo}",
		"-f", `query=query($owner: String!, $name: String!) {
  repository(owner: $owner, name: $name) {
    mergeQueue {
      entries(first: 100) {
        nodes { pullRequest { headRefName } }
      }
    }
  }
}`)
	cmd.Dir = repoRoot
	output, err := cmd.Output()
	if err != nil {
		return nil
	}
	var resp struct {
		Data struct {
			Repository struct {
				MergeQueue struct {
					Entries struct {
						Nodes []struct {
							PullRequest struct {
								HeadRefName string `json:"headRefName"`
							} `json:"pullRequest"`
						} `json:"nodes"`
					} `json:"entries"`
				} `json:"mergeQueue"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(output, &resp); err != nil {
		return nil
	}
	branches := make(map[string]bool)
	for _, node := range resp.Data.Repository.MergeQueue.Entries.Nodes {
		branches[node.PullRequest.HeadRefName] = true
	}
	return branches
}
