package server

import (
	"fmt"
	"os/exec"
	"reflect"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/gitstate"
)

func TestConversationListGitCacheTTL(t *testing.T) {
	cache := newConversationListGitCache()
	now := time.Now()
	cache.set("/repo", conversationListGitCacheEntry{
		state:     &gitstate.GitState{IsRepo: true, Worktree: "/repo"},
		expiresAt: now.Add(time.Minute),
	})
	if _, ok := cache.get("/repo", now); !ok {
		t.Fatalf("expected cache hit before expiry")
	}
	if _, ok := cache.get("/repo", now.Add(time.Minute)); ok {
		t.Fatalf("expected cache miss at expiry")
	}
}

func TestConversationListGitCacheClear(t *testing.T) {
	cache := newConversationListGitCache()
	now := time.Now()
	cache.set("/repo", conversationListGitCacheEntry{
		state:     &gitstate.GitState{IsRepo: true, Worktree: "/repo"},
		expiresAt: now.Add(time.Minute),
	})
	cache.clear()
	if _, ok := cache.get("/repo", now); ok {
		t.Fatalf("expected miss after clear")
	}
}

func TestConversationListGitCacheSurvivesTxCommit(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	now := time.Now()

	// Cache entries must survive DB commits — otherwise every recorded
	// message would re-shell to git for every conversation in the list.
	server.conversationListGitCache.set("/repo", conversationListGitCacheEntry{
		state:     &gitstate.GitState{IsRepo: true, Worktree: "/repo"},
		expiresAt: now.Add(time.Minute),
	})
	conv, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := server.conversationListGitCache.get("/repo", now); !ok {
		t.Fatalf("expected cache to survive Tx commit")
	}
	if err := database.SetConversationAgentWorking(t.Context(), conv.ConversationID, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := server.conversationListGitCache.get("/repo", now); !ok {
		t.Fatalf("expected cache to survive agent_working Tx commit")
	}
}

func TestConversationListWithStateUsesCachedGitInfo(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	cwd := t.TempDir()
	if _, err := database.CreateConversation(t.Context(), nil, true, &cwd, nil, db.ConversationOptions{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	server.conversationListGitCache.set(cwd, conversationListGitCacheEntry{
		state: &gitstate.GitState{
			IsRepo:   true,
			Worktree: cwd,
			Commit:   "cached",
			Subject:  "cached subject",
		},
		worktree:  "/main",
		expiresAt: now.Add(time.Minute),
	})

	list, err := server.conversationListWithState(t.Context(), 5000, 0, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected one conversation, got %d", len(list))
	}
	if list[0].GitCommit != "cached" || list[0].GitWorktreeRoot != "/main" {
		t.Fatalf("expected cached git info, got %+v", list[0])
	}
}

func TestConversationListGitCacheFingerprintInvalidates(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "-c", "user.email=a@b", "-c", "user.name=t", "-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-m", "one\n\nPrompt: test")

	gitDir, err := resolveGitDir(repo)
	if err != nil {
		t.Fatalf("resolveGitDir: %v", err)
	}
	fp1 := gitFingerprint(gitDir)

	cache := newConversationListGitCache()
	now := time.Now()
	cache.set(repo, conversationListGitCacheEntry{
		state:       &gitstate.GitState{IsRepo: true, Worktree: repo, Commit: "old"},
		expiresAt:   now.Add(time.Hour),
		gitDir:      gitDir,
		fingerprint: fp1,
	})
	if _, ok := cache.get(repo, now); !ok {
		t.Fatalf("expected cache hit before commit")
	}

	// New commit must invalidate the cached fingerprint.
	runGit(t, repo, "-c", "user.email=a@b", "-c", "user.name=t", "-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-m", "two\n\nPrompt: test")
	if gitFingerprint(gitDir) == fp1 {
		t.Fatalf("expected fingerprint to change after commit")
	}
	if _, ok := cache.get(repo, now); ok {
		t.Fatalf("expected cache miss after commit")
	}
}

func TestDecorateConversationsMixedCwds(t *testing.T) {
	repo, otherRepo, nonRepo := t.TempDir(), t.TempDir(), t.TempDir()
	for i, dir := range []string{repo, otherRepo} {
		runGit(t, dir, "init", "-q")
		runGit(t, dir, "-c", "user.email=a@b", "-c", "user.name=t", "-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-m", fmt.Sprintf("repo %d", i))
	}
	t.Chdir(repo) // Empty cwd resolves to this test-owned regular repository.
	empty, alias, parent := "", repo+"/.", "parent"
	// Mix repos/nonrepos, repeated rows, nil/empty cwd, and an exact-path alias.
	cwds := []*string{&repo, &nonRepo, &otherRepo, &repo, nil, &empty, &nonRepo, &alias}
	items := make([]db.ConversationListItem, len(cwds))
	for i, cwd := range cwds {
		items[i] = db.ConversationListItem{
			Conversation: generated.Conversation{
				ConversationID:       fmt.Sprintf("conversation-%d", i),
				Cwd:                  cwd,
				AgentWorking:         i%2 == 0,
				ParentConversationID: &parent,
			},
			Preview:               fmt.Sprintf("preview %d", i),
			MaxSequenceID:         int64(i),
			SubagentCount:         int64(i + 1),
			RunningBackgroundJobs: int64(i % 3),
		}
	}
	server := &Server{conversationListGitCache: newConversationListGitCache()}
	result := server.decorateConversations(items)
	if len(result) != len(items) {
		t.Fatalf("got %d rows, want %d", len(result), len(items))
	}
	for i, row := range result {
		if !reflect.DeepEqual(row.ConversationListItem, items[i]) || row.Working != items[i].AgentWorking {
			t.Fatalf("row %d lost its fields or order: %+v", i, row)
		}
		state := &gitstate.GitState{}
		if cwds[i] != nil {
			state = gitstate.GetGitState(*cwds[i])
		}
		if row.GitRepoRoot != state.Worktree || row.GitCommit != state.Commit || row.GitSubject != state.Subject || row.GitWorktreeRoot != "" {
			t.Fatalf("row %d has incorrect git metadata: %+v, state %+v", i, row, state)
		}
	}
	// Empty and aliased cwds remain separate exact keys; nil has no lookup.
	if got := len(server.conversationListGitCache.entries); got != 5 {
		t.Fatalf("got %d cache keys, want 5", got)
	}
	expiries := make(map[string]time.Time)
	for _, cwd := range []string{repo, otherRepo, nonRepo, empty, alias} {
		entry, ok := server.conversationListGitCache.entries[cwd]
		if !ok {
			t.Fatalf("missing exact cwd key %q", cwd)
		}
		expiries[cwd] = entry.expiresAt
	}
	server.decorateConversations(items)
	for cwd, expiry := range expiries {
		if !server.conversationListGitCache.entries[cwd].expiresAt.Equal(expiry) {
			t.Fatalf("reuse extended expiry for %q", cwd)
		}
	}
}

func TestDecorateConversationsRevalidatesNextCall(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "-c", "user.email=a@b", "-c", "user.name=t", "-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-m", "one")
	items := []db.ConversationListItem{
		{Conversation: generated.Conversation{ConversationID: "first", Cwd: &repo}},
		{Conversation: generated.Conversation{ConversationID: "second", Cwd: &repo}},
	}
	server := &Server{conversationListGitCache: newConversationListGitCache()}
	before := server.decorateConversations(items)
	runGit(t, repo, "-c", "user.email=a@b", "-c", "user.name=t", "-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-m", "two")
	after := server.decorateConversations(items)
	state := gitstate.GetGitState(repo)
	for i := range items {
		if before[i].GitSubject != "one" || after[i].GitSubject != "two" ||
			after[i].GitCommit != state.Commit || after[i].GitCommit == before[i].GitCommit {
			t.Fatalf("row %d did not refresh: before %+v, after %+v", i, before[i], after[i])
		}
	}
}

func BenchmarkDecorateConversationsRepeatedCwd(b *testing.B) {
	repo := b.TempDir()
	runGit(b, repo, "init", "-q")
	runGit(b, repo, "-c", "user.email=a@b", "-c", "user.name=t", "-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-m", "benchmark")
	runGit(b, repo, "pack-refs", "--all")
	for _, count := range []int{1, 16} {
		b.Run(fmt.Sprintf("rows=%d", count), func(b *testing.B) {
			server := &Server{conversationListGitCache: newConversationListGitCache()}
			items := make([]db.ConversationListItem, count)
			for i := range items {
				items[i].Cwd = &repo
			}
			server.decorateConversations(items) // Warm the real packed-ref cache entry.
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				server.decorateConversations(items)
			}
		})
	}
}

func runGit(t testing.TB, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
