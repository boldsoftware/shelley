package server

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"shelley.exe.dev/db"
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
	conv, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := server.conversationListGitCache.get("/repo", now); !ok {
		t.Fatalf("expected cache to survive Tx commit")
	}
	if err := database.SetConversationAgentWorking(context.Background(), conv.ConversationID, true); err != nil {
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
	if _, err := database.CreateConversation(context.Background(), nil, true, &cwd, nil, db.ConversationOptions{}); err != nil {
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

	list, err := server.conversationListWithState(context.Background(), 5000, 0, "", false)
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

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
