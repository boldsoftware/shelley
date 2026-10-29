package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"shelley.exe.dev/db/generated"
)

// newDBTestConversation creates a conversation in project: one labeled with its profile.
func newDBTestConversation(t *testing.T, database *DB, project, cwd string) string {
	t.Helper()
	AddTestProject(t, database, project)
	conversation, err := database.CreateConversation(t.Context(), nil, true, &cwd, nil, ConversationOptions{Profile: project})
	if err != nil {
		t.Fatal(err)
	}
	return conversation.ConversationID
}

func TestProjectLifecycleAndValidation(t *testing.T) {
	t.Parallel()
	database, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := t.Context()
	project, err := database.CreateProject(ctx, Project{Name: " Custom "}, "")
	if err != nil || project.Name != "Custom" {
		t.Fatalf("create project: %+v %v", project, err)
	}
	// A project is a profile, with a copy of the default profile's settings.
	var profile generated.Profile
	err = database.Queries(ctx, func(q *generated.Queries) (err error) {
		profile, err = q.GetProfile(ctx, "Custom")
		return err
	})
	if err != nil || profile.IsDefault || profile.Builtin {
		t.Fatalf("project profile: %+v %v", profile, err)
	}
	id := newDBTestConversation(t, database, project.Name, t.TempDir())
	if err := database.DeleteProject(ctx, project.Name); !errors.Is(err, ErrConflict) {
		t.Fatalf("deleted project with an active conversation: %v", err)
	}
	if _, err := database.ArchiveConversation(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := database.MoveConversation(ctx, id, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing project: %v", err)
	}
	// Renaming relabels its conversations.
	project.Name = "Renamed"
	if err := database.UpdateProject(ctx, "Custom", *project); err != nil {
		t.Fatal(err)
	}
	if c, err := database.GetWorkspaceConversation(ctx, id); err != nil || c.Project != "Renamed" || !c.Archived {
		t.Fatalf("renamed project's conversation: %+v %v", c, err)
	}
	AddTestProject(t, database, "my-project")
	for _, name := range []string{"Default", "Assistant", "my-project"} {
		if err := database.UpdateProject(ctx, "Renamed", Project{Name: name}); !errors.Is(err, ErrConflict) && !errors.Is(err, ErrInvalid) {
			t.Fatalf("renamed onto %s: %v", name, err)
		}
	}
	if err := database.MoveConversation(ctx, id, "my-project"); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteProject(ctx, "Renamed"); err != nil {
		t.Fatal(err)
	}
	if err := database.Queries(ctx, func(q *generated.Queries) error {
		_, err := q.GetProfile(ctx, "Renamed")
		return err
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("project's profile survived: %v", err)
	}
	conversation, err := database.GetConversationByID(ctx, id)
	if err != nil || !conversation.Archived {
		t.Fatalf("deletion changed archived conversation: %+v %v", conversation, err)
	}
	if err := database.UpdateProject(ctx, "missing", Project{Name: "Missing"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing update: %v", err)
	}
	for _, project := range []Project{
		{Name: " "},
		{Name: "a/b"},
		{Name: "Default"},
	} {
		if _, err := database.CreateProject(ctx, project, ""); !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrConflict) {
			t.Fatalf("accepted invalid project %+v: %v", project, err)
		}
	}
}

func TestMoveConversationPendingWorkGuards(t *testing.T) {
	t.Parallel()
	database, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := t.Context()
	id := newDBTestConversation(t, database, "my-project", t.TempDir())
	AddTestProject(t, database, "Docs")
	if err := database.SetConversationAgentWorking(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if err := database.MoveConversation(ctx, id, "Docs"); !errors.Is(err, ErrConflict) {
		t.Fatalf("working agent assignment: %v", err)
	}
	if err := database.SetConversationAgentWorking(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if _, err := database.AppendQueuedMessage(ctx, id, QueuedMessage{ID: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := database.MoveConversation(ctx, id, "Docs"); !errors.Is(err, ErrConflict) {
		t.Fatalf("pending queue allowed move: %v", err)
	}
	if _, err := database.ClearQueuedMessages(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := database.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		if _, err := q.CreateSubagentConversation(ctx, generated.CreateSubagentConversationParams{
			ConversationID: "active-child", ParentConversationID: &id,
		}); err != nil {
			return err
		}
		return q.SetConversationAgentWorking(ctx, generated.SetConversationAgentWorkingParams{ConversationID: "active-child", AgentWorking: true})
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.MoveConversation(ctx, id, "Docs"); !errors.Is(err, ErrConflict) {
		t.Fatalf("active child allowed move: %v", err)
	}
	if err := database.SetConversationAgentWorking(ctx, "active-child", false); err != nil {
		t.Fatal(err)
	}
	if err := database.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec(`INSERT INTO background_jobs(job_id,conversation_id,tool_use_id,command,pid,process_start_time,log_path,exit_path,started_at)
			VALUES('job','active-child','tool','true',1,1,'log','exit',CURRENT_TIMESTAMP)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.MoveConversation(ctx, id, "Docs"); !errors.Is(err, ErrConflict) {
		t.Fatalf("descendant job allowed move: %v", err)
	}
	if err := database.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec("UPDATE background_jobs SET exited=TRUE,notified=TRUE WHERE job_id='job'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.MoveConversation(ctx, id, "Docs"); err != nil {
		t.Fatal(err)
	}
}
