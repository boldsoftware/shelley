package db

import (
	"errors"
	"testing"
)

func TestCommitIdentities(t *testing.T) {
	t.Parallel()
	database, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := t.Context()
	identity := func(conversationID string) CommitIdentity {
		t.Helper()
		c, err := database.ConversationCommitIdentity(ctx, conversationID)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if _, err := database.CreateProject(ctx, Project{Name: "hub"}, ""); err != nil {
		t.Fatal(err)
	}
	task, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{Profile: "hub"})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := database.CreateSubagentConversation(ctx, "helper", task.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// None is set: none is invented.
	if got := identity(task.ConversationID); !got.IsZero() {
		t.Fatalf("no identity set, got %v", got)
	}

	ada := CommitIdentity{Name: " Ada Lovelace ", Email: "ada@example.com "}
	if err := database.SetDefaultCommitIdentity(ctx, ada); err != nil {
		t.Fatal(err)
	}
	want := CommitIdentity{Name: "Ada Lovelace", Email: "ada@example.com"}
	for _, id := range []string{task.ConversationID, sub.ConversationID, other.ConversationID} {
		if got := identity(id); got != want {
			t.Fatalf("default for %s: %v", id, got)
		}
	}

	// A project's own identity is its tasks' and their subagents', kept
	// across an update without it and a rename.
	grace := CommitIdentity{Name: "Grace Hopper", Email: "grace@example.com"}
	if err := database.UpdateProject(ctx, "hub", Project{Name: "hub", CommitIdentity: &grace}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateProject(ctx, "hub", Project{Name: "Hub"}); err != nil {
		t.Fatal(err)
	}
	project, err := database.GetProject(ctx, "Hub")
	if err != nil || *project.CommitIdentity != grace {
		t.Fatalf("project identity %v, %v", project.CommitIdentity, err)
	}
	for _, id := range []string{task.ConversationID, sub.ConversationID} {
		if got := identity(id); got != grace {
			t.Fatalf("project identity for %s: %v", id, got)
		}
	}
	if got := identity(other.ConversationID); got != want {
		t.Fatalf("other conversation %v", got)
	}

	// The zero value goes back to the default.
	if err := database.UpdateProject(ctx, "Hub", Project{Name: "Hub", CommitIdentity: &CommitIdentity{}}); err != nil {
		t.Fatal(err)
	}
	if got := identity(task.ConversationID); got != want {
		t.Fatalf("cleared project identity %v", got)
	}

	for _, bad := range []CommitIdentity{
		{Name: "Ada"},
		{Email: "ada@example.com"},
		{Name: "Ada", Email: "ada"},
		{Name: "Ada <a@b.c>", Email: "a@b.c"},
		{Name: "Ada", Email: "a da@b.c"},
		{Name: "Ada\n", Email: "@b.c"},
	} {
		if err := database.SetDefaultCommitIdentity(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", bad, err)
		}
		if err := database.UpdateProject(ctx, "Hub", Project{Name: "Hub", CommitIdentity: &bad}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("project %+v: %v", bad, err)
		}
	}
	if err := database.SetDefaultCommitIdentity(ctx, CommitIdentity{}); err != nil {
		t.Fatal(err)
	}
	if got := identity(other.ConversationID); !got.IsZero() {
		t.Fatalf("removed default %v", got)
	}
}
