package server

import (
	"context"
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

func TestConversationListGitCacheClearedOnListUpdateAndStateChange(t *testing.T) {
	t.Parallel()
	server, _, _ := newTestServer(t)
	now := time.Now()
	server.conversationListGitCache.set("/repo", conversationListGitCacheEntry{
		state:     &gitstate.GitState{IsRepo: true, Worktree: "/repo"},
		expiresAt: now.Add(time.Minute),
	})
	server.publishConversationListUpdate(ConversationListUpdate{Type: "update"})
	if _, ok := server.conversationListGitCache.get("/repo", now); ok {
		t.Fatalf("expected cache clear on conversation list update")
	}

	server.conversationListGitCache.set("/repo", conversationListGitCacheEntry{
		state:     &gitstate.GitState{IsRepo: true, Worktree: "/repo"},
		expiresAt: now.Add(time.Minute),
	})
	server.publishConversationState(ConversationState{ConversationID: "missing", Working: true})
	if _, ok := server.conversationListGitCache.get("/repo", now); ok {
		t.Fatalf("expected cache clear on conversation state change")
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
