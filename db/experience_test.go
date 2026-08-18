package db

import (
	"context"
	"encoding/json"
	"testing"
)

func TestExperienceMemoryProjectIsolationAndSearch(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()
	conversation, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}

	for _, draft := range []MemoryDraft{
		{Scope: "project", ProjectPath: "/repo/a", Kind: "decision", Title: "Database", Content: "Use SQLite WAL mode", Confidence: .9},
		{Scope: "project", ProjectPath: "/repo/b", Kind: "decision", Title: "Database", Content: "Use PostgreSQL", Confidence: .9},
		{Scope: "global", Kind: "preference", Title: "Tests", Content: "Always run focused tests", Confidence: 1},
	} {
		if _, err := database.SaveMemory(ctx, conversation.ConversationID, draft); err != nil {
			t.Fatal(err)
		}
	}

	items, err := database.SearchMemories(ctx, "/repo/a", "Database SQLite Tests", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d memories, want project and global memories: %#v", len(items), items)
	}
	for _, item := range items {
		if item.ProjectPath == "/repo/b" {
			t.Fatal("memory from another project leaked into results")
		}
	}
}

func TestTaskCheckpointLatestIsRecoverable(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()
	conversation, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := database.AppendTaskCheckpoint(ctx, conversation.ConversationID, "checkpoint", "first", json.RawMessage(`{"next":"build"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.AppendTaskCheckpoint(ctx, conversation.ConversationID, "checkpoint", "verified", json.RawMessage(`{"next":"ship"}`)); err != nil {
		t.Fatal(err)
	}
	latest, err := database.LatestTaskCheckpoint(ctx, conversation.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil || latest.Summary != "verified" || string(latest.State) != `{"next":"ship"}` {
		t.Fatalf("unexpected latest checkpoint: %#v", latest)
	}
}

func TestDreamConsolidationIsAuditedAndDeduplicated(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()
	conversation, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`[{"scope":"project","kind":"lesson","title":"Verify","content":"Run tests","confidence":0.9}]`)
	for range 2 {
		if _, err := database.ConsolidateDreamJSON(ctx, conversation.ConversationID, "/repo", "done", raw); err != nil {
			t.Fatal(err)
		}
	}
	items, err := database.SearchMemories(ctx, "/repo", "Verify", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d duplicate memories, want 1", len(items))
	}
	var runs int
	if err := database.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		return rx.QueryRow("SELECT count(*) FROM dream_runs WHERE conversation_id = ?", conversation.ConversationID).Scan(&runs)
	}); err != nil {
		t.Fatal(err)
	}
	if runs != 2 {
		t.Fatalf("got %d dream audit rows, want 2", runs)
	}
}
